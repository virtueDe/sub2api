package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const VideoUnifiedContextKey = "video_unified"
const VideoCaptureResponseContextKey = "video_capture_response"
const VideoPendingContextKey = "video_pending_billing"
const videoCapturedResponseKey = "video_captured_response"
const videoUnifiedInputKey = "video_unified_input"

type unifiedVideoInput struct {
	body        []byte
	contentType string
}

func rewriteUnifiedVideoModel(body []byte, contentType, model string) ([]byte, string, error) {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		return rewriteVideoMultipart(body, params["boundary"], model)
	}
	body, err := sjson.SetBytes(body, "model", model)
	return body, contentType, err
}

// Keep the common request until an account is selected: Wan chooses its native
// endpoint and image fields from the final mapped model, including account aliases.
func PrepareUnifiedVideoRequest(c *gin.Context, platform string, body []byte) (GrokMediaEndpoint, []byte, string, error) {
	contentType := c.GetHeader("Content-Type")
	if model, ok := ResolvedUpstreamModelFromContext(c.Request.Context()); ok {
		var err error
		body, contentType, err = rewriteUnifiedVideoModel(body, contentType, model)
		if err != nil {
			return "", nil, "", err
		}
	}
	endpoint, native, nativeType, err := ConvertUnifiedVideoRequest(platform, contentType, body)
	if err == nil {
		c.Set(videoUnifiedInputKey, unifiedVideoInput{body: body, contentType: contentType})
	}
	return endpoint, native, nativeType, err
}

type capturedVideoResponse struct {
	response *http.Response
	body     []byte
}

func applyVideoPending(c *gin.Context, result *OpenAIForwardResult) {
	value, ok := c.Get(VideoPendingContextKey)
	if !ok {
		return
	}
	pending, ok := value.(*GrokVideoPendingBilling)
	if !ok || pending == nil {
		return
	}
	result.Model, result.BillingModel = pending.Model, pending.BillingModel
	if pending.UpstreamModel != "" {
		result.UpstreamModel = pending.UpstreamModel
	}
	if result.VideoResolution == "" {
		result.VideoResolution = pending.VideoResolution
	}
	if result.VideoDurationSeconds <= 0 {
		result.VideoDurationSeconds = pending.VideoDurationSeconds
	}
}

func PublicVideoTaskID(taskKey string) string {
	return "video_" + base64.RawURLEncoding.EncodeToString([]byte(taskKey))
}

func ParsePublicVideoTaskID(id string) (GrokMediaEndpoint, string, error) {
	if !strings.HasPrefix(id, "video_") || len(id) > 1024 {
		return "", "", fmt.Errorf("invalid video ID")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "video_"))
	if err != nil {
		return "", "", fmt.Errorf("invalid video ID")
	}
	key := string(decoded)
	for prefix, endpoint := range map[string]GrokMediaEndpoint{
		"seedance:": SeedanceEndpointStatus, "wan:": "wan_status", "kling:text2video:": "kling_text_status", "kling:image2video:": "kling_image_status", "sora:": "sora_status",
	} {
		if strings.HasPrefix(key, prefix) {
			raw := strings.TrimPrefix(key, prefix)
			if raw == "" {
				break
			}
			if err := validateUpstreamPathSegment("video ID", raw); err != nil {
				break
			}
			return endpoint, key, nil
		}
	}
	return "", "", fmt.Errorf("invalid video ID")
}

