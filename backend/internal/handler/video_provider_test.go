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

type providerVideoUsageLogs struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *providerVideoUsageLogs) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.logs = append(r.logs, log)
	return true, nil
}

func TestProviderVideoUnifiedLifecycleAndOwnership(t *testing.T) {
	for _, tc := range []struct{ platform, model, create, pending, path, failed, completed string }{
		{service.PlatformSeedance, "seedance-public", `{"id":"job-1","status":"queued"}`, `{"id":"job-1","status":"running"}`, "/contents/generations/tasks", `{"id":"job-1","status":"failed"}`, `{"id":"job-1","status":"succeeded"}`},
		{service.PlatformWan, "wan2.6-t2v", `{"output":{"task_id":"job-1","task_status":"PENDING"}}`, `{"output":{"task_id":"job-1","task_status":"RUNNING"}}`, "/services/aigc/video-generation/video-synthesis", `{"output":{"task_id":"job-1","task_status":"FAILED"}}`, `{"output":{"task_id":"job-1","task_status":"SUCCEEDED"}}`},
		{service.PlatformKling, "kling-v2-6", `{"code":0,"data":{"task_id":"job-1","task_status":"submitted"}}`, `{"code":0,"data":{"task_id":"job-1","task_status":"processing"}}`, "/videos/text2video", `{"code":0,"data":{"task_id":"job-1","task_status":"failed"}}`, `{"code":0,"data":{"task_id":"job-1","task_status":"succeed"}}`},
		{service.PlatformSora, "sora-2", `{"id":"job-1","status":"queued"}`, `{"id":"job-1","status":"in_progress"}`, "/videos", `{"id":"job-1","status":"failed"}`, `{"id":"job-1","status":"completed"}`},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			h, slots, bindings, upstream := newGrokMediaSlotHandler(t, false, false, tc.platform)
			logs := &providerVideoUsageLogs{}
			repo := grokMediaSlotRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: []service.Account{{ID: 1, Platform: tc.platform, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 50, GroupIDs: []int64{24}, Credentials: map[string]any{"api_key": "test-key"}}}}}
			billing := &service.BillingService{}
			h.gatewayService = service.NewOpenAIGatewayService(repo, logs, nil, nil, nil, nil, bindings, h.cfg, nil, service.NewConcurrencyService(slots), billing, nil, nil, upstream,
				&service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, billing), nil, nil, nil, nil)
			var owner int64
			statusBody := tc.pending
			upstream.call = func(req *http.Request, id int64) (*http.Response, error) {
				body := statusBody
				if strings.HasSuffix(req.URL.Path, "/content") {
					require.Equal(t, owner, id)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"video/mp4"}}, Body: io.NopCloser(strings.NewReader("video"))}, nil
				}
				if req.Method == http.MethodPost {
					owner = id
					body = tc.create
					require.True(t, strings.HasSuffix(req.URL.Path, tc.path), req.URL.Path)
					payload, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					field := "model"
					if tc.platform == service.PlatformKling {
						field = "model_name"
					}
					require.Equal(t, tc.model, gjson.GetBytes(payload, field).String())
				} else {
					require.Equal(t, owner, id)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}
			contextFor := func(method, id string) (*gin.Context, *httptest.ResponseRecorder) {
				c, w := grokMediaSlotContext(context.Background(), method == http.MethodPost)
				key, _ := middleware.GetAPIKeyFromContext(c)
				key.Group.Platform = tc.platform
				key.Group.RateMultiplier = 1
				price := 0.1
				key.Group.ModelPricing = []service.ChannelModelPricing{{Platform: tc.platform, Models: []string{tc.model}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &price}}
				body := ""
				if method == http.MethodPost {
					body = `{"model":"` + tc.model + `","prompt":"waves","seconds":20,"size":"1280x720"}`
				}
				c.Request = httptest.NewRequest(method, "/v1/videos", strings.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Params = gin.Params{{Key: "request_id", Value: id}}
				return c, w
			}
			c, w := contextFor(http.MethodPost, "")
			h.ProviderVideos(c)
			require.Equal(t, 200, w.Code, w.Body.String())
			slots.assertReleased(t)
			id := gjson.Get(w.Body.String(), "id").String()
			require.True(t, strings.HasPrefix(id, "video_"))
			require.Equal(t, "queued", gjson.Get(w.Body.String(), "status").String())
			require.Len(t, bindings.pending, 1)
			statusEndpoint, taskKey, err := service.ParsePublicVideoTaskID(id)
			require.NoError(t, err)
			c, w = contextFor(http.MethodGet, id)
			h.ProviderVideos(c)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, "in_progress", gjson.Get(w.Body.String(), "status").String())
			require.Equal(t, tc.model, gjson.Get(w.Body.String(), "model").String())
			slots.assertReleased(t)
			// GET has no model: composite pricing/quota context must be recovered
			// from the task provider, rather than another platform's same-name model.
			c, w = contextFor(http.MethodGet, id)
			compositeKey, _ := middleware.GetAPIKeyFromContext(c)
			compositeKey.Group.Platform = service.PlatformComposite
			h.ProviderVideos(c)
			require.Equal(t, 200, w.Code, w.Body.String())
			resolvedPlatform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context())
			require.True(t, ok)
			require.Equal(t, tc.platform, resolvedPlatform)
			for _, other := range []string{"user", "key", "group", "provider", "id"} {
				c, w = contextFor(http.MethodGet, id)
				key, _ := middleware.GetAPIKeyFromContext(c)
				switch other {
				case "user":
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 99, Concurrency: 5})
				case "key":
					key.ID = 99
				case "group":
					group := int64(99)
					key.GroupID = &group
				case "provider":
					key.Group.Platform = service.PlatformGrok
				case "id":
					c.Params = gin.Params{{Key: "request_id", Value: service.PublicVideoTaskID("wan:other")}}
				}
				before := upstream.calls
				h.ProviderVideos(c)
				require.Equal(t, 404, w.Code, other+": "+w.Body.String())
				require.Equal(t, before, upstream.calls)
				slots.assertReleased(t)
			}
			statusBody = tc.failed
			c, w = contextFor(http.MethodGet, id)
			h.ProviderVideos(c)
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Equal(t, "failed", gjson.Get(w.Body.String(), "status").String())
			require.Empty(t, bindings.billed)
			require.Empty(t, logs.logs)
			statusBody = tc.completed
			for range 2 {
				c, w = contextFor(http.MethodGet, id)
				h.ProviderVideos(c)
				require.Equal(t, 200, w.Code, w.Body.String())
				require.Equal(t, "completed", gjson.Get(w.Body.String(), "status").String())
			}
			// Native polling and Sora downloads must share the completed task's claim.
			c, w = contextFor(http.MethodGet, id)
			switch tc.platform {
			case service.PlatformSeedance:
				c.Params = gin.Params{{Key: "task_id", Value: "job-1"}}
				h.SeedanceTasks(c)
			case service.PlatformSora:
				c.Request = httptest.NewRequest(http.MethodGet, "/v1/videos/"+id+"/content", nil)
				h.ProviderVideos(c)
			default:
				c.Params = gin.Params{{Key: "request_id", Value: "job-1"}}
				h.NativeVideoTasks(statusEndpoint)(c)
			}
			require.Equal(t, 200, w.Code, w.Body.String())
			require.Len(t, logs.logs, 1)
			require.Len(t, bindings.billed, 1)
			require.Equal(t, service.StableGrokVideoBillingRequestID(taskKey), logs.logs[0].RequestID)
			require.InDelta(t, 0.1, logs.logs[0].ActualCost, 1e-12)
			require.Equal(t, string(service.BillingModePerRequest), *logs.logs[0].BillingMode)
		})
	}
}

