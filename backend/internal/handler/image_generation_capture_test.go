package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageGenerationCaptureSurvivesDetachedBillingAndFreezesAtSubmission(t *testing.T) {
	capture := service.NewImageGenerationCapture("application/json", []byte(`{"prompt":"cat"}`))
	capture.ObserveResponse([]byte(`{"data":[{"b64_json":"first"}]}`), false)
	parent := service.WithImageGenerationCapture(context.Background(), capture)
	var saved *service.ImageGenerationDetail
	task, abandon := wrapUsageRecordTaskContext(parent, func(ctx context.Context) { saved = service.ImageGenerationDetailFromContext(ctx) })
	defer abandon()
	capture.ObserveResponse([]byte(`{"data":[{"b64_json":"second"}]}`), false)
	task(context.Background())
	require.NotNil(t, saved)
	require.Len(t, saved.Images, 1)
	require.Equal(t, "first", saved.Images[0].B64JSON)
}

func TestBeginImageGenerationCaptureMappedGeminiModel(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := []byte(`{"model":"my-image","messages":[{"role":"user","content":"cat"}]}`)
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: "gemini"}})
	defer beginImageGenerationCapture(c, body)()
	require.Nil(t, service.ImageGenerationCaptureFromContext(c.Request.Context()))
	defer beginImageGenerationCapture(c, body, "gemini-2.5-flash-image")()
	detail := service.ImageGenerationDetailFromContext(c.Request.Context())
	require.NotNil(t, detail)
	require.Equal(t, "cat", detail.Prompt)
	require.Equal(t, "my-image", detail.RequestParams["model"])
}

func TestBeginImageGenerationCaptureIntent(t *testing.T) {
	for _, tt := range []struct {
		name, path, body, platform string
		want                       bool
	}{
		{"dedicated images", "/v1/images/generations", `{"prompt":"cat"}`, "openai", true},
		{"native Gemini alias", "/v1beta/models/my-image:generateContent", `{"contents":[{"parts":[{"text":"cat"}]}],"generationConfig":{"responseModalities":["IMAGE"]}}`, "gemini", true},
		{"ordinary text", "/v1/responses", `{"model":"gpt-5","input":"hello"}`, "openai", false},
		{"passive Grok namespace", "/v1/responses", `{"model":"grok-4.5","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen"}]}],"tool_choice":"auto","input":"write code"}`, "grok", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: tt.platform}})
			defer beginImageGenerationCapture(c, []byte(tt.body))()
			require.Equal(t, tt.want, service.ImageGenerationCaptureFromContext(c.Request.Context()) != nil)
		})
	}
}

func TestImageGenerationUsageQueueFreezesAllGeminiOutputsWithoutDeadlockingSyncBilling(t *testing.T) {
	capture := service.NewImageGenerationCapture("application/json", []byte(`{"prompt":"cat","n":2}`))
	queue := &imageGenerationUsageQueue{}
	ctx := context.WithValue(service.WithImageGenerationCapture(context.Background(), capture), imageGenerationUsageQueueKey{}, queue)
	h := &GatewayHandler{}
	var records []*service.ImageGenerationDetail
	for _, output := range []string{"first", "second"} {
		capture.ObserveResponse([]byte(`{"data":[{"b64_json":"`+output+`"}]}`), false)
		h.submitUsageRecordTask(ctx, func(taskCtx context.Context) {
			records = append(records, service.ImageGenerationDetailFromContext(taskCtx))
		})
	}
	require.Empty(t, records)
	queue.flush()
	require.Len(t, records, 2)
	for _, detail := range records {
		require.Len(t, detail.Images, 2)
		require.Equal(t, "first", detail.Images[0].B64JSON)
		require.Equal(t, "second", detail.Images[1].B64JSON)
	}
}

func TestImageGenerationErrorDetailsSurviveQueueOverflow(t *testing.T) {
	for _, limit := range []string{"byte budget", "full queue"} {
		t.Run(limit, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 1)
			previousBytes := opsErrorLogQueueBytes.Load()
			t.Cleanup(func() { opsErrorLogQueueBytes.Store(previousBytes) })
			if limit == "byte budget" {
				opsErrorLogQueueBytes.Store(opsErrorLogMaxQueueBytes)
			} else {
				opsErrorLogQueue <- opsErrorLogJob{}
			}
			repo := &ingressRejectOpsRepo{}
			ops := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			detail := &service.ImageGenerationDetail{
				Prompt: "cat", RequestParams: map[string]any{"size": "1024x1024"},
				Images: []service.GeneratedImage{{B64JSON: "partial", Partial: true}},
			}
			enqueueOpsErrorLog(ops, &service.OpsInsertErrorLogInput{ImageGeneration: detail, ErrorType: "upstream_error"})
			require.Len(t, repo.entries, 1)
			require.Equal(t, detail, repo.entries[0].ImageGeneration)
			require.Equal(t, int64(0), OpsErrorLogDroppedTotal())
			require.Equal(t, int64(1), OpsErrorLogProcessedTotal())
			require.Equal(t, int64(0), OpsErrorLogQueueLength())
		})
	}
}
