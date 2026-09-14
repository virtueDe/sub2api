package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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
func rebuildOpenAIImagesRequestBody(parsed *OpenAIImagesRequest) ([]byte, error) {
	if parsed == nil {
		return nil, fmt.Errorf("parsed request is nil")
	}

	// 如果是 multipart 请求，需要特殊处理
	if parsed.Multipart {
		return rebuildOpenAIImagesMultipartRequestBody(parsed)
	}

	// JSON 格式请求
	body := make(map[string]interface{})

	// 基础字段
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

	// 图片 URL 字段
	if len(parsed.InputImageURLs) > 0 {
		body["image"] = parsed.InputImageURLs[0]
	}
	if parsed.MaskImageURL != "" {
		body["mask"] = parsed.MaskImageURL
	}

	return json.Marshal(body)
}

// rebuildOpenAIImagesMultipartRequestBody 重建 multipart 格式的 OpenAI Images 请求体
func rebuildOpenAIImagesMultipartRequestBody(parsed *OpenAIImagesRequest) ([]byte, error) {
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
		return nil, fmt.Errorf("finalize multipart body: %w", err)
	}

	return buffer.Bytes(), nil
}
