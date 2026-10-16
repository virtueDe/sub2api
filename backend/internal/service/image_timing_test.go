package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagetiming"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestImageTimingRecordsEachImageAndStorageFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			ctx := imagetiming.Enable(logger.IntoContext(context.Background(), zap.New(core)))
			ctx = imagetiming.WithFields(ctx, zap.String("task_id", "task-123"))
			storage := &fakeImageStorage{}
			if fail {
				storage.err = errors.New("storage failure with signed-url-secret")
			}
			uploader := NewImageResultUploader(storage, "images/", 0, nil)
			b64 := base64.StdEncoding.EncodeToString(pngBytes)
			result := json.RawMessage(`{"data":[{"b64_json":"` + b64 + `"},{"b64_json":"` + b64 + `"}]}`)
			_, err := uploader.Rewrite(ctx, "task-123", result)
			entries := logs.FilterMessage("image.timing").FilterField(zap.String("stage", "result_image_upload")).All()
			if fail {
				require.Error(t, err)
				require.Len(t, entries, 1)
				require.Equal(t, "error", entries[0].ContextMap()["outcome"])
			} else {
				require.NoError(t, err)
				require.Len(t, entries, 2)
			}
			for i, entry := range entries {
				require.EqualValues(t, i, entry.ContextMap()["image_index"])
				require.Equal(t, "task-123", entry.ContextMap()["task_id"])
				require.NotContains(t, entry.ContextMap(), "error")
			}
		})
	}
}

func TestImageTimingAsyncTaskIncludesStorageFailure(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := imagetiming.Enable(logger.IntoContext(context.Background(), zap.New(core)))
	store := &imageTaskMemoryStore{}
	uploader := NewImageResultUploader(&fakeImageStorage{err: errors.New("storage failed")}, "images/", 0, nil)
	svc := NewImageTaskServiceWithUploader(store, uploader, time.Hour, time.Minute)
	task, err := svc.Create(ctx, ImageTaskOwner{UserID: 1, APIKeyID: 1})
	require.NoError(t, err)
	ctx, finish := BeginImageTaskTiming(ctx)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	err = svc.Complete(ctx, task.ID, 200, json.RawMessage(`{"data":[{"b64_json":"`+b64+`"}]}`))
	require.NoError(t, err, "Complete reports persistence errors; recording a failed task is successful persistence")
	finish(err)
	entries := logs.FilterMessage("image.timing").FilterField(zap.String("stage", "async_task")).All()
	require.Len(t, entries, 1)
	require.Equal(t, "failed", entries[0].ContextMap()["task_status"])
	require.Equal(t, "error", entries[0].ContextMap()["outcome"])
	finalize := logs.FilterMessage("image.timing").FilterField(zap.String("stage", "task_finalize")).All()[0]
	require.Equal(t, entries[0].ContextMap()["span_id"], finalize.ContextMap()["parent_span_id"])
}

type imageTimingWriter struct {
	gin.ResponseWriter
	beforeWrite func()
}

func (w imageTimingWriter) Write(p []byte) (int, error) {
	w.beforeWrite()
	return w.ResponseWriter.Write(p)
}

func TestImageTimingFirstImageObservedBeforeClientWrite(t *testing.T) {
	c, recorder := newOpenAIImagesTestContext(t, []byte(`{"model":"gpt-image-1","prompt":"cat","stream":true}`))
	core, logs := observer.New(zap.InfoLevel)
	c.Request = c.Request.WithContext(imagetiming.Enable(logger.IntoContext(c.Request.Context(), zap.New(core))))
	stream := "data: {\"type\":\"image_generation.completed\",\"b64_json\":\"aGVsbG8=\"}\n\n"
	c.Writer = imageTimingWriter{ResponseWriter: c.Writer, beforeWrite: func() {
		entries := logs.FilterMessage("image.timing").FilterField(zap.String("stage", "wait_first_complete_image")).All()
		require.Len(t, entries, 1, "first image must be recorded before a potentially blocking client write")
		require.Equal(t, true, entries[0].ContextMap()["image_received"])
	}}
	svc := newOpenAIImagesTestService(nil)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}
	_, count, _, _, err := svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, stream, recorder.Body.String())
}

func TestImageTimingFirstImageFromJSONStreamFallback(t *testing.T) {
	c, recorder := newOpenAIImagesTestContext(t, []byte(`{"model":"gpt-image-1","prompt":"cat","stream":true}`))
	core, logs := observer.New(zap.InfoLevel)
	c.Request = c.Request.WithContext(imagetiming.Enable(logger.IntoContext(c.Request.Context(), zap.New(core))))
	payload := `{"data":[{"b64_json":"aGVsbG8="}]}`
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(payload))}
	svc := newOpenAIImagesTestService(nil)
	_, count, _, _, err := svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, payload, recorder.Body.String())
	entries := logs.FilterMessage("image.timing").FilterField(zap.String("stage", "wait_first_complete_image")).All()
	require.Len(t, entries, 1)
	require.Equal(t, true, entries[0].ContextMap()["image_received"])
	require.Equal(t, true, entries[0].ContextMap()["json_fallback"])
}
