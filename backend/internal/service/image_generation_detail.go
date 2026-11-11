package service

import (
	"context"
	"encoding/json"
)

// ImageGenerationDetail describes legacy usage/error details for historical reads.
// New requests do not persist prompts, request parameters or generated images.
type ImageGenerationDetail struct {
	Prompt        string           `json:"prompt"`
	RequestParams map[string]any   `json:"request_params"`
	Images        []GeneratedImage `json:"images"`
}

type GeneratedImage struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	MimeType      string `json:"mime_type,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
	Partial       bool   `json:"partial,omitempty"`
}

type imageGenerationContextKey struct{}

// ImageGenerationCapture remains an image-request marker for billing admission.
// It deliberately holds no request or response data.
type ImageGenerationCapture struct{}

func WithImageGenerationCapture(ctx context.Context, capture *ImageGenerationCapture) context.Context {
	return context.WithValue(ctx, imageGenerationContextKey{}, capture)
}

func ImageGenerationCaptureFromContext(ctx context.Context) *ImageGenerationCapture {
	if ctx == nil {
		return nil
	}
	capture, _ := ctx.Value(imageGenerationContextKey{}).(*ImageGenerationCapture)
	return capture
}

func ImageGenerationDetailFromContext(_ context.Context) *ImageGenerationDetail { return nil }

// WithImageGenerationDetail preserves billing contexts without retaining detail data.
func WithImageGenerationDetail(ctx context.Context, _ *ImageGenerationDetail) context.Context {
	return ctx
}

func NewImageGenerationCapture(_ string, _ []byte) *ImageGenerationCapture {
	return &ImageGenerationCapture{}
}

func (c *ImageGenerationCapture) ObserveResponse(_ []byte, _ bool) {}
func (c *ImageGenerationCapture) Snapshot() *ImageGenerationDetail { return nil }

// ImageGenerationDetailJSON is the shared persistence boundary for usage and Ops.
// Return SQL NULL even when a caller constructs a detail directly.
func ImageGenerationDetailJSON(_ *ImageGenerationDetail) any { return nil }

func ParseImageGenerationDetail(data string) (*ImageGenerationDetail, error) {
	if data == "" || data == "null" {
		return nil, nil
	}
	var detail ImageGenerationDetail
	if err := json.Unmarshal([]byte(data), &detail); err != nil {
		return nil, err
	}
	return &detail, nil
}
