//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestVideoModelRoutesSelectAccountAndBillSnapshot(t *testing.T) {
	h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformWan)
	logs := &providerVideoUsageLogs{}
	accounts := make([]service.Account, 0, 2)
	for i, resolution := range []string{"720p", "1080p"} {
		accounts = append(accounts, service.Account{ID: int64(i + 1), Platform: service.PlatformWan, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 50, Priority: i, GroupIDs: []int64{24},
			Credentials: map[string]any{"api_key": "test-key", service.VideoModelRoutesCredential: map[string]service.VideoModelRoute{
				"sd-video": {DefaultResolution: resolution, Resolutions: map[string]string{resolution: "private-" + resolution}},
			}}})
	}
	repo := grokMediaSlotRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: accounts}}
	billing := &service.BillingService{}
	h.gatewayService = service.NewOpenAIGatewayService(repo, logs, nil, nil, nil, nil, bindings, h.cfg, nil, service.NewConcurrencyService(slots), billing, nil, nil, upstream,
		&service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, billing), nil, nil, nil, nil)
	upstream.call = func(req *http.Request, accountID int64) (*http.Response, error) {
		require.Equal(t, int64(2), accountID, "must skip the higher-priority account without 1080p support")
		body := `{"output":{"task_id":"job-1","task_status":"SUCCEEDED"}}`
		if req.Method == http.MethodPost {
			payload, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.Equal(t, "private-1080p", gjson.GetBytes(payload, "model").String())
			require.Equal(t, "1080P", gjson.GetBytes(payload, "parameters.resolution").String())
			body = `{"output":{"task_id":"job-1","task_status":"PENDING"}}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	contextFor := func(method, id string) (*gin.Context, *httptest.ResponseRecorder) {
		c, w := grokMediaSlotContext(context.Background(), method == http.MethodPost)
		key, _ := middleware.GetAPIKeyFromContext(c)
		key.Group.Platform, key.Group.RateMultiplier = service.PlatformWan, 1
		base, high := 0.1, 0.5
		key.Group.ModelPricing = []service.ChannelModelPricing{{Models: []string{"sd-video"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &base,
			Intervals: []service.PricingInterval{{TierLabel: "1080p", PerRequestPrice: &high}}}}
		body := ""
		if method == http.MethodPost {
			body = `{"model":"sd-video","resolution":"1080p","prompt":"waves","seconds":5}`
		}
		c.Request = httptest.NewRequest(method, "/v1/videos", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Params = gin.Params{{Key: "request_id", Value: id}}
		return c, w
	}
	c, w := contextFor(http.MethodPost, "")
	h.ProviderVideos(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "sd-video", gjson.Get(w.Body.String(), "model").String())
	id := gjson.Get(w.Body.String(), "id").String()
	require.NotEmpty(t, id)
	require.Len(t, bindings.pending, 1)
	// Changing the route after submission must not affect owner, model or tier.
	accounts[1].Credentials[service.VideoModelRoutesCredential] = map[string]service.VideoModelRoute{}
	for range 2 {
		c, w = contextFor(http.MethodGet, id)
		h.ProviderVideos(c)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, "sd-video", gjson.Get(w.Body.String(), "model").String())
		require.Equal(t, "1080p", gjson.Get(w.Body.String(), "resolution").String())
		slots.assertReleased(t)
	}
	require.Len(t, logs.logs, 1)
	require.Equal(t, "sd-video", logs.logs[0].Model)
	require.InDelta(t, 0.5, logs.logs[0].ActualCost, 1e-12)
}
