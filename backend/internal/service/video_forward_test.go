//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProviderVideoForwardPreservesNativeFieldsAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		platform              string
		endpoint              GrokMediaEndpoint
		body, response, field string
	}{
		{PlatformWan, "wan_create", `{"model":"video","input":{"prompt":"waves","negative_prompt":"blur"},"parameters":{"size":"1280*720","duration":5,"watermark":false}}`, `{"output":{"task_id":"task-1"}}`, "model"},
		{PlatformKling, "kling_text_create", `{"model_name":"video","prompt":"waves","mode":"pro","duration":"5","cfg_scale":0.5}`, `{"code":0,"data":{"task_id":"task-1"}}`, "model_name"},
		{PlatformSora, "sora_create", `{"model":"video","prompt":"waves","seconds":"4","size":"1280x720"}`, `{"id":"task-1","status":"queued"}`, "model"},
	} {
		a := &Account{ID: 9, Platform: tc.platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-secret", "model_mapping": map[string]any{"video": "upstream-video"}}}
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(tc.response)}
		s := &OpenAIGatewayService{httpUpstream: upstream}
		c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
		result, err := s.ForwardVideoProvider(context.Background(), c, a, tc.endpoint, "", []byte(tc.body), "application/json")
		require.NoError(t, err)
		require.JSONEq(t, tc.response, w.Body.String())
		require.Zero(t, result.VideoCount)
		require.Equal(t, "Bearer test-secret", upstream.request.Header.Get("Authorization"))
		forwarded, err := io.ReadAll(upstream.request.Body)
		require.NoError(t, err)
		require.Equal(t, "upstream-video", gjson.GetBytes(forwarded, tc.field).String())
		gjson.Parse(tc.body).ForEach(func(key, value gjson.Result) bool {
			if key.String() != tc.field {
				require.Equal(t, value.Raw, gjson.GetBytes(forwarded, key.String()).Raw)
			}
			return true
		})
		if tc.platform == PlatformWan {
			require.Equal(t, "enable", upstream.request.Header.Get("X-DashScope-Async"))
		}
	}
}

func TestUnifiedVideoAPIPassthroughPreservesRequestForAllPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		t.Run(platform, func(t *testing.T) {
			body := []byte(`{"model":"public-model","prompt":"waves","duration":"8s","aspect_ratio":"9:16","reference_images":[{"url":"https://example.com/a.png","role":"first_frame"}],"reference_videos":["extend://media-1"],"custom_options":{"camera":"orbit"}}`)
			a := &Account{ID: 9, Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"api_key": "test-key", "base_url": "https://video.example/v1", VideoAPIModeCredential: VideoAPIModePassthrough,
				"model_mapping": map[string]any{"public-model": "upstream-model"},
			}}
			upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task-1","object":"video","model":"upstream-model","status":"queued","progress":0}`)}
			s := &OpenAIGatewayService{httpUpstream: upstream}
			c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
			c.Set(VideoUnifiedContextKey, true)
			c.Set(videoUnifiedInputKey, unifiedVideoInput{body: body, contentType: "application/json"})
			endpoint := map[string]GrokMediaEndpoint{PlatformSeedance: SeedanceEndpointCreate, PlatformWan: "wan_create", PlatformKling: "kling_text_create", PlatformSora: "sora_create"}[platform]
			result, err := s.ForwardUnifiedVideoAPI(context.Background(), c, a, endpoint, "", body, "application/json")
			require.NoError(t, err)
			require.Equal(t, "POST", upstream.request.Method)
			require.Equal(t, "/v1/videos", upstream.request.URL.Path)
			require.Equal(t, "Bearer test-key", upstream.request.Header.Get("Authorization"))
			forwarded, err := io.ReadAll(upstream.request.Body)
			require.NoError(t, err)
			require.JSONEq(t, `{"model":"upstream-model","prompt":"waves","duration":"8s","aspect_ratio":"9:16","reference_images":[{"url":"https://example.com/a.png","role":"first_frame"}],"reference_videos":["extend://media-1"],"custom_options":{"camera":"orbit"}}`, string(forwarded))
			require.Equal(t, "public-model", result.Model)
			require.Equal(t, "public-model", result.BillingModel)
			require.Equal(t, "upstream-model", result.UpstreamModel)
			require.Equal(t, PublicVideoTaskID(""+platform+":passthrough:task-1"), gjson.Get(w.Body.String(), "id").String())
			require.Equal(t, "queued", gjson.Get(w.Body.String(), "status").String())
		})
	}
}

