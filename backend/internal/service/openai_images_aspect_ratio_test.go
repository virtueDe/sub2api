package service

import (
	"encoding/json"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIImagesAspectRatio(t *testing.T) {
	tests := []struct {
		name, explicit, size, want string
	}{
		{name: "explicit wins", explicit: "2:3", size: "2048x1152", want: "2:3"},
		{name: "size reduces", size: "2048x1152", want: "16:9"},
		{name: "non standard exact", size: "1280x768", want: "5:3"},
		{name: "tier skipped", size: "2K", want: ""},
		{name: "invalid explicit does not fallback", explicit: "bad", size: "1024x1024", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, OpenAIImagesAspectRatio(tt.explicit, tt.size))
		})
	}
}

func TestBuildOpenAIImagesAspectRatioPrompt(t *testing.T) {
	require.Equal(t,
		"输出要求：竖向 2:3 构图，宽高比严格为 2:3，不要输出方形或其他比例。",
		buildOpenAIImagesAspectRatioPrompt("", "2:3"),
	)
	require.Equal(t, "portrait 宽高比严格为 2:3", buildOpenAIImagesAspectRatioPrompt("portrait 宽高比严格为 2:3", "2:3"))
}

func TestRewriteOpenAIImagesPromptJSON(t *testing.T) {
	body, contentType, err := rewriteOpenAIImagesPrompt([]byte(`{"model":"gpt-image-2","prompt":"hello"}`), "application/json", "hello\n\nratio")
	require.NoError(t, err)
	require.Equal(t, "application/json", contentType)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(body, &payload))
	require.Equal(t, "hello\n\nratio", payload["prompt"])
	require.Equal(t, "gpt-image-2", payload["model"])
}

func TestRewriteOpenAIImagesPromptMultipart(t *testing.T) {
	var body strings.Builder
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "hello"))
	require.NoError(t, writer.Close())
	rewritten, contentType, err := rewriteOpenAIImagesPrompt([]byte(body.String()), writer.FormDataContentType(), "hello\n\nratio")
	require.NoError(t, err)
	require.NotEmpty(t, rewritten)
	require.NotEmpty(t, contentType)

	parsed := &OpenAIImagesRequest{Endpoint: openAIImagesGenerationsEndpoint, ContentType: contentType, Multipart: true, N: 1}
	require.NoError(t, parseOpenAIImagesMultipartRequest(rewritten, contentType, parsed))
	require.Equal(t, "hello\n\nratio", parsed.Prompt)
}