func TestProviderVideoPerRequestCompletionClaimsOnceWithoutUsage(t *testing.T) {
	for _, platform := range []string{service.PlatformSeedance, service.PlatformWan, service.PlatformKling, service.PlatformSora} {
		t.Run(platform, func(t *testing.T) {
			h, _, bindings, _ := newGrokMediaSlotHandler(t, false, false, platform)
			c, _ := grokMediaSlotContext(context.Background(), false)
			key, _ := middleware.GetAPIKeyFromContext(c)
			key.Group.Platform = platform
			price := 0.5
			key.Group.ModelPricing = []service.ChannelModelPricing{{Models: []string{"video"}, BillingMode: service.BillingModePerRequest, PerRequestPrice: &price}}
			subject, _ := middleware.GetAuthSubjectFromContext(c)
			taskID := platform + ":task"
			require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), taskID, subject.UserID, key.ID, service.GrokVideoPendingBilling{Model: "video"}))
			// Queued/failed results do not consume the claim, even if they carry usage.
			require.Nil(t, prepareProviderVideoBilling(context.Background(), h, key, subject, taskID, &service.OpenAIForwardResult{Usage: service.OpenAIUsage{OutputTokens: 100}}))
			require.Empty(t, bindings.billed)
			completed := &service.OpenAIForwardResult{VideoCount: 1}
			for i := range 20 {
				bill := prepareProviderVideoBilling(context.Background(), h, key, subject, taskID, completed)
				if i == 0 {
					require.NotNil(t, bill)
					require.Equal(t, service.StableGrokVideoBillingRequestID(taskID), bill.RequestID)
					require.Zero(t, bill.VideoDurationSeconds)
					require.Zero(t, bill.Usage.OutputTokens)
				} else {
					require.Nil(t, bill)
				}
			}
			require.Len(t, bindings.billed, 1)
		})
	}
}

func TestSeedanceTokenCompletionWaitsForUsageBeforeClaim(t *testing.T) {
	h, _, bindings, _ := newGrokMediaSlotHandler(t, false, false, service.PlatformSeedance)
	c, _ := grokMediaSlotContext(context.Background(), false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformSeedance
	price := 0.000002
	key.Group.ModelPricing = []service.ChannelModelPricing{{Models: []string{"video"}, BillingMode: service.BillingModeToken, OutputPrice: &price}}
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	taskID := "seedance:task"
	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), taskID, subject.UserID, key.ID, service.GrokVideoPendingBilling{Model: "video"}))
	result := &service.OpenAIForwardResult{VideoCount: 1}
	require.Nil(t, prepareProviderVideoBilling(context.Background(), h, key, subject, taskID, result))
	require.Empty(t, bindings.billed)
	result.Usage.OutputTokens = 123
	require.NotNil(t, prepareProviderVideoBilling(context.Background(), h, key, subject, taskID, result))
	require.Nil(t, prepareProviderVideoBilling(context.Background(), h, key, subject, taskID, result))
}

func TestProviderVideoRejectsMissingPricingBeforeSubmission(t *testing.T) {
	h, slots, _, upstream := newGrokMediaSlotHandler(t, false, false, service.PlatformWan)
	c, w := grokMediaSlotContext(context.Background(), true)
	key, _ := middleware.GetAPIKeyFromContext(c)
	key.Group.Platform = service.PlatformWan
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"wan2.6-t2v","prompt":"waves","seconds":5,"size":"1280x720"}`))
	h.ProviderVideos(c)
	require.Equal(t, 400, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "video_pricing_required")
	require.Zero(t, upstream.calls)
	slots.assertReleased(t)
}
