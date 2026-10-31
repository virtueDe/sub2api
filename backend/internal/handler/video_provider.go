package handler

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/requestmodel"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func videoGroupPlatform(c *gin.Context) string {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key.Group == nil {
		return ""
	}
	if key.Group.Platform == service.PlatformComposite {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok {
			return platform
		}
	}
	return key.Group.Platform
}

func (h *OpenAIGatewayHandler) NativeVideoTasks(endpoint service.GrokMediaEndpoint) gin.HandlerFunc {
	return func(c *gin.Context) {
		platform := videoGroupPlatform(c)
		if platform != endpoint.VideoProvider() && platform != service.PlatformComposite {
			h.errorResponse(c, http.StatusForbidden, "permission_error", "Video endpoint does not match the group platform")
			return
		}
		key := ""
		if endpoint.IsVideoLookupRequest() {
			key = service.VideoProviderTaskKey(endpoint, c.Param("request_id"))
		}
		h.handleGrokMedia(c, endpoint, key)
	}
}

func (h *OpenAIGatewayHandler) ProviderVideos(c *gin.Context) {
	c.Set(service.VideoUnifiedContextKey, true)
	if c.Request.Method != http.MethodPost {
		endpoint, key, err := service.ParsePublicVideoTaskID(c.Param("request_id"))
		if err != nil {
			h.errorResponse(c, 404, "not_found_error", "Video request not found")
			return
		}
		platform := endpoint.VideoProvider()
		if endpoint.IsSeedance() {
			platform = service.PlatformSeedance
		}
		groupPlatform := videoGroupPlatform(c)
		if groupPlatform != platform && groupPlatform != service.PlatformComposite {
			h.errorResponse(c, 404, "not_found_error", "Video request not found")
			return
		}
		if strings.HasSuffix(c.Request.URL.Path, "/content") {
			c.Set("video_download", true)
		}
		h.handleGrokMedia(c, endpoint, key)
		return
	}
	body, err := httputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		status := http.StatusBadRequest
		if _, ok := err.(*http.MaxBytesError); ok {
			status = http.StatusRequestEntityTooLarge
		}
		h.errorResponse(c, status, "invalid_request_error", "Cannot read video request body")
		return
	}
	endpoint, native, contentType, err := service.PrepareUnifiedVideoRequest(c, videoGroupPlatform(c), body)
	if err != nil {
		h.errorResponse(c, 400, "invalid_request_error", err.Error())
		return
	}
	requestmodel.ResetRequestBody(c.Request, native)
	c.Request.Header.Set("Content-Type", contentType)
	h.handleGrokMedia(c, endpoint, "")
}

func prepareProviderVideoBilling(ctx context.Context, h *OpenAIGatewayHandler, key *service.APIKey, subject middleware.AuthSubject, taskID string, result *service.OpenAIForwardResult) *service.OpenAIForwardResult {
	if result == nil || result.VideoCount <= 0 {
		return nil
	}
	pending, err := h.gatewayService.LoadGrokVideoPendingBilling(ctx, taskID, subject.UserID, key.ID)
	if err != nil || pending == nil {
		return nil
	}
	merged := *result
	merged.Model, merged.BillingModel = pending.Model, firstNonEmptyString(pending.BillingModel, pending.Model)
	merged.UpstreamModel = firstNonEmptyString(pending.UpstreamModel, result.UpstreamModel)
	merged.ResponseID = taskID
	if merged.VideoDurationSeconds <= 0 {
		merged.VideoDurationSeconds = pending.VideoDurationSeconds
	}
	if merged.VideoResolution == "" {
		merged.VideoResolution = pending.VideoResolution
	}
	if !h.gatewayService.ProviderVideoBillingReady(ctx, key, &merged) {
		return nil
	}
	claimed, err := h.gatewayService.ClaimGrokVideoBilling(ctx, taskID, subject.UserID, key.ID)
	if err != nil || !claimed {
		return nil
	}
	merged.RequestID, merged.ResponseID = service.StableGrokVideoBillingRequestID(taskID), taskID
	merged.Duration = service.GrokVideoE2EDuration(pending.CreatedAt, time.Now())
	return &merged
}
