// Package imagepayload removes image bytes from log copies without changing requests.
package imagepayload

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const Omitted = "[IMAGE_DATA_OMITTED]"

// Replace the image URI alone, preserving surrounding prompt text.
var imageDataURI = regexp.MustCompile(`(?i)data:image/[a-z0-9.+-]+(?:;[a-z0-9=.+-]+)*;base64,[a-z0-9+/_-]+={0,2}`)

// Line wrapping is accepted for a whole image field. In prose a newline ends
// the URI, so a following word such as "draw" is never mistaken for Base64.
var wrappedImageDataURI = regexp.MustCompile(`(?i)^data:image/[a-z0-9.+-]+(?:;[a-z0-9=.+-]+)*;base64,[a-z0-9+/_-]+(?:[\r\n]+[a-z0-9+/_-]+)*={0,2}[\r\n]*$`)

func imageKey(key string) bool {
	switch strings.ToLower(key) {
	case "image", "images", "image_url", "image_urls", "image_base64", "b64_json", "base64", "mask", "data", "result", "partial_image_b64", "reference_image", "reference_images", "reference", "input_reference", "input_image", "init_image", "image2":
		return true
	}
	return false
}

// Bare Base64 must be in an image field, have an image signature, and validate
// completely. Long URLs, prose, encrypted content and non-image Base64 survive.
func isEncodedImage(s string, declaredImage bool) bool {
	if len(s) == 0 || (!declaredImage && len(s) < 16) || strings.ContainsAny(s, " \t:") {
		return false
	}
	encoding := base64.StdEncoding
	if strings.ContainsAny(s, "-_") {
		encoding = base64.URLEncoding
	}
	if !strings.HasSuffix(strings.TrimRight(s, "\r\n"), "=") {
		encoding = encoding.WithPadding(base64.NoPadding)
	}
	reader := base64.NewDecoder(encoding, strings.NewReader(s))
	var prefix [512]byte
	n, err := io.ReadFull(reader, prefix[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false
	}
	// AVIF uses ISO BMFF and is not recognized by net/http's image sniffing.
	avif := n >= 12 && (bytes.Equal(prefix[4:12], []byte("ftypavif")) || bytes.Equal(prefix[4:12], []byte("ftypavis")))
	if !declaredImage && !avif && !strings.HasPrefix(http.DetectContentType(prefix[:n]), "image/") {
		return false
	}
	_, err = io.Copy(io.Discard, reader)
	return err == nil
}

func cleanString(s, key string) (string, int64) {
	if (imageKey(key) || key == "declared_image_data") && wrappedImageDataURI.MatchString(s) {
		return Omitted, max(0, int64(len(s)-len(Omitted)))
	}
	if (imageKey(key) || key == "declared_image_data") && isEncodedImage(s, key == "declared_image_data") {
		return Omitted, max(0, int64(len(s)-len(Omitted)))
	}
	var removed int64
	clean := imageDataURI.ReplaceAllStringFunc(s, func(uri string) string {
		removed += max(0, int64(len(uri)-len(Omitted)))
		return Omitted
	})
	return clean, removed
}

// SanitizeInput edits only a decoded log copy. Historical cleanup does not
// modify credentials; capture retains the existing credential redaction policy.
func SanitizeInput(value any, redactCredentials bool) {
	cleanValue(value, "", redactCredentials)
}

func cleanValue(value any, key string, redact bool) (any, int64, bool) {
	var removed int64
	changed := false
	switch v := value.(type) {
	case map[string]any:
		declaredImage := false
		for _, mimeKey := range []string{"mimeType", "mime_type", "media_type"} {
			if mime, ok := v[mimeKey].(string); ok && strings.HasPrefix(strings.ToLower(mime), "image/") {
				declaredImage = true
			}
		}
		for k, child := range v {
			credential := false
			switch strings.ToLower(k) {
			case "api_key", "apikey", "authorization", "access_token", "refresh_token", "password", "secret":
				credential = true
			}
			if credential {
				if redact {
					v[k] = "[REDACTED]"
					changed = true
				}
				continue
			}
			childKey := k
			if declaredImage && (strings.EqualFold(k, "data") || strings.EqualFold(k, "base64")) {
				childKey = "declared_image_data"
			}
			if strings.EqualFold(k, "url") && imageKey(key) {
				childKey = key
			}
			clean, n, modified := cleanValue(child, childKey, redact)
			v[k], removed, changed = clean, removed+n, changed || modified
		}
	case []any:
		for i, child := range v {
			clean, n, modified := cleanValue(child, key, redact)
			v[i], removed, changed = clean, removed+n, changed || modified
		}
	case string:
		clean, n := cleanString(v, key)
		return clean, n, clean != v
	}
	return value, removed, changed
}

// CleanDetail preserves unknown fields, array order and exact JSON numbers.
func CleanDetail(raw []byte, expireOutput bool) ([]byte, int64, bool, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, 0, false, err
	}
	detail, ok := value.(map[string]any)
	if !ok {
		return raw, 0, false, nil
	}
	var removed int64
	changed := false
	if params, ok := detail["request_params"]; ok {
		clean, n, modified := cleanValue(params, "", false)
		detail["request_params"], removed, changed = clean, removed+n, changed || modified
	}
	if prompt, ok := detail["prompt"].(string); ok {
		clean, n := cleanString(prompt, "prompt")
		if clean != prompt {
			detail["prompt"], changed = clean, true
			removed += n
		}
	}
	if images, ok := detail["images"].([]any); ok && expireOutput {
		for _, value := range images {
			image, ok := value.(map[string]any)
			if !ok {
				continue
			}
			if b64, ok := image["b64_json"].(string); ok {
				delete(image, "b64_json")
				removed += int64(len(b64))
				changed = true
			}
			for _, key := range []string{"url", "revised_prompt"} {
				if text, ok := image[key].(string); ok {
					imageKey := key
					if key == "url" {
						imageKey = "image_url"
					}
					clean, n := cleanString(text, imageKey)
					if clean != text {
						image[key], changed = clean, true
						if key == "url" && clean == Omitted {
							delete(image, key)
						}
						removed += n
					}
				}
			}
		}
	}
	if !changed {
		return raw, 0, false, nil
	}
	clean, err := json.Marshal(detail)
	return clean, removed, true, err
}
