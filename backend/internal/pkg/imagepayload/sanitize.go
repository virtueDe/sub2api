// Package imagepayload removes image bytes from log copies without changing requests.
package imagepayload

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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

var imageBase64Text = regexp.MustCompile(`^[a-zA-Z0-9+/_=\r\n-]+$`)

func imageKey(key string) bool {
	switch strings.ToLower(key) {
	case "image", "images", "image_url", "image_urls", "image_base64", "b64_json", "base64", "mask", "data", "result", "partial_image_b64", "reference_image", "reference_images", "reference", "input_reference", "input_image", "init_image", "image2":
		return true
	}
	return false
}

// Bare Base64 must have an image signature or an explicit image MIME type and
// validate completely. URLs, prose and non-image Base64 survive.
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
	// These fields explicitly contain image bytes, even if an upstream returns
	// malformed or truncated Base64 which cannot pass signature validation.
	switch strings.ToLower(key) {
	case "b64_json", "partial_image_b64", "image_base64":
		if s != "" && s != Omitted {
			return Omitted, max(0, int64(len(s)-len(Omitted)))
		}
	}
	// MIME context identifies the image even when a diagnostic was truncated
	// halfway through a Base64 quantum. Ordinary URLs still pass through.
	if key == "declared_image_data" && imageBase64Text.MatchString(s) {
		return Omitted, max(0, int64(len(s)-len(Omitted)))
	}
	if (imageKey(key) || key == "declared_image_data") && wrappedImageDataURI.MatchString(s) {
		return Omitted, max(0, int64(len(s)-len(Omitted)))
	}
	if isEncodedImage(s, key == "declared_image_data") {
		return Omitted, max(0, int64(len(s)-len(Omitted)))
	}
	var removed int64
	clean := imageDataURI.ReplaceAllStringFunc(s, func(uri string) string {
		removed += max(0, int64(len(uri)-len(Omitted)))
		return Omitted
	})
	return clean, removed
}

// SanitizeInput edits a decoded log copy and returns the cleaned root value
// (which can itself be a string). Historical cleanup leaves credentials alone.
func SanitizeInput(value any, redactCredentials bool) any {
	clean, _, _ := cleanValue(value, "", redactCredentials)
	return clean
}

func cleanValue(value any, key string, redact bool) (any, int64, bool) {
	return cleanValueAt(value, key, redact, 0)
}

func cleanValueAt(value any, key string, redact bool, embeddedDepth int) (any, int64, bool) {
	var removed int64
	changed := false
	switch v := value.(type) {
	case map[string]any:
		declaredImage := false
		imageResult := false
		if typ, ok := v["type"].(string); ok {
			imageResult = strings.HasPrefix(typ, "image_generation") || strings.HasPrefix(typ, "response.image_generation_call.")
		}
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
			if imageResult && k == "result" {
				childKey = "b64_json"
			}
			if declaredImage && (strings.EqualFold(k, "data") || strings.EqualFold(k, "base64")) {
				childKey = "declared_image_data"
			}
			if strings.EqualFold(k, "url") && imageKey(key) {
				childKey = key
			}
			clean, n, modified := cleanValueAt(child, childKey, redact, embeddedDepth)
			v[k], removed, changed = clean, removed+n, changed || modified
		}
	case []any:
		for i, child := range v {
			clean, n, modified := cleanValueAt(child, key, redact, embeddedDepth)
			v[i], removed, changed = clean, removed+n, changed || modified
		}
	case string:
		clean, n := cleanLogString(v, key, embeddedDepth)
		return clean, n, clean != v
	}
	return value, removed, changed
}

func cleanLogString(text, key string, embeddedDepth int) (string, int64) {
	switch strings.ToLower(key) {
	case "b64_json", "partial_image_b64", "image_base64":
		return cleanString(text, key)
	}
	// Only explicit image fields may treat a following Base64-like line as
	// image continuation. In prose that line may be an ordinary word.
	if strings.HasPrefix(strings.ToLower(text), "data:image/") && (imageKey(key) || key == "declared_image_data") {
		if clean, n := cleanString(text, key); clean == Omitted {
			return clean, n
		}
	}
	embedded := sanitizeTextAt(text, embeddedDepth)
	clean, n := cleanString(embedded, key)
	return clean, n + max(0, int64(len(text)-len(embedded)))
}

// SanitizeJSON removes image payloads from a persistence copy. Numbers retain
// their original precision; URLs, prompts and non-image business data survive.
func SanitizeJSON(raw []byte) ([]byte, error) {
	return sanitizeJSONAt(raw, 0)
}

func sanitizeJSONAt(raw []byte, embeddedDepth int) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values in image log payload")
		}
		return nil, err
	}
	clean, _, changed := cleanValueAt(value, "", false, embeddedDepth)
	if !changed {
		return raw, nil
	}
	return json.Marshal(clean)
}

