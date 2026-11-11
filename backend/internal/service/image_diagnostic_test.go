package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

type imageDiagnosticRepo struct {
	SettingRepository
	mu     sync.Mutex
	values map[string]string
	reads  int
	err    error
}

func (r *imageDiagnosticRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	values := map[string]string{}
	for _, k := range keys {
		values[k] = r.values[k]
	}
	return values, r.err
}
func (r *imageDiagnosticRepo) SetMultiple(_ context.Context, updates map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range updates {
		r.values[k] = v
	}
	return nil
}

func (r *imageDiagnosticRepo) GetAll(ctx context.Context) (map[string]string, error) {
	return r.GetMultiple(ctx, []string{SettingKeyImageDiagnosticEnabled, SettingKeyImageDiagnosticUserIDs})
}

func (r *imageDiagnosticRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.values[key], nil
}

func TestImageDiagnosticPolicyDefaultsAndSelectedUsers(t *testing.T) {
	originalMapping := xai.RuntimeModelMappingOptions()
	t.Cleanup(func() { xai.SetRuntimeModelMappingOptions(originalMapping) })
	ctx := context.Background()
	repo := &imageDiagnosticRepo{values: map[string]string{}}
	svc := NewSettingService(repo, &config.Config{})
	require.False(t, svc.IsImageDiagnosticUserEnabled(ctx, 7))
	repo.values[SettingKeyImageDiagnosticEnabled] = "true"
	repo.values[SettingKeyImageDiagnosticUserIDs] = "[]"
	svc.invalidateImageDiagnosticPolicy()
	require.False(t, svc.IsImageDiagnosticUserEnabled(ctx, 7), "empty never means all")
	repo.values[SettingKeyImageDiagnosticUserIDs] = "[7,9,7]"
	svc.invalidateImageDiagnosticPolicy()
	require.True(t, svc.IsImageDiagnosticUserEnabled(ctx, 7))
	reads := repo.reads
	for i := 0; i < 20; i++ {
		require.False(t, svc.IsImageDiagnosticUserEnabled(ctx, 8))
		require.True(t, svc.IsImageDiagnosticUserEnabled(ctx, 9))
	}
	require.Equal(t, reads, repo.reads, "hot requests use cached membership")
	settings := svc.parseSettings(repo.values)
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.JSONEq(t, "[7,9]", repo.values[SettingKeyImageDiagnosticUserIDs])
	settings.ImageDiagnosticEnabled = false
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.False(t, svc.IsImageDiagnosticUserEnabled(ctx, 7), "save applies without waiting for TTL")
	settings.ImageDiagnosticEnabled = true
	settings.ImageDiagnosticUserIDs = []int64{9}
	require.NoError(t, svc.UpdateSettings(ctx, settings))
	require.False(t, svc.IsImageDiagnosticUserEnabled(ctx, 7))
	require.True(t, svc.IsImageDiagnosticUserEnabled(ctx, 9))
	ctx = WithImageDiagnosticSettings(ctx, svc)
	require.Nil(t, NewImageGenerationCaptureForUser(ctx, 7, "application/json", []byte(`{"prompt":"private"}`)).Snapshot())
	require.Equal(t, "private", NewImageGenerationCaptureForUser(ctx, 9, "application/json", []byte(`{"prompt":"private"}`)).Snapshot().Prompt)
}

