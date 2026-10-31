package service

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const VideoModelRoutesCredential = "video_model_routes"

// VideoModelRoute maps a public name to private upstream models. Only exact
// names are accepted so model listing and request admission have the same scope.
type VideoModelRoute struct {
	DefaultResolution string            `json:"default_resolution"`
	Resolutions       map[string]string `json:"resolutions"`
}

func (a *Account) VideoModelRoutes() (map[string]VideoModelRoute, error) {
	if a == nil || a.Credentials[VideoModelRoutesCredential] == nil {
		return nil, nil
	}
	data, err := json.Marshal(a.Credentials[VideoModelRoutesCredential])
	if err != nil {
		return nil, fmt.Errorf("invalid video model routes")
	}
	var routes map[string]VideoModelRoute
	if err = json.Unmarshal(data, &routes); err != nil {
		return nil, fmt.Errorf("invalid video model routes")
	}
	return routes, nil
}

func validateVideoModelRoutes(platform string, credentials map[string]any) error {
	a := &Account{Platform: platform, Credentials: credentials}
	routes, err := a.VideoModelRoutes()
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}
	if !IsVideoPlatform(platform) {
		return fmt.Errorf("resolution model routes require a video platform")
	}
	for model, route := range routes {
		if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model || strings.Contains(model, "*") {
			return fmt.Errorf("video public model must be a non-empty exact name")
		}
		if len(route.Resolutions) == 0 {
			return fmt.Errorf("video model %s requires resolution targets", model)
		}
		if _, ok := route.Resolutions[route.DefaultResolution]; !ok {
			return fmt.Errorf("video model %s default resolution must have a target", model)
		}
		for resolution, target := range route.Resolutions {
			normalized, ok := lookupProviderVideoResolution(resolution)
			if !ok || normalized != resolution {
				return fmt.Errorf("invalid video route resolution: %s", resolution)
			}
			if strings.TrimSpace(target) == "" || strings.TrimSpace(target) != target || strings.Contains(target, "*") {
				return fmt.Errorf("video target must be a non-empty exact model name")
			}
			if _, public := routes[target]; public {
				return fmt.Errorf("video target %s cannot also be a public route name", target)
			}
		}
		if target, ok := stringMappingFromRaw(credentials["model_mapping"])[model]; ok && target != model {
			return fmt.Errorf("video public model %s conflicts with an ordinary model mapping", model)
		}
	}
	return nil
}

// Effective catalog: publish route names and ordinary mappings, but do not
// publish private targets imported by the upstream model sync button.
func videoRouteModelMapping(routes map[string]VideoModelRoute, mapping map[string]string) map[string]string {
	private := map[string]bool{}
	for _, route := range routes {
		for _, target := range route.Resolutions {
			private[target] = true
		}
	}
	result := make(map[string]string, len(mapping)+len(routes))
	for name, target := range mapping {
		if !private[name] {
			result[name] = target
		}
	}
	for name := range routes {
		result[name] = name
	}
	return result
}

func (a *Account) ResolveVideoModel(model, resolution string) (upstream, effectiveResolution string, routed bool, err error) {
	routes, err := a.VideoModelRoutes()
	if err != nil {
		return "", "", false, err
	}
	route, ok := routes[model]
	if !ok {
		if videoRoutesHideModel(routes, model) {
			return "", "", false, fmt.Errorf("use the public video model name")
		}
		return a.GetMappedModel(model), resolution, false, nil
	}
	if resolution == "" {
		resolution = route.DefaultResolution
	}
	target, ok := route.Resolutions[resolution]
	if !ok || target == "" {
		return "", "", true, fmt.Errorf("model %s does not support resolution %s", model, resolution)
	}
	return target, resolution, true, nil
}

func videoRoutesHideModel(routes map[string]VideoModelRoute, model string) bool {
	for _, route := range routes {
		for _, target := range route.Resolutions {
			if model == target {
				return true
			}
		}
	}
	return false
}

type videoResolutionContextKey struct{}

