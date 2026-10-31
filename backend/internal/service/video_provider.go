package service

// Native video protocols deliberately preserve vendor request/response fields.
// No retry is performed after submitting a potentially billable asynchronous job.
import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Descriptors are private; the existing media task store and scheduler are reused.
type videoProviderEndpoint struct {
	provider, path, version, modelField, idField string
	create, content                              bool
}

var videoProviderEndpoints = map[GrokMediaEndpoint]videoProviderEndpoint{
	"wan_create":         {"wan", "/services/aigc/video-generation/video-synthesis", "/api/v1", "model", "output.task_id", true, false},
	"wan_image_create":   {"wan", "/services/aigc/image2video/video-synthesis", "/api/v1", "model", "output.task_id", true, false},
	"wan_status":         {"wan", "/tasks", "/api/v1", "model", "output.task_id", false, false},
	"kling_text_create":  {"kling", "/videos/text2video", "/v1", "model_name", "data.task_id", true, false},
	"kling_image_create": {"kling", "/videos/image2video", "/v1", "model_name", "data.task_id", true, false},
	"kling_text_status":  {"kling", "/videos/text2video", "/v1", "model_name", "data.task_id", false, false},
	"kling_image_status": {"kling", "/videos/image2video", "/v1", "model_name", "data.task_id", false, false},
	"sora_create":        {"sora", "/videos", "/v1", "model", "id", true, false},
	"sora_status":        {"sora", "/videos", "/v1", "model", "id", false, false},
	"sora_content":       {"sora", "/videos", "/v1", "model", "id", false, true},
}

func (e GrokMediaEndpoint) VideoProvider() string       { return videoProviderEndpoints[e].provider }
func (e GrokMediaEndpoint) IsProviderVideoCreate() bool { return videoProviderEndpoints[e].create }

func VideoProviderTaskKey(endpoint GrokMediaEndpoint, id string) string {
	d := videoProviderEndpoints[endpoint]
	// Kling task IDs live in separate text/image collections.
	prefix := d.provider
	if d.provider == "kling" {
		prefix += ":" + strings.TrimPrefix(d.path, "/videos/")
	}
	return prefix + ":" + strings.TrimSpace(id)
}

func videoProviderTaskID(endpoint GrokMediaEndpoint, key string) string {
	return strings.TrimPrefix(key, VideoProviderTaskKey(endpoint, ""))
}

func ParseVideoProviderRequest(endpoint GrokMediaEndpoint, contentType string, body []byte) (GrokMediaRequestInfo, error) {
	return parseVideoProviderRequest(endpoint, contentType, body, false)
}

// Account selection may supply a configured default resolution later.
func ParseVideoProviderRoutingRequest(endpoint GrokMediaEndpoint, contentType string, body []byte) (GrokMediaRequestInfo, error) {
	return parseVideoProviderRequest(endpoint, contentType, body, true)
}

