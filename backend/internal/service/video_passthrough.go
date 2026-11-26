package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func videoAPIPassthroughStatusEndpoint(platform string) GrokMediaEndpoint {
	return GrokMediaEndpoint(platform + "_passthrough_status")
}

func (e GrokMediaEndpoint) IsVideoAPIPassthrough() bool {
	return strings.HasSuffix(string(e), "_passthrough_status") && e.VideoProvider() != ""
}

func (s *OpenAIGatewayService) ValidateVideoAPIPassthroughPricing(ctx context.Context, key *APIKey, account *Account, info GrokMediaRequestInfo) error {
	pricing := s.resolveOpenAIChannelPricing(ctx, info.Model, key)
	if pricing == nil || (pricing.Source != PricingSourceGroup && pricing.Source != PricingSourceChannel) {
		return fmt.Errorf("configure video pricing for this model before submitting tasks")
	}
	switch pricing.Mode {
	case BillingModePerRequest:
		return nil
	case BillingModeToken:
		if account.Platform == PlatformSeedance {
			return nil
		}
	case BillingModeVideo:
		if info.DurationSeconds <= 0 || info.Resolution == "" {
			return fmt.Errorf("per-second video pricing requires an explicit duration and resolution or size; use per-request pricing for upstream defaults")
		}
		return nil
	}
	return fmt.Errorf("configure per-request or per-second video pricing for this model before submitting tasks")
}

// Only read fields needed for routing, moderation and billing. Vendor-specific
// fields and values remain in the original body until an account is selected.
func ParseVideoAPIRoutingRequest(contentType string, body []byte) (GrokMediaRequestInfo, error) {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if contentType != "" && (err != nil || (mediaType != "application/json" && mediaType != "multipart/form-data")) {
		return GrokMediaRequestInfo{}, fmt.Errorf("video requests require JSON or multipart/form-data")
	}
	fields, err := videoRequestFields(body, contentType)
	if err != nil {
		return GrokMediaRequestInfo{}, err
	}
	if !gjson.ValidBytes(fields) || !gjson.ParseBytes(fields).IsObject() {
		return GrokMediaRequestInfo{}, fmt.Errorf("request body must be a JSON object")
	}
	model := gjson.GetBytes(fields, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return GrokMediaRequestInfo{}, fmt.Errorf("model is required")
	}
	info := ParseGrokMediaRequest(contentType, body)
	if mediaType == "multipart/form-data" {
		_, params, _ := mime.ParseMediaType(contentType)
		reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for partIndex := 0; ; partIndex++ {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return info, err
			}
			name := part.FormName()
			if part.FileName() != "" && isVideoAPIImageField(name) {
				size, err := io.Copy(io.Discard, part)
				if err != nil {
					return info, err
				}
				info.Uploads = append(info.Uploads, OpenAIImagesUpload{FieldName: name, FileName: part.FileName(), ContentType: part.Header.Get("Content-Type"),
					source: &imageUploadSource{body: body, boundary: params["boundary"], part: partIndex, size: size}})
			}
			_ = part.Close()
		}
	}
	info.Model = strings.TrimSpace(model.String())
	info.DurationSeconds = videoAPIDuration(fields)
	info.Resolution = strings.ToLower(gjson.GetBytes(fields, "resolution").String())
	if info.Resolution == "" {
		info.Resolution = videoPixelResolution(gjson.GetBytes(fields, "size").String())
	}
	for _, field := range []string{"input_reference", "first_frame_image", "last_frame_image", "end_frame"} {
		if value := gjson.GetBytes(fields, field).String(); value != "" {
			info.InputImageURLs = append(info.InputImageURLs, value)
		}
	}
	// Callback settlement and client-chosen task identities are not supported.
	for _, field := range []string{"callback_url", "external_task_id"} {
		if gjson.GetBytes(fields, field).Exists() {
			return info, fmt.Errorf("%s is not supported; poll the task endpoint", field)
		}
	}
	return info, nil
}

func isVideoAPIImageField(name string) bool {
	switch name {
	case "input_reference", "first_frame_image", "last_frame_image", "end_frame":
		return true
	default:
		return name == "reference_images" || strings.HasPrefix(name, "reference_images[")
	}
}

func videoAPIDuration(fields []byte) int {
	value := gjson.GetBytes(fields, "seconds")
	if !value.Exists() {
		value = gjson.GetBytes(fields, "duration")
	}
	raw := strings.TrimSpace(value.String())
	if value.Type == gjson.Number {
		raw = value.Raw
	}
	n, _ := strconv.ParseFloat(strings.TrimSuffix(raw, "s"), 64)
	if n > 0 && n <= math.MaxInt32 {
		return int(math.Ceil(n))
	}
	return 0
}

