package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestOpsErrorStorageOmitsImagesBeforeBodyBudget(t *testing.T) {
	body := `{"data":[{"b64_json":"` + strings.Repeat("a", 40000) + `"}],"message":"upstream failed","quality":"high","seed":9007199254740993}`
	clean, truncated := sanitizeErrorBodyForStorage(body, 1024)
	require.False(t, truncated)
	require.NotContains(t, clean, strings.Repeat("a", 64))
	require.Contains(t, clean, "upstream failed")
	require.Contains(t, clean, "quality")
	require.Contains(t, clean, `"seed":9007199254740993`)
	// Pre-serialized events must pass the same policy on the queue's second pass.
	events := `[{"message":"failure","upstream_response_body":"{\"b64_json\":\"encoded-image\"}","detail":"data:image/png;base64,aGVsbG8="}]`
	entry := &OpsInsertErrorLogInput{UpstreamErrorsJSON: &events}
	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotContains(t, *entry.UpstreamErrorsJSON, "encoded-image")
	require.NotContains(t, *entry.UpstreamErrorsJSON, "aGVsbG8=")
}

func TestTruncateForLogSanitizesBeforeTakingSnippet(t *testing.T) {
	body := `data: {"type":"image_generation_call","result":"` + strings.Repeat("a", 40000) + `","prompt":"cat","seed":9007199254740993}`
	clean := truncateForLog([]byte(body), 1024)
	require.NotContains(t, clean, strings.Repeat("a", 16))
	require.Contains(t, clean, "[IMAGE_DATA_OMITTED]")
	require.Contains(t, clean, `"prompt":"cat"`)
	require.Contains(t, clean, "9007199254740993")
	require.Contains(t, body, strings.Repeat("a", 40000))
}

func TestOpsErrorStorageOmitsRootImageString(t *testing.T) {
	clean, truncated := sanitizeErrorBodyForStorage(`"data:image/png;base64,aGVsbG8="`, 2048)
	require.False(t, truncated)
	require.Equal(t, `"[IMAGE_DATA_OMITTED]"`, clean)
}

func TestOpsSystemLogStorageOmitsSerializedImageBytes(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...)
	payload := struct {
		Image string `json:"b64_json"`
		Seed  int64  `json:"seed"`
	}{"encoded-image", 9007199254740993}
	var stored *OpsInsertSystemLogInput
	repo := &opsRepoMock{BatchInsertSystemLogsFn: func(_ context.Context, inputs []*OpsInsertSystemLogInput) (int64, error) {
		require.Len(t, inputs, 1)
		stored = inputs[0]
		return 1, nil
	}}
	sink := NewOpsSystemLogSink(repo)
	count, err := sink.flushBatch(context.Background(), []*logger.LogEvent{{Level: "warn", Message: "failed data:image/png;base64,aGVsbG8=", Fields: map[string]any{"raw_image": png, "params": payload, "user_id": int64(42)}}})
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NotContains(t, stored.Message, "aGVsbG8=")
	require.NotContains(t, stored.ExtraJSON, base64.StdEncoding.EncodeToString(png))
	require.NotContains(t, stored.ExtraJSON, "encoded-image")
	require.Contains(t, stored.ExtraJSON, `"seed":9007199254740993`)
	require.Equal(t, int64(42), *stored.UserID)
	require.Equal(t, "encoded-image", payload.Image)
}
