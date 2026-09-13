package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// GeminiImages adapts the public OpenAI-compatible image contract to Gemini's
// native generateContent request. Account selection, failover, billing and
// auditing remain in GeminiV1BetaModels.
func (h *GatewayHandler) GeminiImages(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		geminiImageError(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	if !service.GroupAllowsImageGeneration(apiKey.Group) {
		geminiImageError(c, http.StatusForbidden, "permission_error", service.ImageGenerationPermissionMessage())
		return
	}

	body, err := readImageRequestBody(c)
	if err != nil {
		geminiImageError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if h.openAIGatewayService == nil {
		geminiImageError(c, http.StatusServiceUnavailable, "api_error", "image gateway is unavailable")
		return
	}
	parsed, err := h.openAIGatewayService.ParseGeminiImagesRequest(c, body)
	if err != nil {
		geminiImageError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if !parsed.ExplicitModel {
		parsed.Model = "gemini-2.5-flash-image"
	}
	parsed.Model = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(parsed.Model), "models/"), "Models/")
	if !service.IsGeminiImageGenerationModel(parsed.Model) {
		geminiImageError(c, http.StatusBadRequest, "invalid_request_error", "images endpoint requires a Gemini image model")
		return
	}
	if strings.TrimSpace(parsed.Prompt) == "" {
		geminiImageError(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}
	nativeBody, err := buildGeminiImageRequest(parsed)
	if err != nil {
		geminiImageError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	count := parsed.N
	if count <= 0 {
		count = 1
	}
	if count > 4 {
		count = 4
	}
	images := make([]map[string]string, 0, count)
	for i := 0; i < count; i++ {
		status, responseBody := h.forwardGeminiImage(c, parsed.Model, nativeBody)
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			// Gemini native errors are converted to the public image API envelope.
			geminiImageError(c, status, "api_error", extractGeminiImageError(responseBody))
			return
		}
		items, err := geminiImageItems(responseBody)
		if err != nil {
			geminiImageError(c, http.StatusBadGateway, "api_error", err.Error())
			return
		}
		images = append(images, items...)
	}
	imageResponse, _ := json.Marshal(map[string]any{"created": time.Now().Unix(), "data": images})
	c.Data(http.StatusOK, "application/json", imageResponse)
}

func readImageRequestBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return nil, fmt.Errorf("request body is empty")
	}
	body := make([]byte, 0, 4096)
	data, err := c.GetRawData()
	if err != nil {
		return nil, fmt.Errorf("failed to read request body")
	}
	body = append(body, data...)
	if len(body) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	return body, nil
}

func buildGeminiImageRequest(req *service.OpenAIImagesRequest) ([]byte, error) {
	if req == nil {
		return nil, fmt.Errorf("image request is required")
	}
	parts := []map[string]any{{"text": req.Prompt}}
	for _, upload := range req.Uploads {
		if len(upload.Data) == 0 {
			continue
		}
		mimeType := strings.TrimSpace(upload.ContentType)
		if mimeType == "" {
			mimeType = http.DetectContentType(upload.Data)
		}
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			return nil, fmt.Errorf("image upload must have an image MIME type")
		}
		parts = append(parts, map[string]any{"inlineData": map[string]string{
			"mimeType": mimeType,
			"data":     base64.StdEncoding.EncodeToString(upload.Data),
		}})
	}
	for _, raw := range req.InputImageURLs {
		inline, ok := geminiInlineDataURL(raw)
		if !ok {
			// Gemini cannot consume arbitrary HTTP URLs as inlineData. Ignore
			// unsupported optional references so mixed-client payloads remain
			// compatible; multipart uploads remain the reliable edit input.
			continue
		}
		parts = append(parts, map[string]any{"inlineData": inline})
	}
	config := map[string]any{"responseModalities": []string{"IMAGE"}}
	if ratio := geminiAspectRatio(req); ratio != "" {
		config["imageConfig"] = map[string]string{"aspectRatio": ratio}
	}
	payload := map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": config,
	}
	return json.Marshal(payload)
}

func geminiInlineDataURL(raw string) (map[string]string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return nil, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "data" {
		return nil, false
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return nil, false
	}
	header := raw[len("data:"):comma]
	if !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return nil, false
	}
	mimeType := strings.TrimSuffix(header, ";base64")
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return nil, false
	}
	data := strings.TrimSpace(raw[comma+1:])
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return nil, false
	}
	return map[string]string{"mimeType": mimeType, "data": data}, true
}

func geminiAspectRatio(req *service.OpenAIImagesRequest) string {
	ratio := strings.TrimSpace(req.AspectRatio)
	if ratio != "" {
		return ratio
	}
	size := strings.TrimSpace(req.Size)
	var width, height int
	if _, err := fmt.Sscanf(size, "%dx%d", &width, &height); err != nil || width <= 0 || height <= 0 {
		return ""
	}
	r := float64(width) / float64(height)
	switch {
	case r > 1.7:
		return "16:9"
	case r > 1.25:
		return "3:2"
	case r < 0.6:
		return "9:16"
	case r < 0.8:
		return "2:3"
	default:
		return "1:1"
	}
}

func (h *GatewayHandler) forwardGeminiImage(c *gin.Context, model string, body []byte) (int, []byte) {
	recorder := httptest.NewRecorder()
	child, _ := gin.CreateTestContext(recorder)
	child.Keys = c.Keys
	child.Request = c.Request.Clone(c.Request.Context())
	child.Request.Body = io.NopCloser(bytes.NewReader(body))
	child.Request.URL.Path = "/v1beta/models/" + model + ":generateContent"
	child.Params = gin.Params{{Key: "modelAction", Value: "/" + model + ":generateContent"}}
	child.Set(string(middleware2.ContextKeyForcePlatform), service.PlatformGemini)
	child.Request = child.Request.WithContext(context.WithValue(child.Request.Context(), ctxkey.ForcePlatform, service.PlatformGemini))
	h.GeminiV1BetaModels(child)
	return recorder.Code, recorder.Body.Bytes()
}

func geminiImageItems(body []byte) ([]map[string]string, error) {
	data := make([]map[string]string, 0)
	gjson.GetBytes(body, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
			inline := part.Get("inlineData")
			if !inline.Exists() {
				inline = part.Get("inline_data")
			}
			value := strings.TrimSpace(inline.Get("data").String())
			if value == "" {
				return true
			}
			data = append(data, map[string]string{"b64_json": value})
			return true
		})
		return true
	})
	if len(data) == 0 {
		return nil, fmt.Errorf("Gemini returned no image data")
	}
	return data, nil
}

func extractGeminiImageError(body []byte) string {
	for _, path := range []string{"error.message", "message"} {
		if message := strings.TrimSpace(gjson.GetBytes(body, path).String()); message != "" {
			return message
		}
	}
	return "Gemini image generation failed"
}

func geminiImageError(c *gin.Context, status int, errorType, message string) {
	c.JSON(status, gin.H{"error": gin.H{"type": errorType, "message": message}})
}
