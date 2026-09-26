package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

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
	for index, rawURL := range parsed.InputImageURLs {
		if !isHTTPImageURL(rawURL) {
			continue
		}
		imageData, contentType, err := s.fetchOpenAIImageURLBytes(ctx, account, rawURL)
		if err != nil {
			return nil, "", fmt.Errorf("convert images[%d].image_url to base64: %w", index, err)
		}
		converted[index] = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(imageData)
		convertedCount++
	}
	if convertedCount == 0 {
		return body, parsed.ContentType, nil
	}

	parsed.InputImageURLs = converted
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
