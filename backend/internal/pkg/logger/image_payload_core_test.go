package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type imageRichError struct{}

func (imageRichError) Error() string { return "upstream failed" }
func (imageRichError) Format(s fmt.State, verb rune) {
	_, _ = fmt.Fprint(s, "upstream failed data:image/png;base64,aGVsbG8= stack: gateway.go:42")
}

func TestImagePayloadCoreFiltersContextAndFieldsWithoutChangingSources(t *testing.T) {
	var output bytes.Buffer
	core := imagePayloadCore{zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)}
	log := zap.New(core).With(zap.String("image_base64", "context-image"), zap.Int("user_id", 42))
	params := map[string]any{"b64_json": "output-image", "quality": "high", "url": "https://cdn.test/cat.png"}
	log.Info("request data:image/png;base64,aGVsbG8=", zap.Any("params", params), zap.String("body", `{"partial_image_b64":"partial-image","n":2}`))
	stored := output.String()
	for _, payload := range []string{"context-image", "output-image", "partial-image", "aGVsbG8="} {
		require.NotContains(t, stored, payload)
	}
	require.Contains(t, stored, `"user_id":42`)
	require.Contains(t, stored, `"quality":"high"`)
	require.Contains(t, stored, "https://cdn.test/cat.png")
	require.Equal(t, "output-image", params["b64_json"])
}

func TestImagePayloadCorePreservesRichErrorDiagnostics(t *testing.T) {
	var output bytes.Buffer
	core := imagePayloadCore{zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)}
	zap.New(core).Error("failed", zap.Error(imageRichError{}))
	var stored map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &stored))
	require.Equal(t, "upstream failed", stored["error"])
	require.Contains(t, stored["errorVerbose"], "stack: gateway.go:42")
	require.NotContains(t, output.String(), "aGVsbG8=")
}

func TestImagePayloadCoreInlineKeepsNumericParameters(t *testing.T) {
	var output bytes.Buffer
	core := imagePayloadCore{zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zap.DebugLevel)}
	zap.New(core).Info("image", zap.Inline(zapcore.ObjectMarshalerFunc(func(enc zapcore.ObjectEncoder) error {
		enc.AddString("b64_json", "encoded-image")
		enc.AddInt64("seed", 9007199254740993)
		return nil
	})))
	require.NotContains(t, output.String(), "encoded-image")
	require.Contains(t, output.String(), `"seed":9007199254740993`)
}
