package imagepayload

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeInputNestedImagesPreservesBusinessData(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...))
	uri := "data:image/png;base64," + png
	url := "https://example.test/image?signature=" + strings.Repeat("a", 1024)
	params := map[string]any{
		"image":    []any{uri, png, url},
		"messages": []any{map[string]any{"content": []any{map[string]any{"image_url": map[string]any{"url": uri}}, map[string]any{"image_url": map[string]any{"url": png}}}}},
		"contents": []any{map[string]any{"parts": []any{map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": png}}}}},
		"prompt":   "参考 " + uri + "，画猫", "text": png,
		"data": strings.Repeat("普通业务文本", 256), "image_url": url,
		"seed": json.Number("9007199254740993"), "authorization": "secret",
	}
	SanitizeInput(params, true)
	require.Equal(t, []any{Omitted, Omitted, url}, params["image"])
	require.Equal(t, "参考 "+Omitted+"，画猫", params["prompt"])
	require.Equal(t, Omitted, params["text"], "a complete encoded image must not be retained under an arbitrary field")
	require.Equal(t, url, params["image_url"])
	require.Equal(t, strings.Repeat("普通业务文本", 256), params["data"])
	require.Equal(t, json.Number("9007199254740993"), params["seed"])
	require.Equal(t, "[REDACTED]", params["authorization"])
	for _, key := range []string{"messages", "contents"} {
		raw, err := json.Marshal(params[key])
		require.NoError(t, err)
		require.NotContains(t, string(raw), png)
		require.Contains(t, string(raw), Omitted)
	}
}

func TestCleanDetailRetentionAndPreservation(t *testing.T) {
	raw := []byte(`{"prompt":"cat","request_params":{"image":["data:image/png;base64,aGVsbG8="],"prompt":"cat","n":2,"seed":9007199254740993,"token_count":123},"images":[{"b64_json":"output","url":"https://example.test/a.png","mime_type":"image/png","revised_prompt":"a cat","partial":true,"extra":42}],"future":{"bill":9007199254740993}}`)
	for _, expired := range []bool{false, true} {
		cleaned, _, changed, err := CleanDetail(raw, expired)
		require.NoError(t, err)
		require.True(t, changed)
		var got map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(cleaned)))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&got))
		require.Equal(t, "cat", got["prompt"])
		params, ok := got["request_params"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, []any{Omitted}, params["image"])
		require.Equal(t, json.Number("9007199254740993"), params["seed"])
		future, ok := got["future"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, json.Number("9007199254740993"), future["bill"])
		images, ok := got["images"].([]any)
		require.True(t, ok)
		require.Len(t, images, 1)
		image, ok := images[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "https://example.test/a.png", image["url"])
		require.Equal(t, "a cat", image["revised_prompt"])
		require.Equal(t, true, image["partial"])
		require.Equal(t, json.Number("42"), image["extra"])
		if expired {
			require.NotContains(t, image, "b64_json")
		} else {
			require.Equal(t, "output", image["b64_json"])
		}
		twice, n, modified, err := CleanDetail(cleaned, expired)
		require.NoError(t, err)
		require.False(t, modified)
		require.Zero(t, n)
		require.Equal(t, cleaned, twice)
	}
}

