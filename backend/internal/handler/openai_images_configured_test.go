//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const configuredImagePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZQAAAABJRU5ErkJggg=="

type configuredImagesTransport struct {
	service.HTTPUpstream
	mu     sync.Mutex
	ids    []int64
	models []string
	format string
}

func (u *configuredImagesTransport) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.ids = append(u.ids, id)
	u.models = append(u.models, gjson.GetBytes(body, "model").String())
	u.mu.Unlock()
	payload := `{"data":[{"b64_json":"` + configuredImagePNG + `"}]}`
	if u.format == "url" {
		payload = `{"data":[{"url":"https://provider.example/image.png"}]}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
}

func (u *configuredImagesTransport) snapshot() ([]int64, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.ids...), append([]string(nil), u.models...)
}

type configuredImageDownload struct{}

func (configuredImageDownload) RoundTrip(*http.Request) (*http.Response, error) {
	data, _ := base64.StdEncoding.DecodeString(configuredImagePNG)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"image/png"}}, Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func configuredImagesAccounts(model, publicID string) []service.Account {
	var accounts []service.Account
	for i, typ := range []string{service.AccountTypeOAuth, service.AccountTypeSetupToken, service.AccountTypeAPIKey, service.AccountTypeAPIKey} {
		account := service.Account{ID: int64(i + 1), Platform: service.PlatformOpenAI, Type: typ, Priority: i,
			Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{
				"api_key": "test-key", "access_token": "test-token", "base_url": "https://compatible.example/v1",
				"model_mapping": map[string]any{publicID: model},
			}}
		if i == 3 {
			account.Credentials["openai_capabilities"] = []string{"images"}
			account.Credentials[service.OpenAIImageModelsCredentialKey] = []string{model}
		}
		accounts = append(accounts, account)
	}
	return accounts
}

func TestConfiguredImagesGatewayAndAsync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, model := range []string{"seedream-5.5", "vendor-art-v9"} {
		for _, publicID := range []string{model, "gpt-image-public-alias"} {
			for _, format := range []string{"url", "b64_json"} {
				for _, async := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/async=%v", model, publicID, format, async), func(t *testing.T) {
						transport := &configuredImagesTransport{format: format}
						gateway := newOpenAIImagesSizeRoutingTestHandler(t, configuredImagesAccounts(model, publicID), transport)
						body := fmt.Sprintf(`{"model":%q,"prompt":"draw a cat","response_format":%q}`, publicID, format)
						if !async {
							rec, _ := serveOpenAIImagesSizeRoutingRequest(gateway, 21, body)
							require.Equal(t, 200, rec.Code, rec.Body.String())
							require.Len(t, gjson.GetBytes(rec.Body.Bytes(), "data").Array(), 1)
						} else {
							tasks := service.NewImageTaskServiceWithUploader(
								&asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}},
								service.NewImageResultUploader(&passthroughTaskStorage{}, "images/", 0, &http.Client{Transport: configuredImageDownload{}}),
								time.Hour, time.Minute,
							)
							h := NewAsyncImageHandler(tasks, gateway)
							router := gin.New()
							groupID := int64(21)
							router.Use(func(c *gin.Context) {
								c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID, Group: &service.Group{ID: 21, Platform: service.PlatformOpenAI, AllowImageGeneration: true}, User: &service.User{ID: 7}})
								c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
							})
							router.POST("/v1/images/generations/async", h.Submit)
							router.GET("/v1/images/tasks/:task_id", h.Get)
							rec := httptest.NewRecorder()
							req := httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(body))
							req.Header.Set("Content-Type", "application/json")
							router.ServeHTTP(rec, req)
							require.Equal(t, 202, rec.Code, rec.Body.String())
							taskID := gjson.GetBytes(rec.Body.Bytes(), "task_id").String()
							require.Eventually(t, func() bool {
								got, err := tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, taskID)
								return err == nil && (got.Status == service.ImageTaskStatusCompleted || got.Status == service.ImageTaskStatusFailed)
							}, 3*time.Second, 10*time.Millisecond)
							poll := httptest.NewRecorder()
							router.ServeHTTP(poll, httptest.NewRequest(http.MethodGet, "/v1/images/tasks/"+taskID, nil))
							require.Equal(t, 200, poll.Code)
							require.Equal(t, service.ImageTaskStatusCompleted, gjson.GetBytes(poll.Body.Bytes(), "status").String(), poll.Body.String())
							require.Contains(t, poll.Body.String(), "offloaded.png")
						}
						ids, models := transport.snapshot()
						require.Equal(t, []int64{4}, ids, "skip OAuth, setup token and undeclared API key accounts")
						require.Equal(t, []string{model}, models)
					})
				}
			}
		}
	}
}

func TestConfiguredImagesGatewayRejectsUndeclaredModel(t *testing.T) {
	transport := &configuredImagesTransport{}
	// Only the model list changes; a broad model_mapping is insufficient to opt in.
	handler := newOpenAIImagesSizeRoutingTestHandler(t, configuredImagesAccounts("seedream-5.5", "seedream-5.5")[:3], transport)
	rec, _ := serveOpenAIImagesSizeRoutingRequest(handler, 21, `{"model":"seedream-5.5","prompt":"draw"}`)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Equal(t, "model_not_found", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	ids, _ := transport.snapshot()
	require.Empty(t, ids)
}

type configuredImageChannelRepo struct {
	service.ChannelRepository
	channel service.Channel
}

func (r configuredImageChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return []service.Channel{r.channel}, nil
}

func (r configuredImageChannelRepo) GetGroupPlatforms(context.Context, []int64) (map[int64]string, error) {
	return map[int64]string{21: service.PlatformOpenAI}, nil
}

func TestConfiguredImagesChannelMappingKeepsRequestedPricing(t *testing.T) {
	for _, source := range []string{service.BillingModelSourceRequested, service.BillingModelSourceChannelMapped, service.BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			pricedModel := "paint"
			if source != service.BillingModelSourceRequested {
				pricedModel = "seedream-5.5"
			}
			channels := service.NewChannelService(configuredImageChannelRepo{channel: service.Channel{
				ID: 1, Status: service.StatusActive, GroupIDs: []int64{21}, RestrictModels: true, BillingModelSource: source,
				ModelMapping: map[string]map[string]string{service.PlatformOpenAI: {"paint": "seedream-5.5", "seedream-5.5": "must-not-map-twice"}},
				ModelPricing: []service.ChannelModelPricing{{Platform: service.PlatformOpenAI, Models: []string{pricedModel}}},
			}}, nil, nil, nil, nil)
			transport := &configuredImagesTransport{}
			handler := newOpenAIImagesSizeRoutingTestHandler(t, configuredImagesAccounts("seedream-5.5", "seedream-5.5"), transport, channels)
			rec, _ := serveOpenAIImagesSizeRoutingRequest(handler, 21, `{"model":"paint","prompt":"draw"}`)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			ids, models := transport.snapshot()
			require.Equal(t, []int64{4}, ids)
			require.Equal(t, []string{"seedream-5.5"}, models)
		})
	}
}

type configuredImagesUsageRepo struct {
	service.UsageLogRepository
	logs chan service.UsageLog
}

func (r configuredImagesUsageRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.logs <- *log
	return true, nil
}

func TestConfiguredImagesGatewayRecordsActualResponseUsage(t *testing.T) {
	logs := make(chan service.UsageLog, 2)
	transport := &configuredImagesTransport{}
	accounts := configuredImagesAccounts("seedream-5.5", "paint")
	handler := newOpenAIImagesSizeRoutingTestHandler(t, accounts, transport)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	handler.gatewayService = service.NewOpenAIGatewayService(
		openAIImagesSizeRoutingAccountRepo{openAIImagesFailoverAccountRepo{accounts: accounts}},
		configuredImagesUsageRepo{logs: logs}, nil, nil, nil, nil, nil, cfg,
		nil, nil, service.NewBillingService(cfg, nil), nil, nil, transport,
		&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
	)
	price, groupID := 0.21, int64(21)
	for _, model := range []string{"paint", "undeclared"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(fmt.Sprintf(`{"model":%q,"prompt":"draw","size":"1024x1024"}`, model)))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID,
			Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, ImageCountBilling: true, ImageRateIndependent: true, ImageRateMultiplier: 1, ImagePrice1K: &price}, User: &service.User{ID: 7}})
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
		handler.Images(c)
		if model == "undeclared" {
			require.Equal(t, 404, rec.Code)
			require.Empty(t, logs, "rejected requests must not create image charges")
			continue
		}
		require.Equal(t, 200, rec.Code, rec.Body.String())
		select {
		case log := <-logs:
			require.Equal(t, 1, log.ImageCount)
			require.Equal(t, "paint", log.RequestedModel)
			require.NotNil(t, log.UpstreamModel)
			require.Equal(t, "seedream-5.5", *log.UpstreamModel)
			require.InDelta(t, 0.21, log.TotalCost, 1e-12)
			require.Equal(t, string(service.BillingModeImage), *log.BillingMode)
		case <-time.After(time.Second):
			t.Fatal("gateway did not record image response usage")
		}
	}
	// The async runner executes the same gateway and must retain mapped usage.
	tasks := service.NewImageTaskServiceWithUploader(&asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}},
		service.NewImageResultUploader(&passthroughTaskStorage{}, "images/", 0, &http.Client{Transport: configuredImageDownload{}}), time.Hour, time.Minute)
	asyncHandler := NewAsyncImageHandler(tasks, handler)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"model":"paint","prompt":"draw","size":"1024x1024"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 9, UserID: 7, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, ImageCountBilling: true, ImageRateIndependent: true, ImageRateMultiplier: 1, ImagePrice1K: &price}, User: &service.User{ID: 7}})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7})
	asyncHandler.Submit(c)
	require.Equal(t, 202, rec.Code, rec.Body.String())
	taskID := gjson.GetBytes(rec.Body.Bytes(), "task_id").String()
	require.Eventually(t, func() bool {
		got, err := tasks.Get(context.Background(), service.ImageTaskOwner{UserID: 7, APIKeyID: 9}, taskID)
		return err == nil && got.Status == service.ImageTaskStatusCompleted
	}, time.Second, 10*time.Millisecond)
	select {
	case log := <-logs:
		require.Equal(t, 1, log.ImageCount)
		require.Equal(t, "paint", log.RequestedModel)
		require.Equal(t, "seedream-5.5", *log.UpstreamModel)
		require.InDelta(t, 0.21, log.TotalCost, 1e-12)
		require.Equal(t, service.RequestTypeImageAsync, log.RequestType)
	case <-time.After(time.Second):
		t.Fatal("async gateway did not record image response usage")
	}
}
