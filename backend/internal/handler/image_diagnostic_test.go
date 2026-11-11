package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type imageDiagnosticHandlerSettings struct{ service.SettingRepository }

func (imageDiagnosticHandlerSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{service.SettingKeyImageDiagnosticEnabled: "true", service.SettingKeyImageDiagnosticUserIDs: "[7]"}, nil
}

func diagnosticTestContext(t *testing.T, userID int64, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(service.WithImageDiagnosticSettings(c.Request.Context(), service.NewSettingService(imageDiagnosticHandlerSettings{}, &config.Config{})))
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: userID, Group: &service.Group{Platform: "openai"}})
	return c, recorder
}

func TestImageDiagnosticHandlerTrustsAuthenticatedUserAndPreservesResponse(t *testing.T) {
	for _, userID := range []int64{0, 7, 8} {
		for _, response := range []string{`{"data":[{"b64_json":"private-binary","url":"https://example.test/cat.png"}]}`, "data: {\"data\":[{\"url\":\"https://example.test/cat.png\"}]}\n\n"} {
			c, recorder := diagnosticTestContext(t, userID, "/v1/images/generations")
			original := c.Writer
			done := beginImageGenerationCapture(c, []byte(`{"prompt":"cat","user":7,"user_id":7}`))
			capture := service.ImageGenerationCaptureFromContext(c.Request.Context())
			require.NotNil(t, capture, "billing marker is always present")
			c.Header("Content-Type", "text/event-stream")
			if response[0] == '{' {
				c.Header("Content-Type", "application/json")
			}
			_, err := c.Writer.WriteString(response)
			require.NoError(t, err)
			done()
			require.Same(t, original, c.Writer)
			require.Equal(t, response, recorder.Body.String())
			if userID != 7 {
				require.Nil(t, capture.Snapshot())
				continue
			}
			detail := capture.Snapshot()
			require.Equal(t, "cat", detail.Prompt)
			require.Len(t, detail.Images, 1)
			require.Empty(t, detail.Images[0].B64JSON)
			require.Equal(t, int64(7), service.ImageGenerationDetailOwner(detail))
		}
	}
}

func TestImageDiagnosticAsyncAndGeminiChildWriters(t *testing.T) {
	c, submitRecorder := diagnosticTestContext(t, 7, "/v1/images/generations")
	body := []byte(`{"prompt":"cat","n":2}`)
	c.Request = c.Request.WithContext(service.WithImageGenerationCapture(c.Request.Context(), newImageCaptureForRequest(c, body)))
	c.JSON(http.StatusAccepted, gin.H{"task_id": "task-only"})
	child, recorder, cancel, err := newAsyncImageContext(c, body, time.Minute)
	require.NoError(t, err)
	defer cancel()
	for _, path := range []string{"/v1/images/generations", "/v1beta/models/gemini-2.5-flash-image:generateContent"} {
		child.Request.URL.Path = path
		done := beginImageGenerationCapture(child, body, "gemini-2.5-flash-image")
		_, err = child.Writer.WriteString(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"private-binary"}}]}}]}`)
		require.NoError(t, err)
		done()
	}
	detail := service.ImageGenerationDetailFromContext(child.Request.Context())
	require.Equal(t, "cat", detail.Prompt)
	require.Len(t, detail.Images, 2)
	require.Equal(t, http.StatusAccepted, submitRecorder.Code)
	require.Contains(t, recorder.Body.String(), "private-binary", "client output is intact")
	require.NotContains(t, service.ImageGenerationDetailJSON(detail), "private-binary")

	// Detached usage receives a frozen authorized snapshot, even after the caller mutates it.
	var saved *service.ImageGenerationDetail
	task, abandon := wrapUsageRecordTaskContext(child.Request.Context(), func(ctx context.Context) { saved = service.ImageGenerationDetailFromContext(ctx) })
	defer abandon()
	task(context.Background())
	require.Equal(t, int64(7), service.ImageGenerationDetailOwner(saved))
	require.Len(t, saved.Images, 2)

	repo := &ingressRejectOpsRepo{}
	h := &AsyncImageHandler{ops: service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)}
	h.recordAsyncFailure("diagnostic-test", "openai", child, 503, []byte(`{"type":"billing_error","message":"unavailable"}`))
	require.Len(t, repo.entries, 1)
	require.Equal(t, "cat", repo.entries[0].ImageGeneration.Prompt)
	require.Equal(t, int64(7), service.ImageGenerationDetailOwner(repo.entries[0].ImageGeneration))
}
