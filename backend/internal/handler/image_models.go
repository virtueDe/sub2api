package handler

import (
	"net/http"
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ImageModels lists only image-capable models visible to the authenticated
// API key's group. It intentionally does not expose the full chat model list.
func (h *GatewayHandler) ImageModels(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"type": "authentication_error", "message": "Invalid API key"}})
		return
	}
	platform := ""
	var groupID *int64
	if apiKey.Group != nil {
		platform = apiKey.Group.Platform
		groupID = &apiKey.Group.ID
	}
	if forced, exists := middleware2.GetForcePlatformFromContext(c); exists && strings.TrimSpace(forced) != "" {
		platform = forced
	}
	if h == nil || h.gatewayService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "api_error", "message": "model service is unavailable"}})
		return
	}
	available := h.gatewayService.GetAvailableModels(c.Request.Context(), groupID, platform)
	if len(available) == 0 {
		available = defaultModelIDsForPlatform(platform)
	}
	filtered := make([]string, 0, len(available))
	seen := make(map[string]struct{}, len(available))
	for _, model := range available {
		if isImageModelForPlatform(platform, model) {
			model = normalizeImageModelID(platform, model)
			key := strings.ToLower(model)
			if model != "" {
				if _, exists := seen[key]; !exists {
					seen[key] = struct{}{}
					filtered = append(filtered, model)
				}
			}
		}
	}
	if apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
		filtered = apiKey.Group.ModelAllowlist.FilterForListing(filtered)
	}
	// This is a dedicated OpenAI-compatible image endpoint, so keep the
	// response envelope stable across Gemini, Grok and OpenAI groups.
	writeOpenAIModelsList(c, filtered)
}

func normalizeImageModelID(platform, model string) string {
	model = strings.TrimSpace(model)
	if platform == service.PlatformGemini || platform == service.PlatformAntigravity {
		model = strings.TrimPrefix(strings.TrimPrefix(model, "models/"), "Models/")
	}
	return model
}

func isImageModelForPlatform(platform, model string) bool {
	model = strings.TrimSpace(model)
	switch platform {
	case service.PlatformOpenAI:
		return service.IsGPTImageGenerationModel(model)
	case service.PlatformGrok:
		lower := strings.ToLower(model)
		return strings.HasPrefix(lower, "grok-imagine")
	case service.PlatformGemini, service.PlatformAntigravity:
		return service.IsGeminiImageGenerationModel(model)
	case service.PlatformComposite:
		return service.IsGPTImageGenerationModel(model) || strings.HasPrefix(strings.ToLower(model), "grok-imagine") || service.IsGeminiImageGenerationModel(model)
	default:
		return false
	}
}