func TestVideoAPIPassthroughDownloadUsesAuthenticatedContentFallback(t *testing.T) {
	a := &Account{ID: 9, Platform: PlatformKling, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "owner-key", "base_url": "https://video.example/v1", VideoAPIModeCredential: VideoAPIModePassthrough,
	}}
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"id":"raw-id","status":"completed","duration":"8s","size":"1280x720","url":"https://video.example/v1/videos/raw-id/content"}`),
		{StatusCode: 206, Header: http.Header{"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 0-3/10"}}, Body: io.NopCloser(strings.NewReader("data"))},
	}}
	s := &OpenAIGatewayService{httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodGet, "/v1/videos/id/content", map[string]string{"Authorization": "Bearer client-key", "Range": "bytes=0-3"})
	result, err := s.ForwardProviderVideoContent(context.Background(), c, a, "kling_passthrough_status", "kling:passthrough:raw-id")
	require.NoError(t, err)
	require.Equal(t, 206, w.Code)
	require.Equal(t, "data", w.Body.String())
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, "Bearer owner-key", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer owner-key", upstream.requests[1].Header.Get("Authorization"))
	require.Equal(t, "/v1/videos/raw-id", upstream.requests[0].URL.Path)
	require.Equal(t, "/v1/videos/raw-id/content", upstream.requests[1].URL.Path)
	require.Equal(t, "bytes=0-3", upstream.requests[1].Header.Get("Range"))
}

func TestUnifiedVideoAPIPassthroughPreservesMultipartReferences(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "veo-3.1-fast"))
	require.NoError(t, w.WriteField("duration", "8s"))
	for _, name := range []string{"reference_images[]", "reference_images[]", "reference_video"} {
		part, err := w.CreateFormFile(name, "reference.bin")
		require.NoError(t, err)
		_, err = part.Write([]byte{0, 1, 2, 255})
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	a := &Account{ID: 9, Platform: PlatformSeedance, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "key", "base_url": "https://video.example/v1", VideoAPIModeCredential: VideoAPIModePassthrough,
	}}
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task","status":"queued"}`)}
	s := &OpenAIGatewayService{httpUpstream: upstream}
	c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	endpoint, body, contentType, err := PrepareVideoAPIRequest(c, a.Platform, buf.Bytes())
	require.NoError(t, err)
	info, err := ParseVideoAPIRoutingRequest(contentType, body)
	require.NoError(t, err)
	require.Len(t, info.Uploads, 2)
	for _, upload := range info.Uploads {
		require.Equal(t, int64(4), upload.Size())
		data, err := upload.DataURL()
		require.NoError(t, err)
		require.Contains(t, data, "AAEC/w==")
	}
	_, err = s.ForwardUnifiedVideoAPI(context.Background(), c, a, endpoint, "", body, contentType)
	require.NoError(t, err)
	forwarded, err := io.ReadAll(upstream.request.Body)
	require.NoError(t, err)
	require.Equal(t, buf.Bytes(), forwarded)
	require.Equal(t, w.FormDataContentType(), upstream.request.Header.Get("Content-Type"))
}

func TestPrepareVideoAPIRequestAppliesCompositeModelBeforeScheduling(t *testing.T) {
	c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	c.Request = c.Request.WithContext(WithCompositeRouteDecision(c.Request.Context(), CompositeRouteDecision{Matched: true, PublicModel: "public-video", UpstreamModel: "account-video", TargetPlatform: PlatformWan}))
	_, body, _, err := PrepareVideoAPIRequest(c, PlatformWan, []byte(`{"model":"public-video","prompt":"waves","duration":"8s","reference_videos":["extend://media-1"]}`))
	require.NoError(t, err)
	require.Equal(t, "account-video", gjson.GetBytes(body, "model").String())
	require.Equal(t, "8s", gjson.GetBytes(body, "duration").String())
	require.Equal(t, "extend://media-1", gjson.GetBytes(body, "reference_videos.0").String())
}

