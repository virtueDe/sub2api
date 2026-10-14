package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ImageGenerationDetail is stored with its usage/error row, never in list responses.
type ImageGenerationDetail struct {
	Prompt        string           `json:"prompt"`
	RequestParams map[string]any   `json:"request_params"`
	Images        []GeneratedImage `json:"images"`
}

type GeneratedImage struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	Partial       bool   `json:"partial,omitempty"`
}

type imageGenerationContextKey struct{}
type imageGenerationDetailContextKey struct{}

var generatedImageMarkdown = regexp.MustCompile(`!\[[^\]]*\]\(([^\s)]+)\)`)

type generatedImageIdentity struct {
	index, url, base64 string
}

// ImageGenerationCapture is shared by detached billing contexts and Gemini child
// requests. Snapshots own their slices so asynchronous persistence cannot race writes.
type ImageGenerationCapture struct {
	mu      sync.Mutex
	detail  ImageGenerationDetail
	buffer  []byte
	sse     openAISSEDataAccumulator
	stream  bool
	partial map[string]GeneratedImage
	seen    map[generatedImageIdentity]struct{}
}

func WithImageGenerationCapture(ctx context.Context, capture *ImageGenerationCapture) context.Context {
	return context.WithValue(ctx, imageGenerationContextKey{}, capture)
}

func ImageGenerationCaptureFromContext(ctx context.Context) *ImageGenerationCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(imageGenerationContextKey{}).(*ImageGenerationCapture)
	return capture
}

func ImageGenerationDetailFromContext(ctx context.Context) *ImageGenerationDetail {
	if ctx != nil {
		if detail, ok := ctx.Value(imageGenerationDetailContextKey{}).(*ImageGenerationDetail); ok {
			return detail
		}
	}
	return ImageGenerationCaptureFromContext(ctx).Snapshot()
}

// WithImageGenerationDetail freezes a completed response before it is queued for billing.
func WithImageGenerationDetail(ctx context.Context, detail *ImageGenerationDetail) context.Context {
	return context.WithValue(ctx, imageGenerationDetailContextKey{}, detail)
}

func NewImageGenerationCapture(contentType string, body []byte) *ImageGenerationCapture {
	params := make(map[string]any)
	mediaType, options, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		reader := multipart.NewReader(bytes.NewReader(body), options["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			name := part.FormName()
			if part.FileName() != "" {
				size, _ := io.Copy(io.Discard, part)
				file := map[string]any{"filename": part.FileName(), "content_type": part.Header.Get("Content-Type"), "size": size}
				if existing, ok := params[name].([]any); ok {
					params[name] = append(existing, file)
				} else {
					params[name] = []any{file}
				}
			} else {
				value, _ := io.ReadAll(part)
				if previous, ok := params[name]; ok {
					params[name] = appendImageRequestValue(previous, string(value))
				} else {
					params[name] = string(value)
				}
			}
			_ = part.Close()
		}
	} else {
		_ = json.Unmarshal(body, &params)
	}
	if params == nil {
		params = map[string]any{}
	}
	// Request headers and credentials are deliberately excluded; redact embedded
	// credential fields too without changing the prompt or business parameters.
	redactImageRequestCredentials(params)
	prompt, _ := params["prompt"].(string)
	if prompt == "" {
		var texts []string
		collectImageRequestText(params, &texts)
		prompt = strings.Join(texts, "\n")
	}
	return &ImageGenerationCapture{
		detail:  ImageGenerationDetail{Prompt: prompt, RequestParams: params, Images: []GeneratedImage{}},
		partial: make(map[string]GeneratedImage),
		seen:    make(map[generatedImageIdentity]struct{}),
	}
}

func appendImageRequestValue(previous any, next string) []any {
	if values, ok := previous.([]any); ok {
		return append(values, next)
	}
	return []any{previous, next}
}

func redactImageRequestCredentials(value any) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			switch strings.ToLower(key) {
			case "api_key", "apikey", "authorization", "access_token", "refresh_token", "password", "secret":
				node[key] = "[REDACTED]"
			case "b64_json", "base64", "image_base64", "image", "mask", "image_url", "data":
				if text, ok := child.(string); ok && (strings.HasPrefix(strings.ToLower(text), "data:") || len(text) > 256) {
					node[key] = "[IMAGE_DATA_OMITTED]"
				} else {
					redactImageRequestCredentials(child)
				}
			default:
				redactImageRequestCredentials(child)
			}
		}
	case []any:
		for _, child := range node {
			redactImageRequestCredentials(child)
		}
	}
}

func collectImageRequestText(value any, texts *[]string) {
	switch node := value.(type) {
	case map[string]any:
		// Follow only user content, never tool definitions or generation settings.
		if text, ok := node["text"].(string); ok {
			*texts = append(*texts, text)
		}
		for _, key := range []string{"contents", "parts", "messages", "input", "content"} {
			if text, ok := node[key].(string); ok {
				*texts = append(*texts, text)
			} else {
				collectImageRequestText(node[key], texts)
			}
		}
	case []any:
		for _, child := range node {
			collectImageRequestText(child, texts)
		}
	}
}