func parseVideoProviderRequest(endpoint GrokMediaEndpoint, contentType string, body []byte, allowMissingResolution bool) (GrokMediaRequestInfo, error) {
	d, ok := videoProviderEndpoints[endpoint]
	var info GrokMediaRequestInfo
	if !ok || !d.create {
		return info, fmt.Errorf("invalid video creation endpoint")
	}
	fields := body
	mediaType, params, err := mime.ParseMediaType(contentType)
	if contentType == "" {
		mediaType, err = "application/json", nil
	}
	if err != nil {
		return info, fmt.Errorf("invalid Content-Type")
	}
	if mediaType == "multipart/form-data" && d.provider == "sora" {
		fields, _, err = rewriteVideoMultipart(body, params["boundary"], "")
		if err != nil {
			return info, err
		}
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return info, err
			}
			if part.FileName() == "" {
				continue
			}
			if part.FormName() != "input_reference" || len(info.Uploads) > 0 {
				return info, fmt.Errorf("only one input_reference file is supported")
			}
			data, err := io.ReadAll(part)
			if err != nil {
				return info, err
			}
			info.Uploads = append(info.Uploads, OpenAIImagesUpload{FieldName: part.FormName(), FileName: part.FileName(), ContentType: part.Header.Get("Content-Type"), Data: data})
		}
	} else if mediaType != "application/json" {
		return info, fmt.Errorf("this video endpoint requires application/json")
	}
	if !gjson.ValidBytes(fields) || !gjson.ParseBytes(fields).IsObject() {
		return info, fmt.Errorf("request body must be a JSON object")
	}
	model := gjson.GetBytes(fields, d.modelField)
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return info, fmt.Errorf("%s is required", d.modelField)
	}
	info.Model = strings.TrimSpace(model.String())
	info.Prompt = gjson.GetBytes(fields, "prompt").String()
	// Callbacks and task references would bypass ownership and polling settlement.
	for _, field := range []string{"callback_url", "external_task_id"} {
		if gjson.GetBytes(fields, field).Exists() {
			return info, fmt.Errorf("%s is not supported; poll the task endpoint", field)
		}
	}
	duration, resolution := gjson.GetBytes(fields, "duration"), gjson.GetBytes(fields, "resolution").String()
	switch d.provider {
	case "wan":
		if !gjson.GetBytes(fields, "input").IsObject() {
			return info, fmt.Errorf("input must be an object")
		}
		info.Prompt = gjson.GetBytes(fields, "input.prompt").String()
		for _, field := range []string{"img_url", "image_url", "first_frame_url", "last_frame_url"} {
			if value := gjson.GetBytes(fields, "input."+field).String(); value != "" {
				info.InputImageURLs = append(info.InputImageURLs, value)
			}
		}
		duration = gjson.GetBytes(fields, "parameters.duration")
		resolution = gjson.GetBytes(fields, "parameters.resolution").String()
		if resolution == "" {
			resolution = videoPixelResolution(gjson.GetBytes(fields, "parameters.size").String())
		}
	case "kling":
		if value := gjson.GetBytes(fields, "image").String(); value != "" {
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "data:") {
				value = "data:image/png;base64," + value
			}
			info.InputImageURLs = append(info.InputImageURLs, value)
		}
		if resolution == "" {
			resolution = "720p"
			if gjson.GetBytes(fields, "mode").String() == "pro" {
				resolution = "1080p"
			}
		}
	case "sora":
		if value := gjson.GetBytes(fields, "input_reference").String(); value != "" {
			info.InputImageURLs = append(info.InputImageURLs, value)
		}
		duration = gjson.GetBytes(fields, "seconds")
		resolution, err = videoResolutionFromFields(fields, "resolution", "size")
		if err != nil {
			return info, err
		}
		if resolution == "" {
			resolution = "720p"
		}
	}
	info.DurationSeconds = int(duration.Int())
	if duration.Exists() && (duration.Float() != math.Trunc(duration.Float()) || duration.Float() > math.MaxInt32) {
		return info, fmt.Errorf("video duration must be an integer number of seconds")
	}
	if !duration.Exists() {
		info.DurationSeconds = 5
		if d.provider == "sora" {
			info.DurationSeconds = 4
		}
	}
	if info.DurationSeconds <= 0 {
		return info, fmt.Errorf("a positive video duration is required for billing")
	}
	if resolution == "" {
		if allowMissingResolution {
			return info, nil
		}
		return info, fmt.Errorf("an explicit video resolution or size is required for billing")
	}
	var valid bool
	info.Resolution, valid = lookupProviderVideoResolution(resolution)
	if !valid {
		return info, fmt.Errorf("unsupported video billing resolution: %s", resolution)
	}
	return info, nil
}

func videoPixelResolution(size string) string {
	var width, height int
	size = strings.ReplaceAll(size, "*", "x")
	if _, err := fmt.Sscanf(size, "%dx%d", &width, &height); err != nil || width <= 0 || height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dp", min(width, height))
}

// Read multipart without writing uploaded images to disk; preserve every part.
// When model is empty the returned bytes are just the scalar fields as JSON.
func rewriteVideoMultipart(body []byte, boundary, model string) ([]byte, string, error) {
	var updates map[string]string
	if model != "" {
		updates = map[string]string{"model": model}
	}
	return rewriteVideoMultipartFields(body, boundary, updates)
}

