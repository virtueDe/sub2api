package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImageURLInputConversionAccountsDownloadedBytes(t *testing.T) {
	for _, limit := range []int64{4 << 20, 256 << 20} {
		t.Run(fmt.Sprint(limit>>20), func(t *testing.T) {
			budget := httputil.NewBodyMemoryBudget(limit)
			base, closeBase := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
			defer closeBase()
			body := []byte(`{"model":"gpt-image-2","prompt":"keep","images":[{"image_url":"https://cdn.example.com/input.png"}]}`)
			ctx, release, err := httputil.AdmitImageRequestMemory(base, 1, int64(len(body)), false)
			require.NoError(t, err)
			data := append(append([]byte(nil), b64BackfillPNGBytes...), bytes.Repeat([]byte{1}, 5<<20)...)
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: &httpUpstreamRecorder{resp: b64BackfillImageResponse(200, "image/png", data)}}
			parsed := &OpenAIImagesRequest{Endpoint: openAIImagesEditsEndpoint, ContentType: "application/json", InputImageURLs: []string{"https://cdn.example.com/input.png"}}
			out, _, err := svc.rewriteOpenAIImagesInputURLsAsDataURLs(ctx, b64BackfillAccount(true), body, parsed)
			if limit < 64<<20 {
				require.ErrorIs(t, err, httputil.ErrRequestBodyMemoryBudget)
				require.Nil(t, out)
			} else {
				require.NoError(t, err)
				require.Equal(t, "keep", gjson.GetBytes(out, "prompt").String())
				require.Greater(t, len(out), len(data))
			}
			release()
			closeBase()
			probe, done := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
			defer done()
			require.NoError(t, httputil.ReserveRequestBodyMemory(probe, limit), "download/conversion leaked budget")
		})
	}
}

func TestImageDataURLBackfillRespectsMemoryBudget(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 3<<18))
	for _, limit := range []int64{1024, 16 << 20} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			budget := httputil.NewBodyMemoryBudget(limit)
			ctx, release := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
			svc := &OpenAIGatewayService{}
			got, err := svc.fetchOpenAIImageURLBase64(ctx, nil, "data:image/png;base64,"+encoded)
			if limit == 1024 {
				require.ErrorIs(t, err, httputil.ErrRequestBodyMemoryBudget)
				require.Empty(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, encoded, got)
			}
			release()
			probe, closeProbe := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
			defer closeProbe()
			require.NoError(t, httputil.ReserveRequestBodyMemory(probe, limit))
		})
	}
}

func TestContentModerationAsyncOwnsOnlyCopiedContent(t *testing.T) {
	budget := httputil.NewBodyMemoryBudget(1024)
	ctx, release := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
	queue := make(chan contentModerationTask, 1)
	svc := &ContentModerationService{asyncQueue: queue}
	input := ContentModerationCheckInput{Body: bytes.Repeat([]byte{'x'}, 500), BuildBody: func(bool) []byte { panic("must not survive enqueue") }}
	content := ContentModerationInput{Text: "prompt", Images: []string{"https://example.com/image.png"}}
	svc.enqueueAsync(input, defaultContentModerationConfig(), content, content.Hash(), ctx)
	task := <-queue
	require.Nil(t, task.input.Body)
	require.Nil(t, task.input.BuildBody)
	require.Equal(t, content, task.content)
	release()
	probe, done := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
	defer done()
	require.ErrorIs(t, httputil.ReserveRequestBodyMemory(probe, 1024), httputil.ErrRequestBodyMemoryBudget)
	task.releaseMemory()
	require.NoError(t, httputil.ReserveRequestBodyMemory(probe, 1024))
}