func TestCleanDetailEmptyAndUnexpectedShapes(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"images":null}`, `{"images":[null,123,{"url":"https://example.test"}],"request_params":{"data":"long prose"}}`} {
		clean, _, changed, err := CleanDetail([]byte(raw), true)
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, raw, string(clean))
	}
	_, _, _, err := CleanDetail([]byte(`{`), true)
	require.Error(t, err)
}

func TestSanitizeWrappedAndDeclaredImages(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...))
	wrapped := png[:76] + "\r\n" + png[76:152] + "\n" + png[152:]
	svg := base64.StdEncoding.EncodeToString([]byte("<svg/>"))
	avif := base64.StdEncoding.EncodeToString([]byte("\x00\x00\x00\x18ftypavif\x00\x00\x00\x00avifmif1"))
	params := map[string]any{
		"image":      wrapped,
		"images":     []any{avif},
		"image_url":  "data:image/png;base64," + wrapped,
		"prompt":     "参考 data:image/png;base64," + png + "\ndraw a cat",
		"source":     map[string]any{"media_type": "image/svg+xml", "data": svg},
		"inlineData": map[string]any{"mimeType": "image/avif", "data": avif},
	}
	SanitizeInput(params, true)
	require.Equal(t, Omitted, params["image"])
	require.Equal(t, []any{Omitted}, params["images"])
	require.Equal(t, "参考 "+Omitted+"\ndraw a cat", params["prompt"])
	require.Equal(t, Omitted, params["image_url"])
	for _, key := range []string{"source", "inlineData"} {
		image, ok := params[key].(map[string]any)
		require.True(t, ok)
		require.Equal(t, Omitted, image["data"])
	}
}

func TestCleanDetailWrappedMIMEInputAndOutputURL(t *testing.T) {
	raw := []byte(`{"request_params":{"source":{"media_type":"image/png","data":"data:image/png;base64,iVBORw0KGgoA\nAAAA"}},"images":[{"url":"data:image/png;base64,iVBORw0KGgoA\nAAAA","mime_type":"image/png"}],"prompt":"data:image/png;base64,YQ==\ndraw a cat"}`)
	for _, expire := range []bool{false, true} {
		clean, _, changed, err := CleanDetail(raw, expire)
		require.NoError(t, err)
		require.True(t, changed)
		var detail map[string]any
		require.NoError(t, json.Unmarshal(clean, &detail))
		params, ok := detail["request_params"].(map[string]any)
		require.True(t, ok)
		source, ok := params["source"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, Omitted, source["data"])
		require.Equal(t, Omitted+"\ndraw a cat", detail["prompt"])
		images, ok := detail["images"].([]any)
		require.True(t, ok)
		require.Len(t, images, 1)
		output, ok := images[0].(map[string]any)
		require.True(t, ok)
		if expire {
			require.NotContains(t, output, "url")
			require.NotContains(t, string(clean), "AAAA")
		} else {
			require.Equal(t, "data:image/png;base64,iVBORw0KGgoA\nAAAA", output["url"])
		}
	}
}

func TestSanitizeJSONRemovesOutputImagesAndPreservesParameters(t *testing.T) {
	raw := []byte(`{"seed":9007199254740993,"quality":"high","url":"https://cdn.test/img.png","data":[{"b64_json":"invalid-but-image-payload","revised_prompt":"cat"}],"output":[{"type":"image_generation_call","result":"YWJj"}],"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="},"metadata":{"base64":"dGV4dA=="}}`)
	clean, err := SanitizeJSON(raw)
	require.NoError(t, err)
	for _, payload := range []string{"invalid-but-image-payload", "YWJj", "aW1hZ2U="} {
		require.NotContains(t, string(clean), payload)
	}
	for _, value := range []string{`"seed":9007199254740993`, `"quality":"high"`, `https://cdn.test/img.png`, `"revised_prompt":"cat"`, `dGV4dA==`} {
		require.Contains(t, string(clean), value)
	}
	require.Contains(t, string(raw), "invalid-but-image-payload")
	twice, err := SanitizeJSON(clean)
	require.NoError(t, err)
	require.Equal(t, clean, twice)
}

func TestSanitizeTextRemovesImagesBeforeTruncation(t *testing.T) {
	for _, text := range []string{
		`data: {"b64_json":"` + strings.Repeat("a", 2000) + `","quality":"high"}`,
		`broken {"b64_json":"` + strings.Repeat("a", 2000),
		`upstream failed on data:image/png;base64,` + strings.Repeat("a", 2000) + ` request`,
	} {
		clean := SanitizeText(text)
		require.NotContains(t, clean, strings.Repeat("a", 32))
		require.Contains(t, clean, Omitted)
	}
}

