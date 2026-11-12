package service

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// Retain the immutable request, not another copy of each file. Reopening through
// mime/multipart also preserves its quoted-printable decoding and boundary rules.
type imageUploadSource struct {
	body     []byte
	boundary string
	part     int
	size     int64
}

// ImageUploadEncoder belongs to one synchronous encoding pass. Uploads parsed
// in source order share a reader, so N images do not rescan N body prefixes.
// A backwards reference (for example a mask) starts another standard MIME pass.
// It never mutates uploads and must not be shared between goroutines.
type ImageUploadEncoder struct {
	body     []byte
	boundary string
	reader   *multipart.Reader
	nextPart int
}

func (e *ImageUploadEncoder) DataURL(u OpenAIImagesUpload) (string, error) {
	if u.source == nil {
		return u.DataURL()
	}
	source := u.source
	if e.reader == nil || e.nextPart > source.part || e.boundary != source.boundary ||
		len(e.body) != len(source.body) || len(source.body) == 0 || &e.body[0] != &source.body[0] {
		e.body, e.boundary, e.nextPart = source.body, source.boundary, 0
		e.reader = multipart.NewReader(bytes.NewReader(e.body), e.boundary)
	}
	for {
		part, err := e.reader.NextPart()
		if err != nil {
			e.reader = nil
			return "", fmt.Errorf("read image upload: %w", err)
		}
		index := e.nextPart
		e.nextPart++
		if index == source.part {
			result, err := u.dataURL(part)
			_ = part.Close()
			return result, err
		}
		if err := part.Close(); err != nil {
			e.reader = nil
			return "", err
		}
	}
}

func (e *ImageUploadEncoder) moderationDataURL(u OpenAIImagesUpload) string {
	dataURL, err := e.DataURL(u)
	if err != nil || !isImageDataURL(dataURL) {
		return ""
	}
	return dataURL
}

func isImageDataURL(value string) bool {
	return len(value) >= len("data:image/") && strings.EqualFold(value[:len("data:image/")], "data:image/")
}

func (u OpenAIImagesUpload) Size() int64 {
	if u.source != nil {
		return u.source.size
	}
	return int64(len(u.Data))
}

func (u OpenAIImagesUpload) Open() (io.ReadCloser, error) {
	if u.source == nil {
		return io.NopCloser(bytes.NewReader(u.Data)), nil
	}
	r := multipart.NewReader(bytes.NewReader(u.source.body), u.source.boundary)
	for i := 0; ; i++ {
		part, err := r.NextPart()
		if err != nil {
			return nil, fmt.Errorf("reopen image upload: %w", err)
		}
		if i == u.source.part {
			return part, nil
		}
		if err := part.Close(); err != nil {
			return nil, err
		}
	}
}

// DataURL encodes directly from the validated upload, without first copying its
// binary data. This is used only by protocols or moderation that need base64.
func (u OpenAIImagesUpload) DataURL() (string, error) {
	if u.Size() == 0 {
		return "", fmt.Errorf("upload %q is empty", strings.TrimSpace(u.FileName))
	}
	r, err := u.Open()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	return u.dataURL(r)
}

func (u OpenAIImagesUpload) dataURL(r io.Reader) (string, error) {
	if u.Size() == 0 {
		return "", fmt.Errorf("upload %q is empty", strings.TrimSpace(u.FileName))
	}
	contentType := strings.TrimSpace(u.ContentType)
	input := r
	if contentType == "" {
		var prefix [512]byte
		n, readErr := io.ReadFull(r, prefix[:])
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return "", readErr
		}
		contentType = http.DetectContentType(prefix[:n])
		input = io.MultiReader(bytes.NewReader(prefix[:n]), r)
	}
	var out strings.Builder
	out.Grow(len(contentType) + 13 + base64.StdEncoding.EncodedLen(int(u.Size())))
	_, _ = out.WriteString("data:")
	_, _ = out.WriteString(contentType)
	_, _ = out.WriteString(";base64,")
	encoder := base64.NewEncoder(base64.StdEncoding, &out)
	if _, err := io.Copy(encoder, input); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}
