package handler

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBuildGeminiImageRequestIgnoresUnsupportedOptions(t *testing.T) {
	req := &service.OpenAIImagesRequest{
		Model:          "gemini-2.5-flash-image",
		Prompt:         "draw a cat",
		Size:           "1536x1024",
		Quality:        "high",
		Background:     "transparent",
		ResponseFormat: "url",
		Uploads: []service.OpenAIImagesUpload{{
			ContentType: "image/png",
			Data:        []byte("png"),
		}},
	}
	body, err := buildGeminiImageRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["quality"] != nil || got["background"] != nil || got["response_format"] != nil {
		t.Fatalf("unsupported options leaked into Gemini request: %s", body)
	}
	generationConfig, ok := got["generationConfig"].(map[string]any)
	if !ok {
		t.Fatalf("generationConfig has unexpected type: %T", got["generationConfig"])
	}
	imageConfig, ok := generationConfig["imageConfig"].(map[string]any)
	if !ok {
		t.Fatalf("imageConfig has unexpected type: %T", generationConfig["imageConfig"])
	}
	if aspectRatio := imageConfig["aspectRatio"]; aspectRatio != "3:2" {
		t.Fatalf("aspect ratio = %v, want 3:2", aspectRatio)
	}
	contents, ok := got["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents has unexpected value: %#v", got["contents"])
	}
	content, ok := contents[0].(map[string]any)
	if !ok {
		t.Fatalf("first content has unexpected type: %T", contents[0])
	}
	parts, ok := content["parts"].([]any)
	if !ok || len(parts) < 2 {
		t.Fatalf("parts has unexpected value: %#v", content["parts"])
	}
	part, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("second part has unexpected type: %T", parts[1])
	}
	inlineData, ok := part["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("inlineData has unexpected type: %T", part["inlineData"])
	}
	data := inlineData["data"]
	if data != base64.StdEncoding.EncodeToString([]byte("png")) {
		t.Fatalf("inline data was not encoded: %v", data)
	}
}

func TestGeminiImageItemsAcceptsCamelAndSnakeCase(t *testing.T) {
	body := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aGk="}},{"inline_data":{"mime_type":"image/jpeg","data":"aGk="}}]}}]}`)
	items, err := geminiImageItems(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0]["b64_json"] != "aGk=" || items[1]["b64_json"] != "aGk=" {
		t.Fatalf("unexpected image items: %#v", items)
	}
}