// Presence distinguishes an omitted client resolution from a provider default.
func WithVideoRequestedResolution(ctx context.Context, resolution string) context.Context {
	return context.WithValue(ctx, videoResolutionContextKey{}, resolution)
}

func videoResolutionCompatible(ctx context.Context, account *Account, model string) bool {
	resolution, present := ctx.Value(videoResolutionContextKey{}).(string)
	if !present {
		return true
	} // Lookup tasks never re-evaluate current routes.
	_, _, _, err := account.ResolveVideoModel(model, resolution)
	return err == nil
}

func ValidateSelectedVideoResolution(ctx context.Context, account *Account, info GrokMediaRequestInfo, seedance bool) error {
	resolution, _ := ctx.Value(videoResolutionContextKey{}).(string)
	_, effective, routed, err := account.ResolveVideoModel(info.Model, resolution)
	if err != nil {
		return err
	}
	if !seedance && !routed && info.Resolution == "" {
		return fmt.Errorf("an explicit video resolution or size is required")
	}
	if routed && effective == "" {
		return fmt.Errorf("a default video resolution is required")
	}
	return nil
}

// Classify an unsupported tier separately from temporary capacity exhaustion.
func (s *OpenAIGatewayService) VideoResolutionRoutingError(ctx context.Context, groupID *int64, platform, model string) error {
	resolution, present := ctx.Value(videoResolutionContextKey{}).(string)
	if !present || s.accountRepo == nil {
		return nil
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, groupID, []string{platform}, false)
	if err != nil {
		return nil
	}
	var rejected error
	for i := range accounts {
		account := &accounts[i]
		if !account.IsModelSupported(model) {
			continue
		}
		_, _, _, routeErr := account.ResolveVideoModel(model, resolution)
		if routeErr == nil {
			return nil
		}
		rejected = routeErr
	}
	return rejected
}

func videoRequestFields(body []byte, contentType string) ([]byte, error) {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		fields, _, err := rewriteVideoMultipart(body, params["boundary"], "")
		return fields, err
	}
	return body, nil
}

func videoResolutionFromFields(fields []byte, resolutionPath, sizePath string) (string, error) {
	resolution, size := gjson.GetBytes(fields, resolutionPath), gjson.GetBytes(fields, sizePath)
	value := ""
	if resolution.Exists() {
		var valid bool
		value, valid = lookupProviderVideoResolution(resolution.String())
		if resolution.Type != gjson.String || !valid {
			return "", fmt.Errorf("unsupported video resolution: %s", resolution.String())
		}
	}
	if size.Exists() {
		fromSize := videoPixelResolution(size.String())
		if _, valid := lookupProviderVideoResolution(fromSize); size.Type != gjson.String || !valid {
			return "", fmt.Errorf("invalid video size")
		}
		if value != "" && value != fromSize {
			return "", fmt.Errorf("video resolution and size must agree")
		}
		value = fromSize
	}
	return value, nil
}

// RequestVideoResolution reads the original unified request when available,
// before conversion can insert provider defaults or rename fields.
func RequestVideoResolution(c *gin.Context, endpoint GrokMediaEndpoint, body []byte, contentType string) (string, error) {
	unified := false
	if value, exists := c.Get(videoUnifiedInputKey); exists {
		input := value.(unifiedVideoInput)
		body, contentType, unified = input.body, input.contentType, true
	}
	fields, err := videoRequestFields(body, contentType)
	if err != nil {
		return "", err
	}
	resolutionPath, sizePath := "resolution", "size"
	if !unified && endpoint.VideoProvider() == PlatformWan {
		resolutionPath, sizePath = "parameters.resolution", "parameters.size"
	}
	resolution, err := videoResolutionFromFields(fields, resolutionPath, sizePath)
	if err != nil || resolution != "" {
		return resolution, err
	}
	// Preserve explicit provider-specific parameters supplied via metadata too.
	if unified {
		metadata := gjson.GetBytes(fields, "metadata")
		if metadata.Type == gjson.String {
			metadata = gjson.Parse(metadata.String())
		}
		fields = []byte(metadata.Raw)
		if endpoint.VideoProvider() == PlatformWan {
			resolutionPath, sizePath = "parameters.resolution", "parameters.size"
		}
		resolution, err = videoResolutionFromFields(fields, resolutionPath, sizePath)
		if err != nil || resolution != "" {
			return resolution, err
		}
	}
	if endpoint.VideoProvider() == PlatformKling {
		switch gjson.GetBytes(fields, "mode").String() {
		case "pro":
			return "1080p", nil
		case "std":
			return "720p", nil
		}
	}
	return "", nil
}

