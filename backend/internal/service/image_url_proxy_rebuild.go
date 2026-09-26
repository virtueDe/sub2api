package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func stripImageURLQuery(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func stripImageURLQueries(urls []string) []string {
	result := make([]string, len(urls))
	for i, raw := range urls {
		result[i] = stripImageURLQuery(raw)
	}
	return result
}

// redactImageURLForLog keeps the URL shape while hiding signed query values.
func redactImageURLForLog(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		if comma := strings.Index(trimmed, ","); comma >= 0 {
			return trimmed[:comma+1] + "<redacted>"
		}
		return "data:<redacted>"
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "<invalid_url>"
	}
	if parsed.RawQuery == "" {
		return parsed.String()
	}

	keys := make([]string, 0, len(parsed.Query()))
	for key := range parsed.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	query := make(url.Values, len(keys))
	for _, key := range keys {
		query.Set(key, "<redacted>")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func redactImageURLsForLog(urls []string) []string {
	result := make([]string, len(urls))
	for i, raw := range urls {
		result[i] = redactImageURLForLog(raw)
	}
	return result
}

// rebuildGrokMediaRequestBody 重建 Grok 媒体请求体（应用代理后的 URL）
func rebuildGrokMediaRequestBody(info *GrokMediaRequestInfo, contentType string, originalBody []byte) ([]byte, string, error) {
	if info == nil {
		return originalBody, contentType, nil
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		// multipart 请求体重建
		return rebuildGrokMediaMultipartBody(info, contentType, originalBody)
	}

	// JSON 请求体重建
	return rebuildGrokMediaJSONBody(info, originalBody)
}

// rebuildGrokMediaJSONBody 重建 JSON 格式的 Grok 媒体请求体
func rebuildGrokMediaJSONBody(info *GrokMediaRequestInfo, originalBody []byte) ([]byte, string, error) {
	newBody := originalBody

	// 替换 image 字段（单个 URL）
	if len(info.InputImageURLs) > 0 && gjson.GetBytes(originalBody, "image").Exists() {
		newBody, _ = sjson.SetBytes(newBody, "image", info.InputImageURLs[0])
	}

	// 替换 mask 字段
	if info.MaskImageURL != "" && gjson.GetBytes(originalBody, "mask").Exists() {
		newBody, _ = sjson.SetBytes(newBody, "mask", info.MaskImageURL)
	}

	// 替换 image_url 字段
	if len(info.InputImageURLs) > 0 && gjson.GetBytes(originalBody, "image_url").Exists() {
		newBody, _ = sjson.SetBytes(newBody, "image_url", info.InputImageURLs[0])
	}

	return newBody, "application/json", nil
}

// rebuildGrokMediaMultipartBody 重建 multipart 格式的 Grok 媒体请求体
func rebuildGrokMediaMultipartBody(info *GrokMediaRequestInfo, contentType string, originalBody []byte) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("parse multipart content-type: %w", err)
	}

	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is required")
	}

	reader := multipart.NewReader(bytes.NewReader(originalBody), boundary)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)

	inputImageIndex := 0

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("read multipart part: %w", err)
		}

		formName := strings.TrimSpace(part.FormName())
		partHeader := cloneMultipartHeader(part.Header)

		// 判断是否需要替换 URL
		shouldReplace := false
		var replacementURL string

		if formName == "image" && part.FileName() == "" && len(info.InputImageURLs) > inputImageIndex {
			shouldReplace = true
			replacementURL = info.InputImageURLs[inputImageIndex]
			inputImageIndex++
		} else if formName == "mask" && part.FileName() == "" && info.MaskImageURL != "" {
			shouldReplace = true
			replacementURL = info.MaskImageURL
		} else if formName == "image_url" && part.FileName() == "" && len(info.InputImageURLs) > 0 {
			shouldReplace = true
			replacementURL = info.InputImageURLs[0]
		}

		target, err := writer.CreatePart(partHeader)
		if err != nil {
			_ = part.Close()
			return nil, "", fmt.Errorf("create multipart part: %w", err)
		}

		if shouldReplace {
			// 写入替换后的 URL
			if _, err := target.Write([]byte(replacementURL)); err != nil {
				_ = part.Close()
				return nil, "", fmt.Errorf("write replacement url: %w", err)
			}
		} else {
			// 原样复制
			if _, err := io.Copy(target, part); err != nil {
				_ = part.Close()
				return nil, "", fmt.Errorf("copy multipart part: %w", err)
			}
		}

		_ = part.Close()
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("finalize multipart body: %w", err)
	}

	newContentType := writer.FormDataContentType()
	return buffer.Bytes(), newContentType, nil
}

// rebuildOpenAIImagesRequestBody 重建 OpenAI Images 请求体（应用代理后的 URL）
func shouldProxyOpenAIImageURLs(parsed *OpenAIImagesRequest) bool {
	if parsed == nil || (len(parsed.InputImageURLs) == 0 && strings.TrimSpace(parsed.MaskImageURL) == "") {
		return false
	}
	// Rebuilding a multipart request with file parts would drop the in-memory
	// uploads. Keep the original body for native file uploads.
	if parsed.Multipart && (len(parsed.Uploads) > 0 || parsed.MaskUpload != nil) {
		return false
	}
	return true
}