func TestSanitizeTextSSEAndNestedJSON(t *testing.T) {
	png := base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 256)...))
	wrappedURI := "data:image/png;base64," + png[:76] + "\n" + png[76:]
	body, err := json.Marshal(map[string]any{
		"type": "image_generation_call", "result": png,
		"inlineData":  map[string]any{"mimeType": "image/svg+xml", "data": "PHN2Zy8+"},
		"input_image": wrappedURI, "seed": json.Number("9007199254740993"),
		"prompt": "cat", "url": "https://cdn.test/cat.png",
	})
	require.NoError(t, err)
	nested, err := json.Marshal(map[string]any{"upstream_response_body": string(body), "status": 500})
	require.NoError(t, err)
	for _, input := range []string{
		"data: " + string(body) + "\n\ndata: [DONE]\n",
		"upstream failed: " + string(body) + " after retry",
		string(nested),
	} {
		clean := SanitizeText(input)
		for _, payload := range []string{png[:64], png[76:], "PHN2Zy8+"} {
			require.NotContains(t, clean, payload)
		}
		for _, retained := range []string{"cat", "https://cdn.test/cat.png", "9007199254740993"} {
			require.Contains(t, clean, retained)
		}
		require.Equal(t, clean, SanitizeText(clean))
	}
}

func TestSanitizeTextMalformedWrappedImagePreservesOtherFields(t *testing.T) {
	for _, body := range []string{
		"broken {\"b64_json\":\"" + strings.Repeat("a", 300) + "\n" + strings.Repeat("b", 300),
		"broken {\"b64_json\":\"" + strings.Repeat("a", 300) + "\n" + strings.Repeat("b", 300) + "\",\"prompt\":\"cat\",\"n\":2}",
	} {
		clean := SanitizeText(body)
		require.NotContains(t, clean, strings.Repeat("a", 16))
		require.NotContains(t, clean, strings.Repeat("b", 16))
		require.Contains(t, clean, Omitted)
		if strings.Contains(body, "cat") {
			require.Contains(t, clean, `"prompt":"cat","n":2`)
		}
	}
}

func TestSanitizeTextIncompleteImageContextAndNeighbouringParameters(t *testing.T) {
	for _, body := range []string{
		`{"type":"image_generation_call","result":"PHN2Zy8+`,
		`{"inlineData":{"mimeType":"image/svg+xml","data":"PHN2Zy8+`,
		`{"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgoAA`,
		`{"inlineData":{"data":"PHN2Zy8+","mimeType":"image/svg+xml"`,
	} {
		clean := SanitizeText(body)
		require.NotContains(t, clean, "PHN2Zy8+")
		require.NotContains(t, clean, "iVBORw0KGgoAA")
		require.Contains(t, clean, Omitted)
	}
	body := `broken {"image":{"mimeType":"image/png","data":"aW1hZ2U="},"meta":{"data":"dGV4dA=="},"prompt":"cat"}`
	clean := SanitizeText(body)
	require.NotContains(t, clean, "aW1hZ2U=")
	require.Contains(t, clean, "dGV4dA==")
	require.Contains(t, clean, `"prompt":"cat"`)
}

func TestSanitizeImageDoesNotSwallowPromptFollowingLine(t *testing.T) {
	raw := []byte(`{"prompt":"data:image/png;base64,aGVsbG8\ncat","image":"data:image/png;base64,aGVs\nbG8="}`)
	clean, err := SanitizeJSON(raw)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(clean, &got))
	require.Equal(t, Omitted+"\ncat", got["prompt"])
	require.Equal(t, Omitted, got["image"])
	require.Equal(t, Omitted+"\ncat", SanitizeField("prompt", "data:image/png;base64,aGVsbG8\ncat"))
	require.Equal(t, Omitted, SanitizeField("image", "data:image/png;base64,aGVs\nbG8="))
}
