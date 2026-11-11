package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageGenerationCaptureKeepsBillingMarkerWithoutDetails(t *testing.T) {
	request := []byte(`{"prompt":"private prompt","image":"data:image/png;base64,input","size":"1024x1024"}`)
	response := []byte(`{"data":[{"b64_json":"output","url":"https://example.test/image.png"}]}`)
	originalRequest, originalResponse := string(request), string(response)
	for _, contentType := range []string{"application/json", "multipart/form-data; boundary=test"} {
		capture := NewImageGenerationCapture(contentType, request)
		require.NotNil(t, capture)
		for _, streaming := range []bool{false, true} {
			capture.ObserveResponse(response[:10], streaming)
			capture.ObserveResponse(response[10:], streaming)
		}
		ctx := WithImageGenerationCapture(context.Background(), capture)
		require.Same(t, capture, ImageGenerationCaptureFromContext(ctx))
		require.Nil(t, capture.Snapshot())
		require.Nil(t, ImageGenerationDetailFromContext(ctx))
		require.Equal(t, originalRequest, string(request))
		require.Equal(t, originalResponse, string(response))
	}
}

func TestImageGenerationPersistenceRejectsDirectDetails(t *testing.T) {
	detail := &ImageGenerationDetail{Prompt: "private", RequestParams: map[string]any{"size": "1024x1024"}, Images: []GeneratedImage{{URL: "https://example.test/image.png", B64JSON: "image"}}}
	require.Nil(t, ImageGenerationDetailJSON(detail))
	require.Nil(t, ImageGenerationDetailJSON(nil))
	require.Nil(t, ImageGenerationDetailFromContext(WithImageGenerationDetail(context.Background(), detail)))
	require.Equal(t, "private", detail.Prompt)
	require.Equal(t, "image", detail.Images[0].B64JSON)
}

func TestParseImageGenerationDetailRetainsHistoricalReadCompatibility(t *testing.T) {
	detail, err := ParseImageGenerationDetail(`{"prompt":"historic","request_params":{"size":"1024x1024"},"images":[{"url":"https://example.test/old.png","b64_json":"old"}]}`)
	require.NoError(t, err)
	require.Equal(t, "historic", detail.Prompt)
	require.Equal(t, "1024x1024", detail.RequestParams["size"])
	require.Equal(t, "old", detail.Images[0].B64JSON)
	for _, empty := range []string{"", "null"} {
		got, err := ParseImageGenerationDetail(empty)
		require.NoError(t, err)
		require.Nil(t, got)
	}
	_, err = ParseImageGenerationDetail("invalid")
	require.Error(t, err)
}