// ConvertUnifiedVideoRequest maps the common fields and allows native extensions
// in metadata. Provider identity and the routed model cannot be overridden there.
func ConvertUnifiedVideoRequest(platform, contentType string, body []byte) (GrokMediaEndpoint, []byte, string, error) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if contentType == "" {
		mediaType, err = "application/json", nil
	}
	if err != nil {
		return "", nil, "", fmt.Errorf("invalid Content-Type")
	}
	fields := body
	var upload []byte
	var uploadType string
	if mediaType == "multipart/form-data" {
		fields, _, err = rewriteVideoMultipart(body, params["boundary"], "")
		if err != nil {
			return "", nil, "", err
		}
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			part, readErr := r.NextPart()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return "", nil, "", readErr
			}
			if part.FileName() == "" {
				continue
			}
			if part.FormName() != "input_reference" || upload != nil {
				return "", nil, "", fmt.Errorf("only one input_reference file is supported")
			}
			upload, err = io.ReadAll(part)
			if err != nil {
				return "", nil, "", err
			}
			uploadType = part.Header.Get("Content-Type")
		}
	} else if mediaType != "application/json" {
		return "", nil, "", fmt.Errorf("video requests require JSON or multipart/form-data")
	}
	if !gjson.ValidBytes(fields) || !gjson.ParseBytes(fields).IsObject() {
		return "", nil, "", fmt.Errorf("request body must be a JSON object")
	}
	model := gjson.GetBytes(fields, "model")
	if model.Type != gjson.String || strings.TrimSpace(model.String()) == "" {
		return "", nil, "", fmt.Errorf("model is required")
	}
	resolution, err := videoResolutionFromFields(fields, "resolution", "size")
	if err != nil {
		return "", nil, "", err
	}
	size := gjson.GetBytes(fields, "size").String()
	aspectRatio := videoAspectRatio(size)
	if size == "" && resolution != "" {
		size = defaultVideoSize(resolution)
		aspectRatio = "16:9"
	}
	if platform == PlatformSora {
		updates := map[string]string{}
		if size != "" && !gjson.GetBytes(fields, "size").Exists() {
			updates["size"] = size
		}
		if gjson.GetBytes(fields, "resolution").Exists() {
			body, contentType, err = setVideoRequestFields(body, contentType, updates, "resolution")
		}
		return "sora_create", body, contentType, err
	}
	metadata := gjson.GetBytes(fields, "metadata")
	if mediaType == "multipart/form-data" && metadata.Type == gjson.String {
		metadata = gjson.Parse(metadata.String())
	}
	native := map[string]any{}
	if metadata.Exists() {
		if !metadata.IsObject() {
			return "", nil, "", fmt.Errorf("metadata must be an object")
		}
		if err := json.Unmarshal([]byte(metadata.Raw), &native); err != nil {
			return "", nil, "", err
		}
	}
	prompt := gjson.GetBytes(fields, "prompt").String()
	seconds := gjson.GetBytes(fields, "seconds")
	if !seconds.Exists() {
		seconds = gjson.GetBytes(fields, "duration")
	}
	if seconds.Exists() && (seconds.Float() <= 0 || seconds.Float() != math.Trunc(seconds.Float()) || seconds.Float() > math.MaxInt32) {
		return "", nil, "", fmt.Errorf("video duration must be a positive integer number of seconds")
	}
	image := gjson.GetBytes(fields, "input_reference").String()
	if image == "" {
		image = gjson.GetBytes(fields, "image").String()
	}
	if image == "" {
		image = gjson.GetBytes(fields, "images.0").String()
	}
	if upload != nil {
		if platform != PlatformKling {
			return "", nil, "", fmt.Errorf("this provider requires URL references; file uploads are unsupported")
		}
		if !strings.HasPrefix(uploadType, "image/") && uploadType != "application/octet-stream" {
			return "", nil, "", fmt.Errorf("input_reference must be an image")
		}
		image = base64.StdEncoding.EncodeToString(upload)
	}
	endpoint := GrokMediaEndpoint("")
	switch platform {
	case PlatformSeedance:
		endpoint = SeedanceEndpointCreate
		native["model"] = model.String()
		content := []any{}
		if existing, ok := native["content"].([]any); ok {
			content = append(content, existing...)
		}
		if image != "" {
			content = append(content, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}, "role": "first_frame"})
		}
		if prompt != "" {
			content = append(content, map[string]string{"type": "text", "text": prompt})
		}
		native["content"] = content
		if seconds.Exists() {
			native["duration"] = seconds.Int()
		}
		if size != "" {
			native["resolution"] = videoPixelResolution(size)
			native["ratio"] = aspectRatio
		}
	case PlatformWan:
		endpoint = "wan_create"
		native["model"] = model.String()
		input, _ := native["input"].(map[string]any)
		if input == nil {
			input = map[string]any{}
		}
		parameters, _ := native["parameters"].(map[string]any)
		if parameters == nil {
			parameters = map[string]any{}
		}
		if prompt != "" {
			input["prompt"] = prompt
		}
		if image != "" {
			input["img_url"] = image
		}
		if strings.Contains(model.String(), "kf2v") || strings.Contains(model.String(), "s2v") {
			endpoint = "wan_image_create"
			if image != "" {
				delete(input, "img_url")
				if strings.Contains(model.String(), "kf2v") {
					input["first_frame_url"] = image
				} else {
					input["image_url"] = image
				}
			}
		}
		if seconds.Exists() {
			parameters["duration"] = seconds.Int()
		}
		if size != "" {
			setWanVideoSize(parameters, model.String(), size)
		}
		native["input"], native["parameters"] = input, parameters
	case PlatformKling:
		endpoint = "kling_text_create"
		native["model_name"] = model.String()
		if resolution != "" {
			native["resolution"] = resolution
		}
		if prompt != "" {
			native["prompt"] = prompt
		}
		if image != "" {
			native["image"] = image
			endpoint = "kling_image_create"
		}
		if _, ok := native["image"]; ok {
			endpoint = "kling_image_create"
		}
		if seconds.Exists() {
			native["duration"] = strconv.FormatInt(seconds.Int(), 10)
		}
		if size != "" && endpoint == "kling_text_create" {
			native["aspect_ratio"] = aspectRatio
		}
	default:
		return "", nil, "", fmt.Errorf("unsupported video platform")
	}
	encoded, err := json.Marshal(native)
	return endpoint, encoded, "application/json", err
}

func setWanVideoSize(parameters map[string]any, model, size string) {
	if strings.Contains(model, "t2v") {
		delete(parameters, "resolution")
		parameters["size"] = strings.ReplaceAll(size, "x", "*")
	} else {
		delete(parameters, "size")
		parameters["resolution"] = strings.ToUpper(videoPixelResolution(size))
	}
}