func defaultVideoSize(resolution string) string {
	var height int
	if _, err := fmt.Sscanf(resolution, "%dp", &height); err != nil || height <= 0 {
		return ""
	}
	width := (height*16/9 + 1) / 2 * 2
	return fmt.Sprintf("%dx%d", width, height)
}

// Updates scalar fields without altering uploaded references or other parts.
func setVideoRequestFields(body []byte, contentType string, fields map[string]string, removeFields ...string) ([]byte, string, error) {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if mediaType == "multipart/form-data" {
		return rewriteVideoMultipartFields(body, params["boundary"], fields, removeFields...)
	}
	var err error
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		body, err = sjson.SetBytes(body, key, fields[key])
		if err != nil {
			return nil, "", err
		}
	}
	for _, key := range removeFields {
		body, err = sjson.DeleteBytes(body, key)
		if err != nil {
			return nil, "", err
		}
	}
	return body, contentType, nil
}

func prepareVideoAccountModel(c *gin.Context, account *Account, endpoint GrokMediaEndpoint, info *GrokMediaRequestInfo, body []byte, contentType string) (GrokMediaEndpoint, []byte, string, string, error) {
	resolution, err := RequestVideoResolution(c, endpoint, body, contentType)
	if err != nil {
		return endpoint, nil, "", "", err
	}
	upstream, resolved, routed, err := account.ResolveVideoModel(info.Model, resolution)
	if err != nil {
		return endpoint, nil, "", "", err
	}
	if routed {
		info.Resolution = resolved
	}
	if original, exists := c.Get(videoUnifiedInputKey); exists {
		input := original.(unifiedVideoInput)
		updates := map[string]string{"model": upstream}
		if routed && resolution == "" {
			updates["resolution"] = resolved
		}
		mapped, mappedType, err := setVideoRequestFields(input.body, input.contentType, updates)
		if err != nil {
			return endpoint, nil, "", "", err
		}
		provider := endpoint.VideoProvider()
		if endpoint.IsSeedance() {
			provider = PlatformSeedance
		}
		endpoint, body, contentType, err = ConvertUnifiedVideoRequest(provider, mappedType, mapped)
		return endpoint, body, contentType, upstream, err
	}
	if routed {
		if endpoint.VideoProvider() == PlatformWan {
			var native map[string]any
			if err := json.Unmarshal(body, &native); err != nil {
				return endpoint, nil, "", "", err
			}
			parameters, _ := native["parameters"].(map[string]any)
			if parameters == nil {
				parameters = map[string]any{}
			}
			size, _ := parameters["size"].(string)
			if size == "" {
				size = defaultVideoSize(resolved)
			}
			setWanVideoSize(parameters, upstream, size)
			native["parameters"] = parameters
			body, err = json.Marshal(native)
			return endpoint, body, contentType, upstream, err
		}
		updates := map[string]string{"resolution": resolved}
		if endpoint.VideoProvider() == PlatformSora {
			updates = map[string]string{}
			fields, readErr := videoRequestFields(body, contentType)
			if readErr != nil {
				return endpoint, nil, "", "", readErr
			}
			if !gjson.GetBytes(fields, "size").Exists() {
				updates["size"] = defaultVideoSize(resolved)
			}
			body, contentType, err = setVideoRequestFields(body, contentType, updates, "resolution")
			return endpoint, body, contentType, upstream, err
		}
		body, contentType, err = setVideoRequestFields(body, contentType, updates)
	}
	return endpoint, body, contentType, upstream, err
}