func rewriteVideoMultipartFields(body []byte, boundary string, updates map[string]string, removeFields ...string) ([]byte, string, error) {
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is required")
	}
	r := multipart.NewReader(bytes.NewReader(body), boundary)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fields := map[string]string{}
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("invalid video multipart body")
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return nil, "", err
		}
		if part.FileName() == "" {
			if _, exists := fields[part.FormName()]; exists {
				return nil, "", fmt.Errorf("duplicate multipart field: %s", part.FormName())
			}
			fields[part.FormName()] = string(data)
			remove := false
			for _, name := range removeFields {
				remove = remove || name == part.FormName()
			}
			if remove {
				continue
			}
			if value, replace := updates[part.FormName()]; replace {
				data = []byte(value)
			}
		}
		if updates != nil {
			out, err := w.CreatePart(part.Header)
			if err != nil {
				return nil, "", err
			}
			if _, err = out.Write(data); err != nil {
				return nil, "", err
			}
		}
	}
	for key, value := range updates {
		if _, exists := fields[key]; !exists {
			if err := w.WriteField(key, value); err != nil {
				return nil, "", err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	if updates == nil {
		data, err := json.Marshal(fields)
		return data, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func videoProviderToken(account *Account, provider string, now time.Time) (string, error) {
	key := strings.TrimSpace(account.GetCredential("api_key"))
	if provider == "kling" && (account.GetCredential("kling_access_key") != "" || account.GetCredential("kling_secret_key") != "") {
		key = account.GetCredential("kling_access_key") + "|" + account.GetCredential("kling_secret_key")
	}
	if key == "" {
		return "", fmt.Errorf("video account missing api_key")
	}
	if provider != "kling" || !strings.Contains(key, "|") {
		return key, nil
	}
	parts := strings.Split(key, "|")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", fmt.Errorf("Kling credentials must be access_key|secret_key")
	}
	claims, err := json.Marshal(map[string]any{"iss": strings.TrimSpace(parts[0]), "exp": now.Add(30 * time.Minute).Unix(), "nbf": now.Add(-5 * time.Second).Unix()})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(parts[1])))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func buildVideoProviderURL(base string, endpoint GrokMediaEndpoint, taskKey string) (string, error) {
	d, ok := videoProviderEndpoints[endpoint]
	if !ok {
		return "", fmt.Errorf("unknown video endpoint")
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, d.version) {
		base += d.version
	}
	base += d.path
	if !d.create {
		id := videoProviderTaskID(endpoint, taskKey)
		if id == "" {
			return "", fmt.Errorf("video task ID is required")
		}
		if err := validateUpstreamPathSegment("video task ID", id); err != nil {
			return "", err
		}
		base += "/" + id
	}
	if d.content {
		base += "/content"
	}
	return base, nil
}

// Require an operator-specified video tariff. Unknown video models must never
// silently inherit the historical image-price fallback used by the Grok path.
func (s *OpenAIGatewayService) VideoProviderPricingConfigured(ctx context.Context, key *APIKey, model string) bool {
	resolved := s.resolveOpenAIChannelPricing(ctx, model, key)
	return resolved != nil && (resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel) &&
		(resolved.Mode == BillingModeVideo || resolved.Mode == BillingModePerRequest)
}

// ProviderVideoBillingReady checks the units needed by the selected tariff before
// claiming a completed task. Missing token usage must remain retryable for Ark.
func (s *OpenAIGatewayService) ProviderVideoBillingReady(ctx context.Context, key *APIKey, result *OpenAIForwardResult) bool {
	if result == nil || result.VideoCount <= 0 {
		return false
	}
	resolved := s.resolveOpenAIChannelPricing(ctx, firstNonEmpty(result.BillingModel, result.Model), key)
	if resolved != nil && resolved.Mode == BillingModePerRequest {
		return true
	}
	if strings.HasPrefix(result.ResponseID, "seedance:") {
		return (resolved == nil || resolved.Mode == BillingModeToken) && result.Usage.OutputTokens > 0
	}
	return resolved != nil && resolved.Mode == BillingModeVideo && result.VideoDurationSeconds > 0 && result.VideoResolution != ""
}

func (s *OpenAIGatewayService) ForwardVideoProvider(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskKey string, body []byte, contentType string) (*OpenAIForwardResult, error) {
	d, ok := videoProviderEndpoints[endpoint]
	if !ok || !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapability(d.provider)) {
		return nil, fmt.Errorf("account does not support video endpoint")
	}
	base, err := s.validateUpstreamBaseURL(videoAccountBaseURL(account))
	if err != nil {
		return nil, err
	}
	started := time.Now()
	token, err := videoProviderToken(account, d.provider, started)
	if err != nil {
		return nil, err
	}
	result := &OpenAIForwardResult{ResponseID: taskKey}
	method := http.MethodGet
	if d.content && c.Request.Method == http.MethodHead {
		method = http.MethodHead
	}
	if d.create {
		info, err := ParseVideoProviderRoutingRequest(endpoint, contentType, body)
		if err != nil {
			return nil, err
		}
		result.Model, result.BillingModel = info.Model, info.Model
		endpoint, body, contentType, result.UpstreamModel, err = prepareVideoAccountModel(c, account, endpoint, &info, body, contentType)
		if err != nil {
			return nil, err
		}
		if info.Resolution == "" {
			return nil, fmt.Errorf("an explicit video resolution or size is required")
		}
		d = videoProviderEndpoints[endpoint]
		result.VideoResolution, result.VideoDurationSeconds = info.Resolution, info.DurationSeconds
		mediaType, params, _ := mime.ParseMediaType(contentType)
		if mediaType == "multipart/form-data" {
			body, contentType, err = rewriteVideoMultipart(body, params["boundary"], result.UpstreamModel)
		} else {
			body, err = sjson.SetBytes(body, d.modelField, result.UpstreamModel)
			contentType = "application/json"
		}
		if err != nil {
			return nil, err
		}
		method = http.MethodPost
	}
	target, err := buildVideoProviderURL(base, endpoint, taskKey)
	if err != nil {
		return nil, err
	}
	// Never follow a content redirect with an account's Authorization header.
	req, err := http.NewRequestWithContext(WithHTTPUpstreamRedirectsDisabled(ctx), method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if d.create {
		req.Header.Set("Content-Type", contentType)
	}
	if d.provider == "wan" && d.create {
		req.Header.Set("X-DashScope-Async", "enable")
		if c.GetHeader("X-DashScope-OssResourceResolve") == "enable" {
			req.Header.Set("X-DashScope-OssResourceResolve", "enable")
		}
	}
	if d.content {
		req.Header.Set("Accept", "video/mp4")
		if value := c.GetHeader("Range"); value != "" {
			req.Header.Set("Range", value)
		}
	}
	account.ApplyHeaderOverrides(req.Header)
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
	if d.content && (resp.StatusCode == 200 || resp.StatusCode == 206 || resp.StatusCode == 416) {
		return result, writeGrokMediaContentResponse(c, resp)
	}
	data, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		writeGrokMediaResponse(c, resp, data, s.responseHeaderFilter)
		return nil, fmt.Errorf("%s video upstream status %d", d.provider, resp.StatusCode)
	}
	if !gjson.ValidBytes(data) {
		return nil, fmt.Errorf("invalid video upstream JSON response")
	}
	if (d.provider == "kling" && gjson.GetBytes(data, "code").Int() != 0) || (d.provider == "wan" && gjson.GetBytes(data, "code").String() != "") {
		writeGrokMediaResponse(c, resp, data, s.responseHeaderFilter)
		return nil, fmt.Errorf("%s video upstream rejected request", d.provider)
	}
	if d.create {
		id := gjson.GetBytes(data, d.idField).String()
		if id == "" {
			return nil, fmt.Errorf("video create response missing task ID")
		}
		if err := validateUpstreamPathSegment("video task ID", id); err != nil {
			return nil, err
		}
		result.ResponseID = VideoProviderTaskKey(endpoint, id)
	} else {
		parseVideoProviderCompletion(d.provider, data, result)
	}
	applyVideoPending(c, result)
	writeProviderVideoResponse(c, resp, data, result, endpoint, s.responseHeaderFilter)
	return result, nil
}

func parseVideoProviderCompletion(provider string, data []byte, result *OpenAIForwardResult) {
	var done bool
	var seconds gjson.Result
	switch provider {
	case "wan":
		done = gjson.GetBytes(data, "output.task_status").String() == "SUCCEEDED"
		seconds = gjson.GetBytes(data, "usage.duration")
		if !seconds.Exists() {
			seconds = gjson.GetBytes(data, "usage.output_video_duration")
		}
		if !seconds.Exists() {
			seconds = gjson.GetBytes(data, "output.duration")
		}
		if input := gjson.GetBytes(data, "usage.input_video_duration"); input.Float() > 0 && seconds.Float() > 0 {
			result.VideoDurationSeconds = int(math.Ceil(input.Float() + seconds.Float()))
		}
		result.VideoResolution, _ = lookupProviderVideoResolution(gjson.GetBytes(data, "usage.SR").String())
	case "kling":
		done = gjson.GetBytes(data, "data.task_status").String() == "succeed"
		seconds = gjson.GetBytes(data, "data.task_result.videos.0.duration")
	case "sora":
		done = gjson.GetBytes(data, "status").String() == "completed"
		seconds = gjson.GetBytes(data, "seconds")
		result.VideoResolution = videoPixelResolution(gjson.GetBytes(data, "size").String())
	}
	if !done {
		return
	}
	result.VideoCount = 1
	if result.VideoDurationSeconds == 0 && seconds.Float() > 0 {
		result.VideoDurationSeconds = int(math.Ceil(seconds.Float()))
	}
}
