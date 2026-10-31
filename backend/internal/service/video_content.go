package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Called only after the handler has selected the task's bound account. Status is
// fetched first so downloading without a prior poll still settles completed work.
func (s *OpenAIGatewayService) ForwardProviderVideoContent(ctx context.Context, c *gin.Context, a *Account, endpoint GrokMediaEndpoint, key string) (*OpenAIForwardResult, error) {
	statusContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	statusContext.Request = c.Request.Clone(ctx)
	statusContext.Set(VideoCaptureResponseContextKey, true)
	var result *OpenAIForwardResult
	var err error
	if endpoint.IsSeedance() {
		result, err = s.ForwardSeedance(ctx, statusContext, a, endpoint, key, nil)
	} else {
		result, err = s.ForwardVideoProvider(ctx, statusContext, a, endpoint, key, nil, "")
	}
	if err != nil {
		return nil, err
	}
	value, ok := statusContext.Get(videoCapturedResponseKey)
	if !ok {
		return nil, fmt.Errorf("video status response is unavailable")
	}
	data := value.(capturedVideoResponse).body
	provider := endpoint.VideoProvider()
	if endpoint.IsSeedance() {
		provider = PlatformSeedance
	}
	status := unifiedVideoResponse(endpoint, data, result)["status"]
	if status != "completed" {
		c.JSON(http.StatusConflict, gin.H{"error": gin.H{"type": "video_not_ready", "message": "Video is not completed"}})
		return nil, fmt.Errorf("video is not completed")
	}
	if provider == PlatformSora {
		_, err := s.ForwardVideoProvider(ctx, c, a, "sora_content", key, nil, "")
		return result, err
	}
	path := ""
	switch provider {
	case PlatformSeedance:
		path = "content.video_url"
	case PlatformWan:
		path = "output.video_url"
	case PlatformKling:
		path = "data.task_result.videos.0.url"
	}
	rawURL := gjson.GetBytes(data, path).String()
	if provider == PlatformWan && rawURL == "" {
		rawURL = gjson.GetBytes(data, "output.results.video_url").String()
	}
	target, err := url.Parse(rawURL)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil {
		return nil, fmt.Errorf("invalid upstream video content URL")
	}
	// Public-media download transport checks DNS and connected addresses, including
	// every redirect. No account credentials or header overrides enter this request.
	var proxy func(*http.Request) (*url.URL, error)
	if a.ProxyID != nil && a.Proxy != nil {
		proxyURL, err := url.Parse(a.Proxy.URL())
		if err != nil {
			return nil, err
		}
		proxy = http.ProxyURL(proxyURL)
	}
	client := newImageDownloadHTTPClient(proxy)
	client.Timeout = 5 * time.Minute
	defer client.CloseIdleConnections()
	method := http.MethodGet
	if c.Request.Method == http.MethodHead {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, err
	}
	if value := strings.TrimSpace(c.GetHeader("Range")); value != "" {
		req.Header.Set("Range", value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 && resp.StatusCode != 206 && resp.StatusCode != 416 {
		return nil, fmt.Errorf("video content download status %d", resp.StatusCode)
	}
	return result, writeGrokMediaContentResponse(c, resp)
}