// SanitizeText also handles JSON embedded in log messages, SSE lines and
// incomplete body snippets. Call this before truncating a diagnostic payload.
func SanitizeText(text string) string {
	return sanitizeTextAt(text, 0)
}

func sanitizeTextAt(text string, embeddedDepth int) string {
	if !strings.ContainsAny(text, "{[\"") {
		clean, _ := cleanString(text, "")
		return clean
	}
	// Scan complete objects/arrays once, including SSE and JSON inside error
	// strings. The limit applies only to JSON encoded inside strings, not normal
	// nested objects. Fragments are still filtered when the limit is reached.
	if embeddedDepth < 8 {
		var out strings.Builder
		last, start, nesting := 0, -1, 0
		quoted, escaped := false, false
		for i := 0; i < len(text); i++ {
			ch := text[i]
			if start < 0 {
				if ch == '{' || ch == '[' {
					start, nesting = i, 1
					quoted, escaped = false, false
				}
				continue
			}
			if quoted {
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == '"' {
					quoted = false
				}
				continue
			}
			switch ch {
			case '"':
				quoted = true
			case '{', '[':
				nesting++
			case '}', ']':
				nesting--
				if nesting == 0 {
					if clean, err := sanitizeJSONAt([]byte(text[start:i+1]), embeddedDepth+1); err == nil && string(clean) != text[start:i+1] {
						_, _ = out.WriteString(text[last:start])
						_, _ = out.Write(clean)
						last = i + 1
					}
					start = -1
				}
			}
		}
		if last > 0 {
			_, _ = out.WriteString(text[last:])
			text = out.String()
		}
	}
	// Malformed/truncated JSON cannot be decoded. Inspect string fields without
	// stopping at physical line breaks, so wrapped Base64 tails cannot leak.
	text = sanitizeJSONFragments(text)
	text, _ = cleanString(text, "")
	return text
}

var imageJSONFragment = regexp.MustCompile(`(?s)("[a-zA-Z_][a-zA-Z0-9_.-]*"\s*:\s*")((?:[^"\\]|\\.)*)`)

func sanitizeJSONFragments(text string) string {
	type imageContext struct{ declared, result bool }
	type field struct {
		start, end int
		key, value string
		context    *imageContext
	}
	var fields []field
	stack := []*imageContext{{}}
	cursor := 0
	quoted, escaped := false, false
	for _, match := range imageJSONFragment.FindAllStringSubmatchIndex(text, -1) {
		// Keep MIME/type context within its own object even when its final
		// quote/brace is missing. Neighbouring objects never share context.
		for ; cursor < match[0]; cursor++ {
			ch := text[cursor]
			if quoted {
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == '"' {
					quoted = false
				}
				continue
			}
			switch ch {
			case '"':
				quoted = true
			case '{':
				stack = append(stack, &imageContext{})
			case '}':
				if len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
			}
		}
		prefix, value := text[match[2]:match[3]], text[match[4]:match[5]]
		key := prefix[1 : strings.IndexByte(prefix[1:], '"')+1]
		var decoded string
		if strings.Contains(value, `\`) && json.Unmarshal([]byte(`"`+value+`"`), &decoded) == nil {
			value = decoded
		}
		ctx := stack[len(stack)-1]
		switch strings.ToLower(key) {
		case "type":
			ctx.result = strings.HasPrefix(value, "image_generation") || strings.HasPrefix(value, "response.image_generation_call.")
		case "mimetype", "mime_type", "media_type":
			ctx.declared = strings.HasPrefix(strings.ToLower(value), "image/")
		}
		fields = append(fields, field{match[4], match[5], key, value, ctx})
		cursor = match[1]
		if cursor < len(text) && text[cursor] == '"' {
			cursor++
		}
		quoted, escaped = false, false
	}
	var out strings.Builder
	last := 0
	for _, f := range fields {
		key := f.key
		if f.context.result && key == "result" {
			key = "b64_json"
		}
		if f.context.declared && (strings.EqualFold(key, "data") || strings.EqualFold(key, "base64")) {
			key = "declared_image_data"
		}
		clean, _ := cleanString(f.value, key)
		if clean == f.value {
			continue
		}
		encoded, _ := json.Marshal(clean)
		_, _ = out.WriteString(text[last:f.start])
		_, _ = out.Write(encoded[1 : len(encoded)-1])
		last = f.end
	}
	if last == 0 {
		return text
	}
	_, _ = out.WriteString(text[last:])
	return out.String()
}

// SanitizeField applies field context before inspecting embedded log text.
func SanitizeField(key, text string) string {
	clean, _ := cleanLogString(text, key, 0)
	return clean
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
