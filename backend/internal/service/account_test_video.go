package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// The administrator explicitly starts a billable upstream generation. It uses
// the same adapters as gateway traffic, but does not debit a user's API key.
func (s *AccountTestService) testVideoProviderAccount(c *gin.Context, a *Account, model, prompt string) error {
	if model == "" {
		ids := make([]string, 0)
		for name := range a.GetModelMapping() {
			if !strings.Contains(name, "*") {
				ids = append(ids, name)
			}
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			ids = VideoPlatformModels(a.Platform)
		}
		if len(ids) > 0 {
			model = ids[0]
		}
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Ocean waves gently reaching a sandy beach, static camera."
	}
	seconds := 5
	if a.Platform == PlatformSora {
		seconds = 4
	}
	_, resolution, routed, err := a.ResolveVideoModel(model, "")
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	if !routed {
		resolution = "720p"
	}
	body, _ := json.Marshal(map[string]any{"model": model, "prompt": prompt, "seconds": seconds, "resolution": resolution})
	commonBody := body
	endpoint, body, contentType, err := ConvertUnifiedVideoRequest(a.Platform, "application/json", body)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.prepareGrokTestSSE(c)
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})
	s.sendEvent(c, TestEvent{Type: "status", Text: "Submitting video generation..."})
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Minute)
	defer cancel()
	gateway := &OpenAIGatewayService{httpUpstream: s.httpUpstream, cfg: s.cfg}
	forward := func(endpoint GrokMediaEndpoint, key string, payload []byte) (*OpenAIForwardResult, []byte, error) {
		capture, _ := gin.CreateTestContext(httptest.NewRecorder())
		capture.Request = c.Request.Clone(ctx)
		capture.Set(VideoCaptureResponseContextKey, true)
		capture.Set(videoUnifiedInputKey, unifiedVideoInput{body: commonBody, contentType: "application/json"})
		var result *OpenAIForwardResult
		var err error
		if endpoint.IsSeedance() {
			result, err = gateway.ForwardSeedance(ctx, capture, a, endpoint, key, payload)
		} else {
			result, err = gateway.ForwardVideoProvider(ctx, capture, a, endpoint, key, payload, contentType)
		}
		if err != nil {
			return nil, nil, err
		}
		value, ok := capture.Get(videoCapturedResponseKey)
		if !ok {
			return nil, nil, fmt.Errorf("missing video response")
		}
		return result, value.(capturedVideoResponse).body, nil
	}
	created, _, err := forward(endpoint, "", body)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: "Task accepted: " + created.ResponseID + "\n"})
	statusEndpoint, _, err := ParsePublicVideoTaskID(PublicVideoTaskID(created.ResponseID))
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	for {
		select {
		case <-ctx.Done():
			return s.sendErrorAndEnd(c, "Video polling ended; task may still be processing: "+created.ResponseID)
		case <-time.After(3 * time.Second):
		}
		result, data, err := forward(statusEndpoint, created.ResponseID, nil)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		video := unifiedVideoResponse(statusEndpoint, data, result)
		status, _ := video["status"].(string)
		s.sendEvent(c, TestEvent{Type: "status", Text: status})
		if status == "failed" {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Video generation failed: %v", video["error"]))
		}
		if status != "completed" {
			continue
		}
		videoURL := ""
		if meta, ok := video["metadata"].(map[string]string); ok {
			videoURL = meta["video_url"]
		}
		if a.Platform == PlatformSora {
			base, err := gateway.validateUpstreamBaseURL(videoAccountBaseURL(a))
			if err != nil {
				return s.sendErrorAndEnd(c, err.Error())
			}
			target, err := buildVideoProviderURL(base, "sora_content", created.ResponseID)
			if err != nil {
				return s.sendErrorAndEnd(c, err.Error())
			}
			req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), http.MethodGet, target, nil)
			if err != nil {
				return s.sendErrorAndEnd(c, err.Error())
			}
			req.Header.Set("Authorization", "Bearer "+a.GetCredential("api_key"))
			resp, err := s.httpUpstream.Do(req, s.grokTestProxyURL(a), a.ID, a.Concurrency)
			if err != nil {
				return s.sendErrorAndEnd(c, err.Error())
			}
			const limit = 32 << 20
			content, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			_ = resp.Body.Close()
			if readErr != nil || resp.StatusCode != 200 || len(content) > limit {
				return s.sendErrorAndEnd(c, "Video completed, but preview download failed or exceeded 32 MiB: "+created.ResponseID)
			}
			videoURL = "data:video/mp4;base64," + base64.StdEncoding.EncodeToString(content)
		}
		if videoURL == "" {
			return s.sendErrorAndEnd(c, "Completed task did not return video content")
		}
		s.sendEvent(c, TestEvent{Type: "video", VideoURL: videoURL, MimeType: "video/mp4"})
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
}
