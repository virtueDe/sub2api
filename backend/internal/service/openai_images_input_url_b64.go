package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/imagetiming"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// rewriteOpenAIImagesInputURLsAsDataURLs converts public HTTP(S) images in the
// official JSON edits shape into Base64 Data URLs. It is called only after an
// account has been selected and the account-scoped URL response compatibility
// switch has been matched.
func (s *OpenAIGatewayService) rewriteOpenAIImagesInputURLsAsDataURLs(
	ctx context.Context,
	account *Account,
	body []byte,
	parsed *OpenAIImagesRequest,
) ([]byte, string, error) {
	if parsed == nil || account == nil || !parsed.IsEdits() || parsed.Multipart || len(parsed.InputImageURLs) == 0 {
		if parsed == nil {
			return body, "", nil
		}
		return body, parsed.ContentType, nil
	}

	converted := append([]string(nil), parsed.InputImageURLs...)
	convertedCount := 0
	convertedBytes := len(body)
	for index, rawURL := range parsed.InputImageURLs {
		if !isHTTPImageURL(rawURL) {
			continue
		}
		imageCtx := imagetiming.WithFields(ctx, zap.Int("image_index", index), zap.String("image_direction", "input"))
		downloadCtx, releaseDownload := httputil.WithRequestBodyMemoryScope(imageCtx)
		imageData, contentType, err := s.fetchOpenAIImageURLBytes(downloadCtx, account, rawURL)
		if err != nil {
			releaseDownload()
			return nil, "", fmt.Errorf("convert images[%d].image_url to base64: %w", index, err)
		}
		encodeTiming := imagetiming.Start(imageCtx, "image_base64_encode")
		if err := httputil.ReserveImageWorkingMemory(ctx, len(imageData), 2); err != nil {
			releaseDownload()
			encodeTiming.End(err)
			return nil, "", err
		}
		converted[index], err = (OpenAIImagesUpload{Data: imageData, ContentType: contentType}).DataURL()
		encodeTiming.End(nil, zap.Int("body_bytes", len(imageData)))
		releaseDownload()
		if err != nil {
			return nil, "", err
		}
		convertedBytes += len(converted[index])
		convertedCount++
	}
	if convertedCount == 0 {
		return body, parsed.ContentType, nil
	}

	parsed.InputImageURLs = converted
	if err := httputil.ReserveImageWorkingMemory(ctx, convertedBytes, 8); err != nil {
		return nil, "", err
	}
	rebuiltBody, rebuiltContentType, err := rebuildOpenAIImagesRequestBody(parsed, body)
	if err != nil {
		return nil, "", fmt.Errorf("rebuild image request after input URL conversion: %w", err)
	}
	parsed.Body = rebuiltBody
	logger.L().Info("image_url_response_b64.input_rewrite",
		zap.Int64("account_id", account.ID),
		zap.Int("converted_count", convertedCount),
	)
	return rebuiltBody, rebuiltContentType, nil
}

func isHTTPImageURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
