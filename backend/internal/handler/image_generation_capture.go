package handler

import (
	"context"
	"strings"

	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type imageGenerationUsageQueueKey struct{}

// Gemini n>1 uses several native calls for one public request. Wait until all
// output is collected before freezing that request's persisted details.
type imageGenerationUsageQueue struct {
	tasks []func()
}

func (q *imageGenerationUsageQueue) flush() {
	for _, submit := range q.tasks {
		submit()
	}
}

func delayImageGenerationUsageTask(parent context.Context, task service.UsageRecordTask, submit func(context.Context, service.UsageRecordTask)) bool {
	if parent == nil {
		return false
	}
	queue, _ := parent.Value(imageGenerationUsageQueueKey{}).(*imageGenerationUsageQueue)
	if queue == nil {
		return false
	}
	// Hold the reservation while the child returns and subsequent images are generated.
	done := service.InflightReservationFromContext(parent).Acquire()
	parent = context.WithValue(parent, imageGenerationUsageQueueKey{}, (*imageGenerationUsageQueue)(nil))
	queue.tasks = append(queue.tasks, func() {
		defer done()
		submit(parent, task)
	})
	return true
}

type imageGenerationWriter struct {
	gin.ResponseWriter
	capture *service.ImageGenerationCapture
}

func (w *imageGenerationWriter) Write(data []byte) (int, error) {
	w.capture.ObserveResponse(data, strings.Contains(w.Header().Get("Content-Type"), "text/event-stream"))
	return w.ResponseWriter.Write(data)
}

func (w *imageGenerationWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

func beginImageGenerationCapture(c *gin.Context, body []byte, mappedModels ...string) func() {
	if c == nil || c.Request == nil {
		return func() {}
	}
	capture := service.ImageGenerationCaptureFromContext(c.Request.Context())
	if writer, ok := c.Writer.(*imageGenerationWriter); ok && writer.capture == capture {
		return func() {}
	}
	if capture == nil {
		model := gjson.GetBytes(body, "model").String()
		if model == "" {
			model = strings.TrimPrefix(strings.Split(c.Param("modelAction"), ":")[0], "/")
		}
		resolvedModel, _ := service.ResolvedUpstreamModelFromContext(c.Request.Context())
		geminiImage := service.IsGeminiImageGenerationModel(model) || service.IsGeminiImageGenerationModel(resolvedModel)
		for _, mappedModel := range mappedModels {
			geminiImage = geminiImage || service.IsGeminiImageGenerationModel(mappedModel)
		}
		if strings.Contains(c.Request.URL.Path, "/models/") {
			for _, path := range []string{"generationConfig.responseModalities", "generation_config.response_modalities"} {
				for _, modality := range gjson.GetBytes(body, path).Array() {
					geminiImage = geminiImage || strings.EqualFold(modality.String(), "IMAGE")
				}
			}
		}
		apiKey, _ := middleware.GetAPIKeyFromContext(c)
		platform := openAICompatibleRequestPlatform(c.Request.Context(), apiKey)
		if !service.IsImageGenerationIntentForPlatform(c.Request.URL.Path, model, body, platform) && !geminiImage {
			return func() {}
		}
		capture = service.NewImageGenerationCapture(c.GetHeader("Content-Type"), body)
		c.Request = c.Request.WithContext(service.WithImageGenerationCapture(c.Request.Context(), capture))
	}
	original := c.Writer
	c.Writer = &imageGenerationWriter{ResponseWriter: original, capture: capture}
	return func() { c.Writer = original }
}
