package service

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImagesSizeValidationJSON(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  any
		valid bool
	}{
		{"auto", "auto", true},
		{"custom", "provider-custom-size", true},
		{"unicode_at_limit", strings.Repeat("中", 32), true},
		{"ascii_over_limit", strings.Repeat("x", 33), false},
		{"unicode_over_limit", strings.Repeat("中", 33), false},
		{"number", 1024, false},
		{"object", map[string]int{"width": 1024}, false},
		{"array", []string{"1024x1024"}, false},
		{"boolean", true, false},
		{"null", nil, false},
	} {
		for _, gemini := range []bool{false, true} {
			name := tc.name + "/openai"
			if gemini {
				name = tc.name + "/gemini"
			}
			t.Run(name, func(t *testing.T) {
				body, err := json.Marshal(map[string]any{"model": "gpt-image-2", "prompt": "test", "size": tc.size})
				require.NoError(t, err)
				c, _ := newOpenAIImagesTestContext(t, body)
				svc := newOpenAIImagesTestService(nil)
				parsed, err := svc.parseOpenAIImagesRequest(c, body, gemini)
				if tc.valid {
					require.NoError(t, err)
					require.Equal(t, tc.size, parsed.Size)
				} else {
					require.Error(t, err)
					require.Contains(t, err.Error(), "size")
					require.Nil(t, parsed)
				}
			})
		}
	}
}

func TestImagesSizeValidationMultipart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sizes []string
		valid bool
	}{
		{"custom", []string{"provider-custom-size"}, true},
		{"unicode_at_limit", []string{strings.Repeat("中", 32)}, true},
		{"unicode_over_limit", []string{strings.Repeat("中", 33)}, false},
		{"overlong_then_valid", []string{strings.Repeat("x", 124), "1024x1024"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			for _, size := range tc.sizes {
				require.NoError(t, writer.WriteField("size", size))
			}
			require.NoError(t, writer.Close())
			request := &OpenAIImagesRequest{}
			err := parseOpenAIImagesMultipartRequestMode(body.Bytes(), writer.FormDataContentType(), request, false)
			if tc.valid {
				require.NoError(t, err)
				require.Equal(t, tc.sizes[len(tc.sizes)-1], request.Size)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), "size")
			}
		})
	}
}

func TestImagesSizeValidationRejectsDuplicateJSONKeys(t *testing.T) {
	for _, gemini := range []bool{false, true} {
		for _, second := range []string{`123`, `"auto"`, `"` + strings.Repeat("x", 33) + `"`} {
			body := []byte(`{"model":"gpt-image-2","prompt":"test","size":"auto","size":` + second + `}`)
			if gemini {
				body = []byte(`{"model":"gpt-image-2","prompt":"test","stream":"invalid","size":"auto","size":` + second + `}`)
			}
			c, _ := newOpenAIImagesTestContext(t, body)
			parsed, err := newOpenAIImagesTestService(nil).parseOpenAIImagesRequest(c, body, gemini)
			require.ErrorContains(t, err, "size must be specified only once")
			require.Nil(t, parsed)
		}
	}
}
