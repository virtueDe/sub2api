package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagepayload"
	"github.com/tidwall/gjson"
)

const (
	imageDetailMaxBytes    = 64 * 1024
	imageCaptureFrameBytes = 256 * 1024
	imageCaptureTotalBytes = 2 * 1024 * 1024
	imageDetailStringBytes = 8192
)

// Only captures authorized at request admission can produce new stored details.
// ownerID is deliberately private and never restored from historical JSON.
type ImageGenerationDetail struct {
	Prompt        string           `json:"prompt"`
	RequestParams map[string]any   `json:"request_params"`
	Images        []GeneratedImage `json:"images"`
	MetadataOnly  bool             `json:"metadata_only,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
	ownerID       int64
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

// A disabled capture remains a billing marker with no request/response buffers.
type ImageGenerationCapture struct {
	mu       sync.Mutex
	detail   *ImageGenerationDetail
	buffer   []byte
	event    []byte
	dropping bool
	observed int
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
	if ctx == nil {
		return nil
	}
	if detail, ok := ctx.Value(imageGenerationDetailContextKey{}).(*ImageGenerationDetail); ok {
		return detail
	}
	return ImageGenerationCaptureFromContext(ctx).Snapshot()
}

// WithImageGenerationDetail freezes an authorized, bounded snapshot for billing.
func WithImageGenerationDetail(ctx context.Context, detail *ImageGenerationDetail) context.Context {
	return context.WithValue(ctx, imageGenerationDetailContextKey{}, cloneImageDetail(detail))
}

func NewImageGenerationCapture(_ string, _ []byte) *ImageGenerationCapture {
	return &ImageGenerationCapture{}
}

func (c *ImageGenerationCapture) Enabled() bool { return c != nil && c.detail != nil }

func newAuthorizedImageCapture(userID int64, contentType string, body []byte) *ImageGenerationCapture {
	c := &ImageGenerationCapture{detail: &ImageGenerationDetail{ownerID: userID, MetadataOnly: true, RequestParams: map[string]any{}, Images: []GeneratedImage{}}}
	mediaType, options, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		reader := multipart.NewReader(bytes.NewReader(body), options["boundary"])
		for i := 0; i < 64; i++ {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			name := part.FormName()
			if part.FileName() != "" {
				// The original request owns the bytes. Only bounded file metadata is copied.
				size, _ := io.Copy(io.Discard, part)
				if name == "image" || strings.HasPrefix(name, "image[") || name == "mask" {
					key := "images"
					if name == "mask" {
						key = "mask"
					}
					files, _ := c.detail.RequestParams[key].([]any)
					if len(files) < 16 {
						c.detail.RequestParams[key] = append(files, map[string]any{"filename": boundedImageText(part.FileName(), 256), "content_type": boundedImageText(part.Header.Get("Content-Type"), 128), "size": size})
					} else {
						c.detail.Truncated = true
					}
				}
			} else if imageDiagnosticField(name) {
				value, _ := io.ReadAll(io.LimitReader(part, imageDetailStringBytes+1))
				if len(value) > imageDetailStringBytes {
					c.detail.Truncated = true
				} else {
					c.detail.RequestParams[name] = imagepayload.SanitizeText(string(value))
				}
			}
			_ = part.Close()
			if i == 63 {
				c.detail.Truncated = true
			}
		}
	} else {
		body = bytes.TrimSpace(body)
		if len(body) == 0 || body[0] != '{' {
			return c
		}
		budget, nodes := 24*1024, 256
		// Only object requests are accepted. Traverse raw tokens before decoding so
		// even ignored, escaped multi-megabyte image strings cannot allocate copies.
		if value := c.requestValue(parseRawJSONView(body).Raw, "", 0, &budget, &nodes); value != nil {
			if params, ok := value.(map[string]any); ok {
				c.detail.RequestParams = params
			}
		}
	}
	if prompt, ok := c.detail.RequestParams["prompt"].(string); ok {
		c.detail.Prompt = prompt
	} else {
		var texts []string
		collectDiagnosticPrompt(c.detail.RequestParams, &texts)
		c.detail.Prompt = boundedImageText(strings.Join(texts, "\n"), imageDetailStringBytes)
	}
	return c
}

func imageDiagnosticField(key string) bool {
	switch key {
	case "model", "prompt", "negative_prompt", "negativePrompt", "n", "size", "quality", "style", "background", "response_format", "output_format", "output_compression", "moderation", "seed", "aspect_ratio", "aspectRatio", "imageSize", "sampleCount", "numberOfImages", "personGeneration", "enhancePrompt", "addWatermark", "generationConfig", "generation_config", "imageConfig", "image_config", "responseModalities", "response_modalities", "temperature", "topP", "topK", "maxOutputTokens", "contents", "parts", "messages", "input", "content", "text", "role", "type", "image", "images", "mask", "image_url", "url", "inlineData", "inline_data", "fileData", "file_data", "fileUri", "file_uri", "mimeType", "mime_type":
		return true
	}
	return false
}

func (c *ImageGenerationCapture) requestValue(raw string, key string, depth int, budget, nodes *int) any {
	*nodes--
	if depth > 10 || *nodes < 0 || *budget <= 0 || len(raw) == 0 {
		c.detail.Truncated = true
		return nil
	}
	if raw[0] == '{' {
		out := map[string]any{}
		walkImageDiagnosticJSON(raw, func(k, v string) bool {
			if imageDiagnosticField(k) {
				if value := c.requestValue(v, k, depth+1, budget, nodes); value != nil {
					out[strings.Clone(k)] = value
				}
			} else {
				*nodes--
			}
			c.detail.Truncated = c.detail.Truncated || *nodes <= 0 || *budget <= 0
			return *nodes > 0 && *budget > 0
		})
		return out
	}
	if raw[0] == '[' {
		out := []any{}
		count := 0
		walkImageDiagnosticJSON(raw, func(_, v string) bool {
			count++
			if count > 32 {
				c.detail.Truncated = true
				return false
			}
			if value := c.requestValue(v, key, depth+1, budget, nodes); value != nil {
				out = append(out, value)
			}
			return *nodes > 0 && *budget > 0
		})
		return out
	}
	if len(raw) > imageDetailStringBytes+2 {
		c.detail.Truncated = true
		return nil
	}
	node := gjson.Parse(raw)
	if node.Type == gjson.String {
		// Do not unescape/copy arbitrary image data or unbounded strings.
		if len(node.Raw) > imageDetailStringBytes || len(node.Str) > *budget {
			c.detail.Truncated = true
			return nil
		}
		value := node.Str
		switch key {
		case "image", "images", "mask", "image_url", "url", "fileUri", "file_uri":
			value = diagnosticImageURL(value)
		}
		if value == "" {
			return nil
		}
		value = imagepayload.SanitizeText(value)
		*budget -= len(value)
		return strings.Clone(value)
	}
	if node.Type == gjson.Number {
		return node.Num
	}
	if node.Type == gjson.True || node.Type == gjson.False {
		return node.Bool()
	}
	return nil
}

func collectDiagnosticPrompt(value any, texts *[]string) {
	switch v := value.(type) {
	case map[string]any:
		if text, ok := v["text"].(string); ok {
			*texts = append(*texts, text)
		}
		for _, k := range []string{"contents", "parts", "messages", "input", "content"} {
			if text, ok := v[k].(string); ok {
				*texts = append(*texts, text)
			} else {
				collectDiagnosticPrompt(v[k], texts)
			}
		}
	case []any:
		for _, child := range v {
			collectDiagnosticPrompt(child, texts)
		}
	}
}

func boundedImageText(s string, limit int) string {
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return strings.Clone(s)
}

func diagnosticImageURL(s string) string {
	if len(s) > 2048 {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return strings.Clone(s)
}

// ObserveResponse is bounded even for base64 JSON and SSE lines without a newline.
func (c *ImageGenerationCapture) ObserveResponse(chunk []byte, streaming bool) {
	if c == nil || c.detail == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observeResponseLocked(chunk, streaming)
}

// WriteString forwarding must not allocate a copy of a multi-megabyte image
// frame before applying the observer's budget.
func (c *ImageGenerationCapture) ObserveResponseString(data string, streaming bool) {
	if c == nil || c.detail == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) > imageCaptureTotalBytes-c.observed {
		c.observed = imageCaptureTotalBytes
		c.detail.Truncated = true
		c.buffer, c.event = nil, nil
		c.dropping = true
		return
	}
	for len(data) > 0 {
		n := min(len(data), 32*1024)
		c.observeResponseLocked([]byte(data[:n]), streaming)
		data = data[n:]
	}
}

// Grow a buffer without letting capacity exceed its share of the capture budget.
func appendImageCaptureBytes(dst, data []byte, limit int) []byte {
	if cap(dst) < len(dst)+len(data) {
		grown := make([]byte, len(dst), min(limit, max(len(dst)+len(data), max(cap(dst)*2, 1024))))
		copy(grown, dst)
		dst = grown
	}
	return append(dst, data...)
}

func (c *ImageGenerationCapture) observeResponseLocked(chunk []byte, streaming bool) {
	if c.observed >= imageCaptureTotalBytes {
		c.detail.Truncated = true
		return
	}
	if len(chunk) > imageCaptureTotalBytes-c.observed {
		c.observed = imageCaptureTotalBytes
		c.detail.Truncated = true
		c.buffer = nil
		c.event = nil
		c.dropping = true
		return
	}
	c.observed += len(chunk)
	if !streaming {
		if c.dropping {
			return
		}
		if len(c.buffer)+len(chunk) > imageCaptureFrameBytes {
			c.detail.Truncated = true
			c.buffer = nil
			c.dropping = true
			return
		}
		c.buffer = appendImageCaptureBytes(c.buffer, chunk, imageCaptureFrameBytes)
		if len(c.buffer) > 0 && (c.buffer[len(c.buffer)-1] == '}' || c.buffer[len(c.buffer)-1] == ']') && json.Valid(c.buffer) {
			c.observeJSON(c.buffer)
			c.buffer = nil
		}
		return
	}
	for len(chunk) > 0 {
		end := bytes.IndexByte(chunk, '\n')
		complete := end >= 0
		if !complete {
			end = len(chunk)
		}
		if !c.dropping {
			if cap(c.event)+len(c.buffer)+end > imageCaptureFrameBytes && len(c.event)+len(c.buffer)+end <= imageCaptureFrameBytes {
				// Spare capacity is not event data. Reclaim it before rejecting a
				// valid multi-line event that still fits the shared byte budget.
				compact := make([]byte, len(c.event))
				copy(compact, c.event)
				c.event = compact
			}
			if cap(c.event)+len(c.buffer)+end > imageCaptureFrameBytes {
				c.buffer = nil
				c.event = nil
				c.dropping = true
				c.detail.Truncated = true
			} else {
				c.buffer = appendImageCaptureBytes(c.buffer, chunk[:end], imageCaptureFrameBytes-cap(c.event))
			}
		}
		if !complete {
			break
		}
		if c.dropping {
			if end == 0 || (end == 1 && chunk[0] == '\r') {
				c.dropping = false
			}
		} else {
			line := c.buffer
			c.buffer = nil
			c.observeLine(line)
		}
		c.buffer = nil
		chunk = chunk[end+1:]
	}
}

func (c *ImageGenerationCapture) observeLine(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		c.observeJSON(c.event)
		c.event = nil
		return
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimSpace(line[5:])
	if len(c.event)+len(data)+1 > imageCaptureFrameBytes {
		c.event = nil
		c.dropping = true
		c.detail.Truncated = true
		return
	}
	// Event growth leaves little spare capacity because the next line needs
	// the same shared budget. Keep the newline and data in one allocation.
	extra := len(data)
	if len(c.event) > 0 {
		extra++
	}
	if cap(c.event) < len(c.event)+extra {
		grown := make([]byte, len(c.event), min(imageCaptureFrameBytes, len(c.event)+extra+1024))
		copy(grown, c.event)
		c.event = grown
	}
	if len(c.event) > 0 {
		c.event = append(c.event, '\n')
	}
	c.event = append(c.event, data...)
	if json.Valid(c.event) {
		c.observeJSON(c.event)
		c.event = nil
	}
}

func (c *ImageGenerationCapture) observeJSON(body []byte) {
	if len(body) == 0 || !json.Valid(body) {
		return
	}
	nodes := 256
	c.collectImages(parseRawJSONView(body), 0, &nodes)
}

func (c *ImageGenerationCapture) collectImages(node gjson.Result, depth int, nodes *int) {
	*nodes--
	if depth > 12 || *nodes < 0 || len(c.detail.Images) >= 16 {
		c.detail.Truncated = true
		return
	}
	if node.IsArray() {
		node.ForEach(func(_, v gjson.Result) bool {
			c.collectImages(v, depth+1, nodes)
			return *nodes > 0 && len(c.detail.Images) < 16
		})
		return
	}
	if !node.IsObject() {
		return
	}
	image := GeneratedImage{MimeType: "image/png"}
	for _, k := range []string{"url", "fileUri", "file_uri"} {
		value := node.Get(k)
		if len(value.Raw) <= 2050 {
			image.URL = diagnosticImageURL(value.Str)
			if image.URL != "" {
				break
			}
		}
	}
	for _, k := range []string{"mimeType", "mime_type"} {
		value := node.Get(k)
		if len(value.Str) < 128 && strings.HasPrefix(value.Str, "image/") {
			image.MimeType = strings.Clone(value.Str)
		}
	}
	typ := node.Get("type").Str
	image.Partial = strings.Contains(typ, "partial_image")
	hasBinary := node.Get("b64_json").Exists() || node.Get("partial_image_b64").Exists() || ((strings.Contains(typ, "image_generation")) && node.Get("result").Exists()) || (strings.HasPrefix(node.Get("mimeType").Str, "image/") && node.Get("data").Type == gjson.String)
	if image.URL != "" || hasBinary {
		image.RevisedPrompt = imagepayload.SanitizeText(boundedImageText(node.Get("revised_prompt").Str, 2048))
		c.detail.Images = append(c.detail.Images, image)
	}
	node.ForEach(func(k, v gjson.Result) bool {
		switch k.Str {
		case "data", "output", "response", "item", "candidates", "choices", "message", "delta", "content", "parts", "inlineData", "inline_data", "fileData", "file_data", "images", "image_url":
			c.collectImages(v, depth+1, nodes)
		}
		return *nodes > 0 && len(c.detail.Images) < 16
	})
}

func (c *ImageGenerationCapture) FinishResponse() {
	if c == nil || c.detail == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dropping {
		if bytes.HasPrefix(c.buffer, []byte("data:")) {
			c.observeLine(c.buffer)
		} else {
			c.observeJSON(c.buffer)
		}
		c.observeJSON(c.event)
	}
	c.buffer = nil
	c.event = nil
	c.dropping = false
}

func (c *ImageGenerationCapture) Snapshot() *ImageGenerationDetail {
	if c == nil || c.detail == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dropping && len(c.buffer) > 0 {
		data := c.buffer
		if bytes.HasPrefix(c.buffer, []byte("data:")) {
			data = bytes.TrimSpace(c.buffer[5:])
		}
		// A billing snapshot can occur while a streaming frame is incomplete.
		// Leave it intact so subsequent chunks are not split into separate events.
		if json.Valid(data) && len(c.event) == 0 {
			c.observeJSON(data)
			c.buffer = nil
		}
	}
	return cloneImageDetail(c.detail)
}

func cloneImageDetail(detail *ImageGenerationDetail) *ImageGenerationDetail {
	encoded, ok := ImageGenerationDetailJSON(detail).(string)
	if !ok {
		return nil
	}
	copy, err := ParseImageGenerationDetail(encoded)
	if err != nil {
		return nil
	}
	copy.ownerID = detail.ownerID
	return copy
}

// ImageGenerationDetailJSON is the shared persistence boundary for usage and Ops.
// Untrusted/manual/historical details cannot opt themselves into new persistence.
func ImageGenerationDetailJSON(detail *ImageGenerationDetail) any {
	if detail == nil || detail.ownerID <= 0 {
		return nil
	}
	copy := *detail
	copy.MetadataOnly = true
	copy.Truncated = copy.Truncated || len(copy.Prompt) > imageDetailStringBytes || len(detail.Images) > 16
	copy.Prompt = imagepayload.SanitizeText(boundedImageText(copy.Prompt, imageDetailStringBytes))
	budget, nodes := 24*1024, 256
	copy.RequestParams, _ = boundedDiagnosticValue(detail.RequestParams, "", 0, &budget, &nodes, &copy.Truncated).(map[string]any)
	if copy.RequestParams == nil {
		copy.RequestParams = map[string]any{}
	}
	copy.Images = append([]GeneratedImage{}, detail.Images[:min(len(detail.Images), 16)]...)
	for i := range copy.Images {
		copy.Truncated = copy.Truncated || len(copy.Images[i].RevisedPrompt) > 2048 || len(copy.Images[i].MimeType) > 128
		copy.Images[i].B64JSON = ""
		copy.Images[i].URL = diagnosticImageURL(copy.Images[i].URL)
		copy.Images[i].MimeType = boundedImageText(copy.Images[i].MimeType, 128)
		copy.Images[i].RevisedPrompt = imagepayload.SanitizeText(boundedImageText(copy.Images[i].RevisedPrompt, 2048))
	}
	data, err := json.Marshal(copy)
	if err != nil {
		return nil
	}
	if len(data) > imageDetailMaxBytes {
		copy.RequestParams = map[string]any{}
		copy.Truncated = true
		data, err = json.Marshal(copy)
	}
	for err == nil && len(data) > imageDetailMaxBytes && len(copy.Images) > 0 {
		copy.Images = copy.Images[:len(copy.Images)-1]
		copy.Truncated = true
		data, err = json.Marshal(copy)
	}
	if err != nil || len(data) > imageDetailMaxBytes {
		return nil
	}
	return string(data)
}

// Reapply the small allowlist at persistence, even if a caller changes a snapshot.
func boundedDiagnosticValue(value any, key string, depth int, budget, nodes *int, truncated *bool) any {
	*nodes--
	if depth > 10 || *nodes < 0 || *budget <= 0 {
		*truncated = true
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, child := range v {
			if imageDiagnosticField(k) || k == "filename" || k == "content_type" {
				if clean := boundedDiagnosticValue(child, k, depth+1, budget, nodes, truncated); clean != nil {
					out[k] = clean
				}
			}
			if *nodes <= 0 || *budget <= 0 {
				break
			}
		}
		return out
	case []any:
		*truncated = *truncated || len(v) > 32
		out := []any{}
		for _, child := range v[:min(len(v), 32)] {
			if clean := boundedDiagnosticValue(child, key, depth+1, budget, nodes, truncated); clean != nil {
				out = append(out, clean)
			}
			if *nodes <= 0 || *budget <= 0 {
				break
			}
		}
		return out
	case string:
		if len(v) > imageDetailStringBytes || len(v) > *budget {
			*truncated = true
			return nil
		}
		switch key {
		case "image", "images", "mask", "image_url", "url", "fileUri", "file_uri":
			v = diagnosticImageURL(v)
		}
		*budget -= len(v)
		return imagepayload.SanitizeText(v)
	case float64, int, int64, bool, json.Number:
		return v
	}
	return nil
}

func ImageGenerationDetailJSONForUser(detail *ImageGenerationDetail, userID int64) any {
	if detail == nil || userID <= 0 || detail.ownerID != userID {
		return nil
	}
	return ImageGenerationDetailJSON(detail)
}

func ImageGenerationDetailOwner(detail *ImageGenerationDetail) int64 {
	if detail == nil {
		return 0
	}
	return detail.ownerID
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