func rebuildOpenAIImagesRequestBody(parsed *OpenAIImagesRequest, originalBodies ...[]byte) ([]byte, string, error) {
	if parsed == nil {
		return nil, "", fmt.Errorf("parsed request is nil")
	}

	// 如果是 multipart 请求，需要特殊处理
	if parsed.Multipart {
		return rebuildOpenAIImagesMultipartRequestBody(parsed)
	}

	// JSON 格式请求。优先在原始 JSON 上做局部修改，避免代理重建时
	// 丢失未知字段，尤其是 OpenAI edits 所需的 images[].image_url 结构。
	source := parsed.Body
	if len(originalBodies) > 0 {
		source = originalBodies[0]
	}
	if len(source) > 0 && json.Valid(source) {
		var payload map[string]any
		if err := json.Unmarshal(source, &payload); err == nil {
			if len(parsed.InputImageURLs) > 0 {
				images := make([]any, 0, len(parsed.InputImageURLs))
				if existing, ok := payload["images"].([]any); ok {
					images = existing
				}
				replacementIndex := 0
				for _, rawItem := range images {
					item, ok := rawItem.(map[string]any)
					if !ok {
						continue
					}
					originalURL, ok := item["image_url"].(string)
					if !ok || strings.TrimSpace(originalURL) == "" {
						continue
					}
					if replacementIndex >= len(parsed.InputImageURLs) {
						break
					}
					item["image_url"] = parsed.InputImageURLs[replacementIndex]
					replacementIndex++
				}
				for ; replacementIndex < len(parsed.InputImageURLs); replacementIndex++ {
					imageURL := parsed.InputImageURLs[replacementIndex]
					images = append(images, map[string]any{"image_url": imageURL})
				}
				payload["images"] = images
			}
			if parsed.MaskImageURL != "" {
				mask, _ := payload["mask"].(map[string]any)
				if mask == nil {
					mask = make(map[string]any)
				}
				mask["image_url"] = parsed.MaskImageURL
				payload["mask"] = mask
			}
			rebuilt, err := json.Marshal(payload)
			return rebuilt, "application/json", err
		}
	}

	// 没有可复用的原始 JSON 时保留一个最小兼容请求体。
	body := make(map[string]any)
	if parsed.Model != "" {
		body["model"] = parsed.Model
	}
	if parsed.Prompt != "" {
		body["prompt"] = parsed.Prompt
	}
	if parsed.N > 0 {
		body["n"] = parsed.N
	}
	if parsed.Size != "" {
		body["size"] = parsed.Size
	}
	if parsed.Quality != "" {
		body["quality"] = parsed.Quality
	}
	if parsed.Style != "" {
		body["style"] = parsed.Style
	}
	if parsed.ResponseFormat != "" {
		body["response_format"] = parsed.ResponseFormat
	}
	if len(parsed.InputImageURLs) > 0 {
		images := make([]map[string]string, 0, len(parsed.InputImageURLs))
		for _, imageURL := range parsed.InputImageURLs {
			images = append(images, map[string]string{"image_url": imageURL})
		}
		body["images"] = images
	}
	if parsed.MaskImageURL != "" {
		body["mask"] = map[string]string{"image_url": parsed.MaskImageURL}
	}

	rebuilt, err := json.Marshal(body)
	return rebuilt, "application/json", err
}

// rebuildOpenAIImagesMultipartRequestBody 重建 multipart 格式的 OpenAI Images 请求体
func rebuildOpenAIImagesMultipartRequestBody(parsed *OpenAIImagesRequest) ([]byte, string, error) {
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)

	// 写入基础字段
	if parsed.Model != "" {
		_ = writer.WriteField("model", parsed.Model)
	}
	if parsed.Prompt != "" {
		_ = writer.WriteField("prompt", parsed.Prompt)
	}
	if parsed.N > 0 {
		_ = writer.WriteField("n", fmt.Sprintf("%d", parsed.N))
	}
	if parsed.Size != "" {
		_ = writer.WriteField("size", parsed.Size)
	}
	if parsed.Quality != "" {
		_ = writer.WriteField("quality", parsed.Quality)
	}
	if parsed.Style != "" {
		_ = writer.WriteField("style", parsed.Style)
	}
	if parsed.ResponseFormat != "" {
		_ = writer.WriteField("response_format", parsed.ResponseFormat)
	}

	// 写入图片 URL
	if len(parsed.InputImageURLs) > 0 {
		_ = writer.WriteField("image", parsed.InputImageURLs[0])
	}
	if parsed.MaskImageURL != "" {
		_ = writer.WriteField("mask", parsed.MaskImageURL)
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("finalize multipart body: %w", err)
	}

	return buffer.Bytes(), writer.FormDataContentType(), nil
}