func TestImageDiagnosticPolicyFailsClosed(t *testing.T) {
	for _, raw := range []string{"", "null", "invalid", "[-1]", "[0]", "[9007199254740992]"} {
		repo := &imageDiagnosticRepo{values: map[string]string{SettingKeyImageDiagnosticEnabled: "true", SettingKeyImageDiagnosticUserIDs: raw}}
		svc := NewSettingService(repo, &config.Config{})
		require.False(t, svc.IsImageDiagnosticUserEnabled(context.Background(), 7), raw)
	}
	repo := &imageDiagnosticRepo{values: map[string]string{}, err: errors.New("database unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	require.False(t, svc.IsImageDiagnosticUserEnabled(context.Background(), 7))
	require.False(t, svc.IsImageDiagnosticUserEnabled(context.Background(), 7))
	require.Equal(t, 1, repo.reads)
	_, err := validateImageDiagnosticUserIDs(make([]int64, 51))
	require.Error(t, err)
	_, err = svc.buildSystemSettingsUpdates(context.Background(), &SystemSettings{ImageDiagnosticUserIDs: []int64{-1}})
	require.Error(t, err)
}

func TestImageDiagnosticCaptureMetadataAndOwner(t *testing.T) {
	capture := newAuthorizedImageCapture(7, "application/json", []byte(`{"prompt":"draw a cat","size":"1024x1024","api_key":"secret","image":"data:image/png;base64,AAAA","generationConfig":{"responseModalities":["IMAGE"],"imageConfig":{"aspectRatio":"1:1"}}}`))
	capture.ObserveResponse([]byte(`{"data":[{"url":"https://example.test/cat.png","b64_json":"private-image","revised_prompt":"a cat"}]}`), false)
	detail := capture.Snapshot()
	require.Equal(t, "draw a cat", detail.Prompt)
	require.NotContains(t, detail.RequestParams, "api_key")
	require.NotContains(t, detail.RequestParams, "image")
	require.Len(t, detail.Images, 1)
	require.Empty(t, detail.Images[0].B64JSON)
	require.Nil(t, ImageGenerationDetailJSONForUser(detail, 8))
	encoded, ok := ImageGenerationDetailJSONForUser(detail, 7).(string)
	require.True(t, ok)
	require.NotContains(t, encoded, "secret")
	require.NotContains(t, encoded, "private-image")
	parsed, err := ParseImageGenerationDetail(encoded)
	require.NoError(t, err)
	require.Nil(t, ImageGenerationDetailJSON(parsed), "historical JSON cannot authorize persistence")
	detail.RequestParams["api_key"] = "secret"
	detail.RequestParams["images"] = []any{"data:image/png;base64,hidden"}
	detail.Images[0].B64JSON = "private-image"
	encoded, ok = ImageGenerationDetailJSON(detail).(string)
	require.True(t, ok)
	require.NotContains(t, encoded, "secret")
	require.NotContains(t, encoded, "base64")
	require.NotContains(t, encoded, "private-image")
	ctx := WithImageGenerationDetail(context.Background(), detail)
	detail.RequestParams["size"] = "changed"
	require.Equal(t, "1024x1024", ImageGenerationDetailFromContext(ctx).RequestParams["size"])
}

func TestImageDiagnosticCaptureBoundsAndStreaming(t *testing.T) {
	c := newAuthorizedImageCapture(7, "application/json", []byte(`{"contents":[{"parts":[{"text":"Gemini prompt"},{"inlineData":{"mimeType":"image/png","data":"hidden"}}]}]}`))
	require.Equal(t, "Gemini prompt", c.Snapshot().Prompt)
	large := []byte("data: {\"b64_json\":\"" + strings.Repeat("x", imageCaptureFrameBytes) + "\"}\n\n")
	c.ObserveResponse(large, true)
	response := []byte("data: {\"data\":[{\"url\":\"https://example.test/a.png\"}]}\n\n")
	for _, b := range response {
		c.ObserveResponse([]byte{b}, true)
	}
	detail := c.Snapshot()
	require.True(t, detail.Truncated)
	require.Len(t, detail.Images, 1)
	require.LessOrEqual(t, len(c.buffer), imageCaptureFrameBytes)
	encoded, ok := ImageGenerationDetailJSON(detail).(string)
	require.True(t, ok)
	require.LessOrEqual(t, len(encoded), imageDetailMaxBytes)
	for i := 0; i < 100; i++ {
		c.ObserveResponse([]byte(`{"data":[{"url":"https://example.test/a.png"}]}`), false)
	}
	require.LessOrEqual(t, len(c.Snapshot().Images), 16)
	largeRequest, _ := json.Marshal(map[string]any{"prompt": strings.Repeat("x", imageDetailStringBytes+1), "size": "1024x1024"})
	bounded := newAuthorizedImageCapture(7, "application/json", largeRequest).Snapshot()
	require.True(t, bounded.Truncated)
	require.Empty(t, bounded.Prompt)
}

func TestImageDiagnosticCaptureConcurrentSnapshots(t *testing.T) {
	c := newAuthorizedImageCapture(7, "application/json", []byte(`{"prompt":"cat"}`))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				c.ObserveResponse([]byte(`{"data":[{"url":"https://example.test/a.png"}]}`), false)
				require.NotNil(t, c.Snapshot())
			}
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, len(c.Snapshot().Images), 16)
}