func videoAspectRatio(size string) string {
	var w, h int
	if _, err := fmt.Sscanf(strings.ReplaceAll(size, "*", "x"), "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
		return ""
	}
	a, b := w, h
	for b != 0 {
		a, b = b, a%b
	}
	return fmt.Sprintf("%d:%d", w/a, h/a)
}

func writeProviderVideoResponse(c *gin.Context, resp *http.Response, data []byte, result *OpenAIForwardResult, endpoint GrokMediaEndpoint, filter *responseheaders.CompiledHeaderFilter) {
	publicModel := result.Model
	if value, ok := c.Get(VideoPendingContextKey); ok {
		if pending, valid := value.(*GrokVideoPendingBilling); valid && pending.OriginalModel != "" {
			publicModel = pending.OriginalModel
		}
	} else if name, ok := RequestedPublicModelFromContext(c.Request.Context()); ok {
		publicModel = name
	}
	if c.GetBool(VideoUnifiedContextKey) {
		publicResult := *result
		publicResult.Model = publicModel
		data, _ = json.Marshal(unifiedVideoResponse(endpoint, data, &publicResult))
		resp.Header.Set("Content-Type", "application/json")
	} else if publicModel != "" {
		for _, path := range []string{"model", "model_name", "data.model", "data.model_name"} {
			if gjson.GetBytes(data, path).Exists() {
				if rewritten, err := sjson.SetBytes(data, path, publicModel); err == nil {
					data = rewritten
				}
			}
		}
	}
	if c.GetBool(VideoCaptureResponseContextKey) {
		c.Set(videoCapturedResponseKey, capturedVideoResponse{response: resp, body: data})
		return
	}
	writeGrokMediaResponse(c, resp, data, filter)
}

func (s *OpenAIGatewayService) FlushVideoResponse(c *gin.Context) {
	if value, exists := c.Get(videoCapturedResponseKey); exists {
		pending := value.(capturedVideoResponse)
		writeGrokMediaResponse(c, pending.response, pending.body, s.responseHeaderFilter)
	}
}

func unifiedVideoResponse(endpoint GrokMediaEndpoint, data []byte, result *OpenAIForwardResult) map[string]any {
	provider := endpoint.VideoProvider()
	if endpoint.IsSeedance() {
		provider = PlatformSeedance
	}
	status, errorValue, created, videoURL := "queued", gjson.Result{}, gjson.Result{}, ""
	switch provider {
	case PlatformSeedance:
		status = gjson.GetBytes(data, "status").String()
		errorValue = gjson.GetBytes(data, "error")
		created = gjson.GetBytes(data, "created_at")
		videoURL = gjson.GetBytes(data, "content.video_url").String()
	case PlatformWan:
		status = gjson.GetBytes(data, "output.task_status").String()
		errorValue = gjson.GetBytes(data, "output.message")
		videoURL = gjson.GetBytes(data, "output.video_url").String()
		if videoURL == "" {
			videoURL = gjson.GetBytes(data, "output.results.video_url").String()
		}
	case PlatformKling:
		status = gjson.GetBytes(data, "data.task_status").String()
		errorValue = gjson.GetBytes(data, "data.task_status_msg")
		created = gjson.GetBytes(data, "data.created_at")
		videoURL = gjson.GetBytes(data, "data.task_result.videos.0.url").String()
	case PlatformSora:
		status = gjson.GetBytes(data, "status").String()
		errorValue = gjson.GetBytes(data, "error")
		created = gjson.GetBytes(data, "created_at")
	}
	switch strings.ToLower(status) {
	case "", "queued", "pending", "submitted":
		status = "queued"
	case "running", "processing", "in_progress":
		status = "in_progress"
	case "succeeded", "succeed", "completed":
		status = "completed"
	case "failed", "cancelled", "canceled", "expired":
		status = "failed"
	default:
		status = "unknown"
	}
	progress := 0
	if status == "in_progress" {
		progress = 50
	}
	if status == "completed" {
		progress = 100
	}
	id := PublicVideoTaskID(result.ResponseID)
	out := map[string]any{"id": id, "object": "video", "status": status, "progress": progress}
	if result.Model != "" {
		out["model"] = result.Model
	}
	if result.VideoDurationSeconds > 0 {
		out["seconds"] = strconv.Itoa(result.VideoDurationSeconds)
	}
	if result.VideoResolution != "" {
		out["resolution"] = result.VideoResolution
	}
	if created.Int() > 0 {
		value := created.Int()
		if value > 1e12 {
			value /= 1000
		}
		out["created_at"] = value
	} else if endpoint.IsGenerationRequest() {
		out["created_at"] = time.Now().Unix()
	}
	if status == "failed" && errorValue.Exists() {
		if errorValue.IsObject() {
			out["error"] = errorValue.Value()
		} else {
			out["error"] = map[string]string{"message": errorValue.String()}
		}
	}
	if status == "completed" {
		out["url"] = "/v1/videos/" + id + "/content"
		if videoURL != "" {
			out["metadata"] = map[string]string{"video_url": videoURL}
		}
	}
	return out
}
