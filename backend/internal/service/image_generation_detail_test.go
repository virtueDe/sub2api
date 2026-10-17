package service

import (
	"bytes"
	"context"
	"mime/multipart"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageGenerationCapturePreservesRequestAndRedactsCredentials(t *testing.T) {
	capture := NewImageGenerationCapture("application/json", []byte(`{"prompt":"画一只猫","size":"1024x1024","n":2,"extra":{"api_key":"private"},"image_url":"data:image/png;base64,input"}`))
	detail := capture.Snapshot()
	require.Equal(t, "画一只猫", detail.Prompt)
	require.Equal(t, "1024x1024", detail.RequestParams["size"])
	require.Equal(t, float64(2), detail.RequestParams["n"])
	extra, ok := detail.RequestParams["extra"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", extra["api_key"])
	require.Equal(t, "[IMAGE_DATA_OMITTED]", detail.RequestParams["image_url"])
	require.Empty(t, detail.Images)
}

func TestImageGenerationCaptureMultipartRequest(t *testing.T) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("prompt", "edit this cat"))
	require.NoError(t, w.WriteField("size", "1024x1024"))
	file, err := w.CreateFormFile("image", "cat.png")
	require.NoError(t, err)
	_, err = file.Write([]byte("input image bytes"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	detail := NewImageGenerationCapture(w.FormDataContentType(), body.Bytes()).Snapshot()
	require.Equal(t, "edit this cat", detail.Prompt)
	files, ok := detail.RequestParams["image"].([]any)
	require.True(t, ok)
	require.Len(t, files, 1)
	fileInfo, ok := files[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "cat.png", fileInfo["filename"])
	require.EqualValues(t, 17, fileInfo["size"])
}

func TestImageGenerationCaptureResponseFormats(t *testing.T) {
	tests := []struct {
		name, response, image, mime string
		stream, partial             bool
	}{
		{"openai JSON", `{"data":[{"url":"https://example.com/cat.png","revised_prompt":"a cat"}]}`, "https://example.com/cat.png", "image/png", false, false},
		{"gemini JSON", `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"aW1hZ2U="}}]}}]}`, "aW1hZ2U=", "image/jpeg", false, false},
		{"responses final", "data: {\"type\":\"response.image_generation_call.completed\",\"result\":\"ZmluYWw=\"}\n\n", "ZmluYWw=", "image/png", true, false},
		{"multiline SSE", "data: {\"type\":\"image_generation.completed\",\ndata: \"b64_json\":\"ZmluYWw=\"}\n\n", "ZmluYWw=", "image/png", true, false},
		{"partial before error", "data: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"cGFydGlhbA==\"}\n\ndata: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n", "cGFydGlhbA==", "image/png", true, true},
		{"unterminated SSE", "data: {\"type\":\"response.image_generation_call.completed\",\"result\":\"ZmluYWw=\"}", "ZmluYWw=", "image/png", true, false},
		{"markdown", `{"output":[{"type":"message","content":[{"type":"output_text","text":"![cat](https://example.com/cat.png)"}]}]}`, "https://example.com/cat.png", "image/png", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			capture := NewImageGenerationCapture("application/json", []byte(`{"prompt":"cat"}`))
			// Deliberately split inside JSON and SSE lines, as the writer can do.
			for i := 0; i < len(tt.response); i += 7 {
				end := min(i+7, len(tt.response))
				capture.ObserveResponse([]byte(tt.response[i:end]), tt.stream)
			}
			detail := capture.Snapshot()
			require.Len(t, detail.Images, 1)
			image := detail.Images[0]
			require.Equal(t, tt.image, image.URL+image.B64JSON)
			require.Equal(t, tt.mime, image.MimeType)
			require.Equal(t, tt.partial, image.Partial)
		})
	}
}

func TestImageGenerationCaptureFinalReplacesPartialAndDeduplicates(t *testing.T) {
	capture := NewImageGenerationCapture("application/json", []byte(`{"prompt":"cat"}`))
	capture.ObserveResponse([]byte("data: {\"type\":\"response.image_generation_call.partial_image\",\"output_index\":0,\"partial_image_b64\":\"partial\"}\n\n"), true)
	first := capture.Snapshot()
	capture.ObserveResponse([]byte("data: {\"type\":\"response.image_generation_call.completed\",\"output_index\":0,\"result\":\"final\"}\n\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"final\"}}\n\n"), true)
	require.Equal(t, "partial", first.Images[0].B64JSON)
	final := capture.Snapshot()
	require.Len(t, final.Images, 1)
	require.Equal(t, "final", final.Images[0].B64JSON)
	require.False(t, final.Images[0].Partial)
}

func TestImageGenerationCaptureContext(t *testing.T) {
	capture := NewImageGenerationCapture("application/json", []byte(`{"contents":[{"parts":[{"text":"cat"}]}]}`))
	ctx := WithImageGenerationCapture(context.Background(), capture)
	require.Equal(t, "cat", ImageGenerationDetailFromContext(ctx).Prompt)
	require.Equal(t, capture.Snapshot(), ImageGenerationDetailFromContext(ctx))
}

func TestImageGenerationCapturePreservesIdenticalOutputs(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		capture := NewImageGenerationCapture("application/json", []byte(`{"prompt":"cat","n":2}`))
		response := `{"data":[{"b64_json":"same"},{"b64_json":"same"}]}`
		if streaming {
			response = "data: " + response + "\n\n"
		}
		capture.ObserveResponse([]byte(response), streaming)
		if streaming {
			// A cumulative SSE frame repeats the same two outputs.
			capture.ObserveResponse([]byte(response), true)
		}
		require.Len(t, capture.Snapshot().Images, 2)
	}
	// Separate Gemini native calls may return identical image bytes.
	capture := NewImageGenerationCapture("application/json", []byte(`{"prompt":"cat","n":2}`))
	response := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"same"}}]}}]}`)
	capture.ObserveResponse(response, false)
	capture.ObserveResponse(response, false)
	require.Len(t, capture.Snapshot().Images, 2)
}