func TestImageDiagnosticExpiredCacheDoesNotRetainEnabledPolicyOnReadFailure(t *testing.T) {
	repo := &imageDiagnosticRepo{values: map[string]string{}, err: errors.New("unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	svc.imageDiagnosticCache.Store(&cachedImageDiagnosticPolicy{users: map[int64]struct{}{7: {}}, expires: time.Now().Add(-time.Second)})
	require.False(t, svc.IsImageDiagnosticUserEnabled(context.Background(), 7))
}

func TestImageDiagnosticRawRequestSkipsLargeEscapedStrings(t *testing.T) {
	body := []byte(`{"ignored":"` + strings.Repeat(`\/`, 1024*1024) + `","image":"` + strings.Repeat(`\u0041`, 10000) + `","prompt":"a \"cat\"","n":2,"generationConfig":{"imageConfig":{"aspectRatio":"1:1"}}}`)
	c := newAuthorizedImageCapture(7, "application/json", body)
	detail := c.Snapshot()
	require.Equal(t, `a "cat"`, detail.Prompt)
	require.Equal(t, float64(2), detail.RequestParams["n"])
	require.NotContains(t, detail.RequestParams, "ignored")
	require.NotContains(t, detail.RequestParams, "image")
	require.True(t, detail.Truncated)
}

func TestImageDiagnosticMultipartMultipleImagesAndMask(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("prompt", "edit cat"))
	for _, name := range []string{"image[]", "image[0]", "image", "mask"} {
		part, err := w.CreateFormFile(name, "cat.png")
		require.NoError(t, err)
		_, err = part.Write([]byte("image-binary-secret"))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	detail := newAuthorizedImageCapture(7, w.FormDataContentType(), body.Bytes()).Snapshot()
	require.Equal(t, "edit cat", detail.Prompt)
	require.Len(t, detail.RequestParams["images"], 3)
	require.Len(t, detail.RequestParams["mask"], 1)
	images, ok := detail.RequestParams["images"].([]any)
	require.True(t, ok)
	file, ok := images[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cat.png", file["filename"])
	require.Equal(t, float64(len("image-binary-secret")), file["size"])
	require.NotContains(t, ImageGenerationDetailJSON(detail), "image-binary-secret")
}

func TestImageDiagnosticSharedStreamingBudgetAndMidframeSnapshot(t *testing.T) {
	c := newAuthorizedImageCapture(7, "application/json", []byte(`{"prompt":"cat"}`))
	c.ObserveResponseString("data: {\"ignored\":\""+strings.Repeat("x", 180*1024)+"\n", true)
	c.ObserveResponseString("data: "+strings.Repeat("x", 180*1024), true)
	require.LessOrEqual(t, cap(c.buffer)+cap(c.event), imageCaptureFrameBytes)
	require.True(t, c.Snapshot().Truncated)
	c.ObserveResponseString("\n\n", true)
	c.ObserveResponseString("data: {\"data\":[{\"url\":\"https://example.test/", true)
	require.Empty(t, c.Snapshot().Images)
	c.ObserveResponseString("cat.png\"}]}\n\n", true)
	require.Len(t, c.Snapshot().Images, 1)
	c.ObserveResponseString(strings.Repeat("x", imageCaptureTotalBytes), true)
	require.Empty(t, c.buffer)
	require.Empty(t, c.event)
}

func TestImageDiagnosticMultilineSSEUnderLimitRetainsLinks(t *testing.T) {
	c := newAuthorizedImageCapture(7, "application/json", []byte(`{"prompt":"cat"}`))
	for _, line := range []string{"data: {\n", "data: \"ignored\":\"" + strings.Repeat("x", 140*1024) + "\",\n", "data: \"n\":1,\n", "data: \"data\":[{\"url\":\"https://example.test/a.png\"}]}\n\n"} {
		c.ObserveResponseString(line, true)
		require.LessOrEqual(t, cap(c.event)+cap(c.buffer), imageCaptureFrameBytes)
	}
	require.Len(t, c.Snapshot().Images, 1)
	require.False(t, c.Snapshot().Truncated)
}

func TestImageDiagnosticMaskOnlyAcceptsURLOrFileMetadata(t *testing.T) {
	for _, mask := range []string{"iVBORw0KGgoAAAANSUhEUg==!", "data:image/png;base64,AAAA"} {
		body, err := json.Marshal(map[string]any{"prompt": "cat", "mask": mask})
		require.NoError(t, err)
		detail := newAuthorizedImageCapture(7, "application/json", body).Snapshot()
		require.NotContains(t, detail.RequestParams, "mask")
		detail.RequestParams["mask"] = mask
		require.NotContains(t, ImageGenerationDetailJSON(detail), mask)
	}
}

func TestImageDiagnosticPublicSettingsExcludeUserList(t *testing.T) {
	repo := &imageDiagnosticRepo{values: map[string]string{SettingKeyImageDiagnosticEnabled: "true", SettingKeyImageDiagnosticUserIDs: "[7]"}}
	svc := NewSettingService(repo, &config.Config{})
	settings, err := svc.GetPublicSettings(context.Background())
	require.NoError(t, err)
	injected, err := svc.GetPublicSettingsForInjection(context.Background())
	require.NoError(t, err)
	for _, value := range []any{settings, injected} {
		data, err := json.Marshal(value)
		require.NoError(t, err)
		require.NotContains(t, string(data), "image_diagnostic")
	}
}

type diagnosticSettlementRecorder struct {
	UsageSettlementRepository
	details []*ImageGenerationDetail
}

func (r *diagnosticSettlementRecorder) UpdateSettlementImageDetail(_ context.Context, _ UsageSettlementRef, detail *ImageGenerationDetail) error {
	r.details = append(r.details, detail)
	return nil
}

func TestImageDiagnosticDeferredQueueIsBoundedAndKeepsAuthorization(t *testing.T) {
	svc := NewDeferredService(nil, nil, time.Second)
	repo := &diagnosticSettlementRecorder{}
	detail := newAuthorizedImageCapture(7, "application/json", []byte(`{"prompt":"cat"}`)).Snapshot()
	for i := 0; i < 130; i++ {
		svc.ScheduleSettlementImageDetail(repo, UsageSettlementRef{RequestID: "diagnostic", APIKeyID: 1}, detail)
	}
	require.Len(t, svc.imageDetails, 128)
	require.Empty(t, repo.details, "queue overflow never falls back to synchronous DB I/O")
	detail.Prompt = "changed later"
	svc.flushSettlementImageDetails()
	require.Len(t, repo.details, 8)
	require.Len(t, svc.imageDetails, 120)
	for _, saved := range repo.details {
		require.Equal(t, "cat", saved.Prompt)
		require.Equal(t, int64(7), ImageGenerationDetailOwner(saved))
	}
}
