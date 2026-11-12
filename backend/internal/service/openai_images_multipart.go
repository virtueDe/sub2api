package service

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"sort"
	"strings"
)

// A nil value removes a scalar field. Files, unknown fields and their headers
// pass through verbatim, including transfer encodings. No-op requests reuse the
// original bytes and boundary, including duplicate fields that already agree.
func rewriteOpenAIImagesMultipartFields(body []byte, contentType string, changes map[string]*string) ([]byte, string, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, "", fmt.Errorf("parse multipart content-type: %w", err)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is required")
	}
	seen := make(map[string]bool, len(changes))
	changed := false
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("read multipart body: %w", err)
		}
		name := strings.TrimSpace(part.FormName())
		if value, ok := changes[name]; ok && part.FileName() == "" {
			seen[name] = true
			if value == nil {
				changed = true
			} else {
				data, err := io.ReadAll(io.LimitReader(part, int64(len(*value))+1))
				if err != nil {
					return nil, "", err
				}
				changed = changed || string(data) != *value
			}
		}
		if err := part.Close(); err != nil {
			return nil, "", err
		}
	}
	for name, value := range changes {
		if value != nil && !seen[name] {
			changed = true
		}
	}
	if !changed {
		return body, contentType, nil
	}
	var out bytes.Buffer
	capacity := len(body) + 512
	for _, value := range changes {
		if value != nil {
			capacity += len(*value) + 128
		}
	}
	out.Grow(capacity)
	w := multipart.NewWriter(&out)
	r = multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := r.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", err
		}
		name := strings.TrimSpace(part.FormName())
		value, replace := changes[name]
		replace = replace && part.FileName() == ""
		if replace && value == nil {
			if err := part.Close(); err != nil {
				return nil, "", err
			}
			continue
		}
		header := cloneMultipartHeader(part.Header)
		if replace {
			header.Del("Content-Transfer-Encoding")
		}
		target, err := w.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if replace {
			_, err = io.WriteString(target, *value)
		} else {
			_, err = io.Copy(target, part)
		}
		if err != nil {
			return nil, "", err
		}
		if err := part.Close(); err != nil {
			return nil, "", err
		}
	}
	missing := make([]string, 0, len(changes))
	for name, value := range changes {
		if value != nil && !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		if err := w.WriteField(name, *changes[name]); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return out.Bytes(), w.FormDataContentType(), nil
}
