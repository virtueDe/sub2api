package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	openAIImagesAspectRatioPromptEnabledContextKey = "openai_images_aspect_ratio_prompt_enabled"
	openAIImagesAspectRatioContextKey              = "openai_images_aspect_ratio"
)

// OpenAIImagesAspectRatio returns the canonical positive ratio from an explicit
// ratio or a WIDTHxHEIGHT size. It deliberately keeps non-standard ratios exact.
func OpenAIImagesAspectRatio(explicitRatio, size string) string {
	if strings.TrimSpace(explicitRatio) != "" {
		return normalizeOpenAIImagesRatio(explicitRatio, ':')
	}
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return ""
	}
	width, errW := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	height, errH := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return ""
	}
	return reduceOpenAIImagesRatio(width, height)
}

// PrepareOpenAIImagesAspectRatioPrompt applies the configured compatibility
// hint once to an OpenAI images request. The original size and model remain
// unchanged for billing and routing.
func (s *OpenAIGatewayService) PrepareOpenAIImagesAspectRatioPrompt(ctx context.Context, c *gin.Context, groupID int64, body []byte, parsed *OpenAIImagesRequest) ([]byte, error) {
	if s == nil || parsed == nil || s.settingService == nil || !s.settingService.IsOpenAIImagesAspectRatioPromptEnabled(ctx, groupID) {
		return body, nil
	}
	ratio := OpenAIImagesAspectRatio(parsed.AspectRatio, parsed.Size)
	if ratio == "" {
		return body, nil
	}
	effectivePrompt := buildOpenAIImagesAspectRatioPrompt(parsed.Prompt, ratio)
	if effectivePrompt == parsed.Prompt {
		return body, nil
	}
	parsed.Prompt = effectivePrompt
	if c != nil {
		c.Set(openAIImagesAspectRatioPromptEnabledContextKey, true)
		c.Set(openAIImagesAspectRatioContextKey, ratio)
	}
	if len(body) == 0 {
		return body, nil
	}
	rewritten, rewrittenContentType, err := rewriteOpenAIImagesPrompt(body, parsed.ContentType, effectivePrompt)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rewrittenContentType) != "" {
		parsed.ContentType = rewrittenContentType
	}
	return rewritten, nil
}

func normalizeOpenAIImagesRatio(value string, separator rune) string {
	parts := strings.Split(strings.TrimSpace(value), string(separator))
	if len(parts) != 2 {
		return ""
	}
	left, errL := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	right, errR := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
	if errL != nil || errR != nil || left <= 0 || right <= 0 {
		return ""
	}
	return reduceOpenAIImagesRatio(left, right)
}

func reduceOpenAIImagesRatio(left, right int64) string {
	if left <= 0 || right <= 0 {
		return ""
	}
	a, b := left, right
	for b != 0 {
		a, b = b, a%b
	}
	return fmt.Sprintf("%d:%d", left/a, right/a)
}

func buildOpenAIImagesAspectRatioPrompt(prompt, ratio string) string {
	ratio = strings.TrimSpace(ratio)
	if ratio == "" || strings.Contains(prompt, "宽高比严格为 "+ratio) {
		return prompt
	}
	orientation := "横向"
	parts := strings.Split(ratio, ":")
	if len(parts) == 2 {
		left, _ := strconv.ParseInt(parts[0], 10, 64)
		right, _ := strconv.ParseInt(parts[1], 10, 64)
		switch {
		case left == right:
			orientation = "方形"
		case left < right:
			orientation = "竖向"
		}
	}
	suffix := fmt.Sprintf("输出要求：%s %s 构图，宽高比严格为 %s，不要输出方形或其他比例。", orientation, ratio, ratio)
	if strings.TrimSpace(prompt) == "" {
		return suffix
	}
	return strings.TrimSpace(prompt) + "\n\n" + suffix
}

func rewriteOpenAIImagesPrompt(body []byte, contentType, prompt string) ([]byte, string, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && strings.EqualFold(mediaType, "multipart/form-data") {
		return rewriteOpenAIImagesMultipartPrompt(body, contentType, prompt)
	}
	rewritten, err := setJSONFieldString(body, "prompt", prompt)
	if err != nil {
		return nil, "", fmt.Errorf("rewrite image request prompt: %w", err)
	}
	return rewritten, contentType, nil
}

func setJSONFieldString(body []byte, path, value string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	payload[path] = value
	// aspect_ratio is a gateway compatibility field; selected upstreams only
	// understand the prompt hint and may reject unknown request properties.
	delete(payload, "aspect_ratio")
	delete(payload, "aspectRatio")
	return json.Marshal(payload)
}

func rewriteOpenAIImagesMultipartPrompt(body []byte, contentType, prompt string) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("parse multipart content-type: %w", err)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	promptWritten := false
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, "", fmt.Errorf("read multipart body: %w", nextErr)
		}
		if name := strings.TrimSpace(part.FormName()); (name == "aspect_ratio" || name == "aspectRatio") && part.FileName() == "" {
			_ = part.Close()
			continue
		}
		target, createErr := writer.CreatePart(cloneMultipartHeader(part.Header))
		if createErr != nil {
			_ = part.Close()
			return nil, "", createErr
		}
		if strings.TrimSpace(part.FormName()) == "prompt" && part.FileName() == "" {
			_, writeErr := target.Write([]byte(prompt))
			_ = part.Close()
			if writeErr != nil {
				return nil, "", writeErr
			}
			promptWritten = true
			continue
		}
		if _, copyErr := io.Copy(target, part); copyErr != nil {
			_ = part.Close()
			return nil, "", copyErr
		}
		_ = part.Close()
	}
	if !promptWritten {
		if err := writer.WriteField("prompt", prompt); err != nil {
			return nil, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func parseOpenAIImagesAspectRatioPromptGroupIDs(raw string) []int64 {
	var ids []int64
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &ids); err != nil {
		return []int64{}
	}
	return NormalizeOpenAIImagesAspectRatioPromptGroupIDs(ids)
}

// NormalizeOpenAIImagesAspectRatioPromptGroupIDs removes invalid and duplicate IDs.
func NormalizeOpenAIImagesAspectRatioPromptGroupIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