func TestVideoAPIPassthroughUnconfirmedSubmissionKeepsTaskQueryable(t *testing.T) {
	a := &Account{ID: 9, Platform: PlatformKling, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "key", "base_url": "https://video.example/v1", VideoAPIModeCredential: VideoAPIModePassthrough,
	}}
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"error":{"code":"submission_unconfirmed","message":"Keep polling this task"},"data":{"task_id":"upstream-task"}}`)}
	upstream.response.StatusCode = http.StatusConflict
	s := &OpenAIGatewayService{httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	c.Set(VideoUnifiedContextKey, true)
	result, err := s.ForwardUnifiedVideoAPI(context.Background(), c, a, "kling_text_create", "", []byte(`{"model":"kling-v4.0-flash","prompt":"waves"}`), "application/json")
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Equal(t, "kling:passthrough:upstream-task", result.ResponseID)
	require.Equal(t, PublicVideoTaskID(result.ResponseID), gjson.Get(w.Body.String(), "data.task_id").String())
	require.Equal(t, "submission_unconfirmed", gjson.Get(w.Body.String(), "error.code").String())
	require.Zero(t, result.VideoCount)
	require.Len(t, upstream.requests, 1)
}

func TestProviderVideoSoraDownloadChecksCompletionAndUsesOwnerCredential(t *testing.T) {
	a := &Account{ID: 9, Platform: PlatformSora, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "owner-secret"}}
	upstream := &grokMediaContentUpstreamStub{responses: []*http.Response{
		grokMediaContentStatusResponse(`{"id":"raw-id","status":"completed","seconds":"8","size":"1280x720"}`),
		{StatusCode: 206, Header: http.Header{"Content-Type": []string{"video/mp4"}, "Content-Range": []string{"bytes 0-3/10"}}, Body: io.NopCloser(strings.NewReader("data"))},
	}}
	s := &OpenAIGatewayService{httpUpstream: upstream}
	c, w := grokMediaContentTestContext(http.MethodGet, "/v1/videos/id/content", map[string]string{"Authorization": "Bearer user-key", "Range": "bytes=0-3"})
	result, err := s.ForwardProviderVideoContent(context.Background(), c, a, "sora_status", "sora:raw-id")
	require.NoError(t, err)
	require.Equal(t, 206, w.Code)
	require.Equal(t, "data", w.Body.String())
	require.Equal(t, 1, result.VideoCount)
	require.Equal(t, 8, result.VideoDurationSeconds)
	require.Len(t, upstream.requests, 2)
	for _, req := range upstream.requests {
		require.Equal(t, "Bearer owner-secret", req.Header.Get("Authorization"))
	}
	require.Equal(t, "/v1/videos/raw-id/content", upstream.requests[1].URL.Path)
	require.Equal(t, "bytes=0-3", upstream.requests[1].Header.Get("Range"))
	upstream.responses = []*http.Response{grokMediaContentStatusResponse(`{"id":"raw-id","status":"in_progress"}`)}
	c, w = grokMediaContentTestContext(http.MethodGet, "/v1/videos/id/content", nil)
	_, err = s.ForwardProviderVideoContent(context.Background(), c, a, "sora_status", "sora:raw-id")
	require.Error(t, err)
	require.Equal(t, 409, w.Code)
	require.Len(t, upstream.requests, 3)
}

func TestProviderVideoCreateDoesNotRetryAndPreservesUpstreamError(t *testing.T) {
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"error":{"message":"quota exceeded"}}`)}
	upstream.response.StatusCode = 429
	s := &OpenAIGatewayService{httpUpstream: upstream}
	a := &Account{Platform: PlatformSora, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}
	c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	_, err := s.ForwardVideoProvider(context.Background(), c, a, "sora_create", "", []byte(`{"model":"sora-2","prompt":"waves"}`), "application/json")
	require.Error(t, err)
	require.Equal(t, 429, w.Code)
	require.Len(t, upstream.requests, 1)
	require.Contains(t, w.Body.String(), "quota exceeded")
}

func TestUnifiedVideoWanAccountAliasChoosesMappedProtocol(t *testing.T) {
	for _, tc := range []struct{ model, path, imageField, sizeField string }{
		{"wan2.2-kf2v-flash", "/image2video/video-synthesis", "input.first_frame_url", "parameters.resolution"},
		{"wan2.6-t2v", "/video-generation/video-synthesis", "input.img_url", "parameters.size"},
	} {
		a := &Account{Platform: PlatformWan, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key", "model_mapping": map[string]any{"alias": tc.model}}}
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"output":{"task_id":"task"}}`)}
		s := &OpenAIGatewayService{httpUpstream: upstream}
		c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
		endpoint, body, contentType, err := PrepareUnifiedVideoRequest(c, PlatformWan, []byte(`{"model":"alias","prompt":"waves","seconds":5,"size":"1280x720","input_reference":"https://example.com/frame.png"}`))
		require.NoError(t, err)
		result, err := s.ForwardVideoProvider(context.Background(), c, a, endpoint, "", body, contentType)
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(upstream.request.URL.Path, tc.path))
		payload, err := io.ReadAll(upstream.request.Body)
		require.NoError(t, err)
		require.Equal(t, tc.model, gjson.GetBytes(payload, "model").String())
		require.Equal(t, "https://example.com/frame.png", gjson.GetBytes(payload, tc.imageField).String())
		require.True(t, gjson.GetBytes(payload, tc.sizeField).Exists())
		require.Equal(t, "alias", result.BillingModel)
	}
}