// Unlike the native adapter, this entry point defers conversion until after
// scheduling so JSON extensions and multipart references can pass unchanged.
func PrepareVideoAPIRequest(c *gin.Context, platform string, body []byte) (GrokMediaEndpoint, []byte, string, error) {
	contentType := c.GetHeader("Content-Type")
	if targetModel, ok := ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
		var err error
		body, contentType, err = rewriteUnifiedVideoModel(body, contentType, targetModel)
		if err != nil {
			return "", nil, "", err
		}
	}
	if _, err := ParseVideoAPIRoutingRequest(contentType, body); err != nil {
		return "", nil, "", err
	}
	endpoint := map[string]GrokMediaEndpoint{PlatformSeedance: SeedanceEndpointCreate, PlatformWan: "wan_create", PlatformKling: "kling_text_create", PlatformSora: "sora_create"}[platform]
	if endpoint == "" {
		return "", nil, "", fmt.Errorf("unsupported video platform")
	}
	c.Set(videoUnifiedInputKey, unifiedVideoInput{body: body, contentType: contentType})
	nativeResolution, nativeErr := RequestVideoResolution(c, endpoint, body, contentType)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), videoAPINativeResolutionContextKey{}, videoAPINativeResolution{resolution: nativeResolution, err: nativeErr}))
	return endpoint, body, contentType, nil
}

func (s *OpenAIGatewayService) ForwardUnifiedVideoAPI(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskKey string, body []byte, contentType string) (*OpenAIForwardResult, error) {
	if endpoint.IsVideoAPIPassthrough() || (endpoint.IsGenerationRequest() && account.VideoAPIMode() == VideoAPIModePassthrough) {
		return s.forwardVideoAPIPassthrough(ctx, c, account, endpoint, taskKey, body, contentType, false)
	}
	if endpoint.IsGenerationRequest() {
		var err error
		endpoint, body, contentType, err = ConvertUnifiedVideoRequest(account.Platform, contentType, body)
		if err != nil {
			return nil, &GrokMediaInputValidationError{Reason: err.Error()}
		}
	}
	if endpoint.IsSeedance() {
		return s.ForwardSeedance(ctx, c, account, endpoint, taskKey, body)
	}
	return s.ForwardVideoProvider(ctx, c, account, endpoint, taskKey, body, contentType)
}

func buildVideoAPIPassthroughURL(base, id string, content bool) (string, error) {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	base += "/videos"
	if id != "" {
		if err := validateUpstreamPathSegment("video task ID", id); err != nil {
			return "", err
		}
		base += "/" + id
	}
	if content {
		base += "/content"
	}
	return base, nil
}

