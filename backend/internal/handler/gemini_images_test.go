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
	if got := got["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)["aspectRatio"]; got != "3:2" {
		t.Fatalf("aspect ratio = %v, want 3:2", got)
	}
	data := got["contents"].([]any)[0].(map[string]any)["parts"].([]any)[1].(map[string]any)["inlineData"].(map[string]any)["data"]
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