// ObserveResponse consumes exactly the bytes written to the client. SSE payloads
// are parsed incrementally, including partial outputs before a terminal error.
func (c *ImageGenerationCapture) ObserveResponse(chunk []byte, streaming bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buffer = append(c.buffer, chunk...)
	c.stream = streaming
	if streaming {
		for {
			end := bytes.IndexByte(c.buffer, '\n')
			if end < 0 {
				break
			}
			c.sse.AddLine(string(c.buffer[:end]), c.observeJSON)
			c.buffer = c.buffer[end+1:]
		}
	} else if json.Valid(c.buffer) {
		c.observeJSON(c.buffer)
		c.buffer = nil
	}
}

func (c *ImageGenerationCapture) observeJSON(body []byte) {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return
	}
	c.collectImages(root, false, "0")
}

func (c *ImageGenerationCapture) collectImages(value any, partial bool, index string) {
	switch node := value.(type) {
	case map[string]any:
		typ, _ := node["type"].(string)
		partial = partial || strings.Contains(typ, "partial_image")
		if idx, ok := node["output_index"]; ok {
			encoded, _ := json.Marshal(idx)
			index = string(encoded)
		}
		image := GeneratedImage{Partial: partial}
		image.URL, _ = node["url"].(string)
		image.B64JSON, _ = node["b64_json"].(string)
		if image.B64JSON == "" {
			image.B64JSON, _ = node["partial_image_b64"].(string)
		}
		image.RevisedPrompt, _ = node["revised_prompt"].(string)
		image.MimeType, _ = node["mimeType"].(string)
		if image.MimeType == "" {
			image.MimeType, _ = node["mime_type"].(string)
		}
		if strings.HasPrefix(typ, "image_generation") || strings.HasPrefix(typ, "response.image_generation_call.") {
			if image.B64JSON == "" {
				image.B64JSON, _ = node["result"].(string)
			}
		}
		if strings.HasPrefix(image.MimeType, "image/") && image.B64JSON == "" {
			image.B64JSON, _ = node["data"].(string)
		}
		if strings.HasPrefix(image.URL, "data:image/") {
			if header, data, ok := strings.Cut(image.URL, ";base64,"); ok {
				image.MimeType = strings.TrimPrefix(header, "data:")
				image.B64JSON = data
				image.URL = ""
			}
		}
		if image.MimeType == "" {
			image.MimeType = "image/png"
		}
		if image.URL != "" || image.B64JSON != "" {
			if image.Partial {
				c.partial[index] = image
			} else {
				delete(c.partial, index)
				identity := generatedImageIdentity{index: index, url: image.URL, base64: image.B64JSON}
				_, duplicate := c.seen[identity]
				// SSE can repeat a completed output in later lifecycle frames. JSON
				// responses (including separate Gemini children) each contain new outputs.
				if !c.stream || !duplicate {
					c.detail.Images = append(c.detail.Images, image)
					if c.stream {
						c.seen[identity] = struct{}{}
					}
				}
			}
		}
		for key, child := range node {
			// Only output containers; a response may also echo input/reference images.
			switch key {
			case "data", "output", "response", "item", "candidates", "choices", "message", "delta", "content", "text", "parts", "inlineData", "inline_data", "images", "image_url":
				childIndex := index
				// Top-level output arrays use the same positions as output_index in
				// SSE events; nested arrays retain their parent position.
				if _, array := child.([]any); array && (key == "data" || key == "output" || key == "candidates" || key == "choices") {
					childIndex = ""
				}
				c.collectImages(child, partial, childIndex)
			}
		}
	case []any:
		for i, child := range node {
			encoded, _ := json.Marshal(i)
			childIndex := string(encoded)
			if index != "" {
				childIndex = index + "/" + childIndex
			}
			c.collectImages(child, partial, childIndex)
		}
	case string:
		for _, match := range generatedImageMarkdown.FindAllStringSubmatch(node, -1) {
			c.collectImages(map[string]any{"url": match[1]}, partial, index)
		}
	}
}

func (c *ImageGenerationCapture) Snapshot() *ImageGenerationDetail {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	detail := c.detail
	// A terminal SSE frame may arrive without its final blank line.
	if c.stream {
		acc := c.sse
		acc.lines = append([]string(nil), acc.lines...)
		if len(c.buffer) > 0 {
			acc.AddLine(string(c.buffer), c.observeJSON)
		}
		acc.Flush(c.observeJSON)
		detail = c.detail
	}
	detail.Images = append([]GeneratedImage{}, c.detail.Images...)
	indices := make([]string, 0, len(c.partial))
	for index := range c.partial {
		indices = append(indices, index)
	}
	sort.Strings(indices)
	for _, index := range indices {
		detail.Images = append(detail.Images, c.partial[index])
	}
	return &detail
}

func ImageGenerationDetailJSON(detail *ImageGenerationDetail) any {
	if detail == nil {
		return nil
	}
	data, _ := json.Marshal(detail)
	return string(data)
}

func ParseImageGenerationDetail(data string) (*ImageGenerationDetail, error) {
	if data == "" || data == "null" {
		return nil, nil
	}
	var detail ImageGenerationDetail
	if err := json.Unmarshal([]byte(data), &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}
