package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProviderVideoRoutesRegisteredWithNativeProtocols(t *testing.T) {
	router := newGatewayRoutesTestRouter(service.PlatformWan)
	registered := map[string]bool{}
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"POST /v1/videos", "GET /v1/videos/:request_id", "GET /v1/videos/:request_id/content", "HEAD /v1/videos/:request_id/content",
		"POST /api/v1/services/aigc/video-generation/video-synthesis", "POST /api/v1/services/aigc/image2video/video-synthesis", "GET /api/v1/tasks/:request_id",
		"POST /v1/videos/text2video", "GET /v1/videos/text2video/:request_id", "POST /v1/videos/image2video", "GET /v1/videos/image2video/:request_id",
	} {
		require.True(t, registered[route], route)
	}
	for _, path := range []string{"/v1/videos", "/v1/videos/text2video", "/api/v1/services/aigc/video-generation/video-synthesis", "/api/v3/contents/generations/tasks"} {
		require.Equal(t, service.CompositeRouteEndpointAny, compositeRouteEndpointForPath(path))
	}
}
