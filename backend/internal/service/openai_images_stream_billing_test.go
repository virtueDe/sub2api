package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestImagesStreamBillingCompletion(t *testing.T) {
	completed := "event: image_generation.completed\ndata: {\"id\":\"img-1\",\"b64_json\":\"AA==\",\"usage\":{\"input_tokens\":10,\"output_tokens\":18,\"output_tokens_details\":{\"image_tokens\":8}}}\n\n"
	failure := "event: error\ndata: {\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed\"}}\n\n"
	flatFailure := "event: error\ndata: {\"code\":\"server_error\",\"message\":\"upstream failed\"}\n\n"
	for _, async := range []bool{false, true} {
		for _, tc := range []struct {
			name    string
			wire    string
			count   int
			wantErr bool
		}{
			{name: "empty", wantErr: true},
			{name: "heartbeat_done", wire: ": heartbeat\n\ndata: [DONE]\n\n", wantErr: true},
			{name: "partial_only", wire: "event: image_generation.partial_image\ndata: {\"b64_json\":\"AA==\"}\n\n", wantErr: true},
			{name: "upstream_error", wire: failure, wantErr: true},
			{name: "event_completion", wire: completed, count: 1},
			{name: "edit_multiline_crlf", wire: "event: image_edit.completed\r\ndata: {\r\ndata: \"b64_json\":\"AA==\"}\r\n\r\n", count: 1},
			{name: "json_type_wins", wire: "event: image_generation.completed\ndata: {\"type\":\"image_generation.partial_image\",\"b64_json\":\"AA==\"}\n\n", wantErr: true},
			{name: "empty_event_resets_type", wire: "event: image_generation.completed\n\ndata: {\"b64_json\":\"AA==\"}\n\n", wantErr: true},
			{name: "duplicate_completion", wire: completed + completed + "event: image_generation.completed\ndata: {\"id\":\"img-2\",\"b64_json\":\"AQ==\"}\n\n", count: 2},
			{name: "json_fallback", wire: `{"data":[{"b64_json":"AA=="}]}`, count: 1},
			{name: "completed_then_error", wire: completed + failure, count: 1, wantErr: true},
			{name: "completed_then_flat_error", wire: completed + flatFailure, count: 1, wantErr: true},
			{name: "error_then_completed", wire: failure + completed, wantErr: true},
		} {
			t.Run(fmt.Sprintf("async_%t/%s", async, tc.name), func(t *testing.T) {
				c, rec := newOpenAIImagesTestContext(t, nil)
				svc := newOpenAIImagesTestService(nil)
				if async {
					svc.cfg.Gateway.ImageStreamDataIntervalTimeout = 1
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.wire))}
				usage, count, _, _, err := svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), nil)
				if tc.wantErr {
					require.Error(t, err)
					if strings.Contains(tc.wire, "upstream failed") {
						require.Contains(t, err.Error(), "upstream failed")
					}
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, tc.count, count)
				require.Equal(t, tc.wire, rec.Body.String(), "accounting must not rewrite passthrough bytes")
				if strings.HasPrefix(tc.wire, completed) {
					require.Equal(t, OpenAIUsage{InputTokens: 10, OutputTokens: 18, ImageOutputTokens: 8}, usage)
				}
				if tc.name != "json_fallback" && tc.name != "error_then_completed" {
					require.Equal(t, tc.count, countOpenAIImageOutputsFromSSEBody(tc.wire))
				}
			})
		}
	}
}

func TestImagesStreamDirectEventOnlyCompletionAndError(t *testing.T) {
	complete := "event: image_generation.completed\ndata: {\"b64_json\":\"AA==\"}\n\n"
	for _, async := range []bool{false, true} {
		for _, withError := range []bool{false, true} {
			t.Run(fmt.Sprintf("async_%t/error_%t", async, withError), func(t *testing.T) {
				c, rec := newOpenAIImagesTestContext(t, nil)
				svc := newOpenAIImagesTestService(nil)
				if async {
					svc.cfg.Gateway.ImageStreamDataIntervalTimeout = 1
				}
				wire := complete
				if withError {
					wire += "event: error\ndata: {\"code\":\"server_error\",\"message\":\"upstream failed\"}\n\n"
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}
				_, count, _, _, err := svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), &OpenAIImagesRequest{Model: "gpt-image-2", ResponseFormat: "b64_json"})
				if withError {
					var upstreamErr *OpenAIImagesUpstreamError
					require.ErrorAs(t, err, &upstreamErr)
					require.Equal(t, "server_error", upstreamErr.Code)
					require.Equal(t, "api_error", upstreamErr.ErrorType)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, 1, count)
				require.Equal(t, 1, countOpenAIImageOutputsFromSSEBody(rec.Body.String()))
			})
		}
	}
}

type imageStreamBillingReadError struct{}

func (imageStreamBillingReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestImagesStreamCompletedBeforeReadError(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			t.Run(fmt.Sprintf("async_%t/direct_%t", async, direct), func(t *testing.T) {
				c, rec := newOpenAIImagesTestContext(t, nil)
				svc := newOpenAIImagesTestService(nil)
				if async {
					svc.cfg.Gateway.ImageStreamDataIntervalTimeout = 1
				}
				wire := "event: image_generation.completed\ndata: {\"b64_json\":\"AA==\",\"usage\":{\"input_tokens\":10,\"output_tokens\":18}}\n\n"
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(io.MultiReader(strings.NewReader(wire), imageStreamBillingReadError{}))}
				var request *OpenAIImagesRequest
				if direct {
					request = &OpenAIImagesRequest{Model: "gpt-image-2", ResponseFormat: "b64_json"}
				}
				usage, count, _, _, err := svc.handleOpenAIImagesStreamingResponse(resp, c, time.Now(), request)
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
				require.Equal(t, 1, count, "completed output remains billable after a read failure")
				require.Equal(t, 10, usage.InputTokens)
				require.Equal(t, 18, usage.OutputTokens)
				require.Equal(t, 1, countOpenAIImageOutputsFromSSEBody(rec.Body.String()))
			})
		}
	}
}