func (s *OpenAIGatewayService) forwardVideoAPIPassthrough(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskKey string, body []byte, contentType string, content bool) (*OpenAIForwardResult, error) {
	if taskKey == "" {
		if value, ok := c.Get(videoUnifiedInputKey); ok {
			if original, valid := value.(unifiedVideoInput); valid {
				body, contentType = original.body, original.contentType
			}
		}
	}
	base, err := s.validateUpstreamBaseURL(videoAccountBaseURL(account))
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(account.GetCredential("api_key"))
	if token == "" {
		return nil, fmt.Errorf("video passthrough account missing API key")
	}
	result := &OpenAIForwardResult{ResponseID: taskKey}
	method, id := http.MethodGet, ""
	passthroughEndpoint := videoAPIPassthroughStatusEndpoint(account.Platform)
	if taskKey == "" {
		info, err := ParseVideoAPIRoutingRequest(contentType, body)
		if err != nil {
			return nil, err
		}
		result.Model, result.BillingModel = info.Model, info.Model
		resolution := info.Resolution
		upstream, _, _, err := account.ResolveVideoModel(info.Model, resolution)
		if err != nil {
			return nil, err
		}
		result.UpstreamModel = upstream
		// A route default selects a model but does not assert the upstream's
		// output resolution. Only snapshot an explicit client billing unit.
		result.VideoResolution, result.VideoDurationSeconds = info.Resolution, info.DurationSeconds
		if upstream != info.Model {
			body, contentType, err = rewriteUnifiedVideoModel(body, contentType, upstream)
			if err != nil {
				return nil, err
			}
		}
		method = http.MethodPost
	} else {
		id = videoProviderTaskID(passthroughEndpoint, taskKey)
	}
	if content && c.Request.Method == http.MethodHead {
		method = http.MethodHead
	}
	target, err := buildVideoAPIPassthroughURL(base, id, content)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if content {
		req.Header.Set("Accept", "video/mp4")
		if value := c.GetHeader("Range"); value != "" {
			req.Header.Set("Range", value)
		}
	}
	account.ApplyHeaderOverrides(req.Context(), req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	result.Duration, result.ResponseHeaders = time.Since(started), resp.Header.Clone()
	if content && (resp.StatusCode == 200 || resp.StatusCode == 206 || resp.StatusCode == 416) {
		return result, writeGrokMediaContentResponse(c, resp)
	}
	data, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost && resp.StatusCode == http.StatusConflict &&
		(gjson.GetBytes(data, "error.code").String() == "submission_unconfirmed" || gjson.GetBytes(data, "error.type").String() == "submission_unconfirmed" || gjson.GetBytes(data, "code").String() == "submission_unconfirmed") {
		id := gjson.GetBytes(data, "data.task_id").String()
		if id == "" {
			writeGrokMediaResponse(c, resp, data, s.responseHeaderFilter)
			return nil, fmt.Errorf("unconfirmed video submission missing task ID; do not resubmit automatically")
		}
		if err := validateUpstreamPathSegment("video task ID", id); err != nil {
			return nil, err
		}
		result.ResponseID = VideoProviderTaskKey(passthroughEndpoint, id)
		data, err = sjson.SetBytes(data, "data.task_id", PublicVideoTaskID(result.ResponseID))
		if err != nil {
			return nil, err
		}
		writeProviderVideoResponse(c, resp, data, result, passthroughEndpoint, s.responseHeaderFilter)
		return result, nil
	}
	if resp.StatusCode >= 300 {
		writeGrokMediaResponse(c, resp, data, s.responseHeaderFilter)
		return nil, fmt.Errorf("video passthrough upstream status %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(data) || !gjson.ParseBytes(data).IsObject() {
		return nil, fmt.Errorf("invalid video upstream JSON response")
	}
	if method == http.MethodPost {
		id = gjson.GetBytes(data, "id").String()
		if id == "" {
			return nil, fmt.Errorf("video create response missing task ID")
		}
		if err := validateUpstreamPathSegment("video task ID", id); err != nil {
			return nil, err
		}
		result.ResponseID = VideoProviderTaskKey(passthroughEndpoint, id)
	} else {
		parseVideoAPIPassthroughCompletion(data, result)
	}
	applyVideoPending(c, result)
	writeProviderVideoResponse(c, resp, data, result, passthroughEndpoint, s.responseHeaderFilter)
	return result, nil
}

func parseVideoAPIPassthroughCompletion(data []byte, result *OpenAIForwardResult) {
	status := normalizeVideoAPIPassthroughStatus(gjson.GetBytes(data, "status").String())
	if status == "failed" {
		result.VideoFailed = true
		return
	}
	if status != "completed" {
		return
	}
	result.VideoCount = 1
	result.VideoDurationSeconds = videoAPIDuration(data)
	if !gjson.GetBytes(data, "duration").Exists() && !gjson.GetBytes(data, "seconds").Exists() {
		result.VideoDurationSeconds = 0
	}
	result.VideoResolution = strings.ToLower(gjson.GetBytes(data, "resolution").String())
	if result.VideoResolution == "" {
		result.VideoResolution = videoPixelResolution(gjson.GetBytes(data, "size").String())
	}
	result.Usage.OutputTokens = max(0, int(gjson.GetBytes(data, "usage.completion_tokens").Int()))
	if result.Usage.OutputTokens == 0 {
		result.Usage.OutputTokens = max(0, int(gjson.GetBytes(data, "usage.total_tokens").Int()))
	}
}

func videoAPIPassthroughContentURL(data []byte) string {
	for _, path := range []string{"url", "output.0.url"} {
		value := gjson.GetBytes(data, path).String()
		target, err := url.Parse(value)
		if err == nil && target.Scheme == "https" && target.Host != "" && target.User == nil && !isVideoAPIContentEndpoint(target.Path) {
			return value
		}
	}
	return ""
}

func isVideoAPIContentEndpoint(path string) bool {
	return strings.Contains(path, "/videos/") && strings.HasSuffix(strings.TrimRight(path, "/"), "/content")
}

func videoAPIPassthroughResponse(data []byte, result *OpenAIForwardResult) ([]byte, error) {
	data, err := sjson.SetBytes(data, "id", PublicVideoTaskID(result.ResponseID))
	if err != nil {
		return nil, err
	}
	if result.Model != "" {
		data, err = sjson.SetBytes(data, "model", result.Model)
	}
	if err == nil && !gjson.GetBytes(data, "object").Exists() {
		data, err = sjson.SetBytes(data, "object", "video")
	}
	if err == nil {
		data, err = sjson.SetBytes(data, "status", normalizeVideoAPIPassthroughStatus(gjson.GetBytes(data, "status").String()))
	}
	if err == nil && gjson.GetBytes(data, "status").String() == "completed" {
		data, err = sjson.SetBytes(data, "progress", 100)
	}
	// Public URLs, progress, media_id and vendor extensions are preserved.
	// Only providers without a direct URL need the authenticated content proxy.
	if err == nil && gjson.GetBytes(data, "status").String() == "completed" {
		if url := videoAPIPassthroughContentURL(data); url != "" {
			data, err = sjson.SetBytes(data, "url", url)
		} else {
			contentURL := "/v1/videos/" + PublicVideoTaskID(result.ResponseID) + "/content"
			data, err = sjson.SetBytes(data, "url", contentURL)
			if err == nil && gjson.GetBytes(data, "output.0.url").Exists() {
				data, err = sjson.SetBytes(data, "output.0.url", contentURL)
			}
		}
	}
	return data, err
}

func normalizeVideoAPIPassthroughStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "pending", "submitted":
		return "queued"
	case "running", "processing", "in_progress":
		return "in_progress"
	case "succeed", "succeeded", "complete", "completed":
		return "completed"
	case "failed", "cancelled", "canceled", "expired":
		return "failed"
	default:
		return "unknown"
	}
}
