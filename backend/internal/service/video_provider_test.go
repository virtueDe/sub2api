//go:build unit

package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestVideoProviderNativeRequests(t *testing.T) {
	for _, tc := range []struct {
		endpoint                GrokMediaEndpoint
		body, model, resolution string
		seconds                 int
	}{
		{"wan_create", `{"model":"wan2.6-t2v","input":{"prompt":"waves"},"parameters":{"size":"1280*720","duration":10}}`, "wan2.6-t2v", "720p", 10},
		{"wan_image_create", `{"model":"wan2.2-kf2v-flash","input":{"first_frame_url":"https://example.com/a.png"},"parameters":{"resolution":"1080P","duration":5}}`, "wan2.2-kf2v-flash", "1080p", 5},
		{"kling_text_create", `{"model_name":"kling-v2-6","prompt":"waves","duration":"10","mode":"pro"}`, "kling-v2-6", "1080p", 10},
		{"kling_image_create", `{"model_name":"kling-v2-6","image":"https://example.com/a.png","mode":"std"}`, "kling-v2-6", "720p", 5},
		{"sora_create", `{"model":"sora-2","prompt":"waves","seconds":"8","size":"1280x720"}`, "sora-2", "720p", 8},
	} {
		t.Run(string(tc.endpoint), func(t *testing.T) {
			info, err := ParseVideoProviderRequest(tc.endpoint, "application/json", []byte(tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.model, info.Model)
			require.Equal(t, tc.seconds, info.DurationSeconds)
			require.Equal(t, tc.resolution, info.Resolution)
			if strings.Contains(tc.body, "example.com") {
				require.Contains(t, string(info.ModerationBody()), "example.com")
			}
		})
	}
}

func TestVideoProviderRejectsInvalidRequests(t *testing.T) {
	for _, body := range []string{`{`, `[]`, `{}`, `{"model":23}`, `{"model":"wan","input":{},"parameters":{"duration":-1}}`, `{"model":"wan","input":{},"parameters":{"duration":5,"resolution":"4k"}}`, `{"model":"wan","input":{},"callback_url":"https://example.com"}`} {
		_, err := ParseVideoProviderRequest("wan_create", "application/json", []byte(body))
		require.Error(t, err, body)
	}
	_, err := ParseVideoProviderRequest("wan_create", "text/plain", []byte(`{"model":"wan"}`))
	require.Error(t, err)
}

func TestKlingCredentialSigning(t *testing.T) {
	now := time.Unix(1700000000, 0)
	a := &Account{Credentials: map[string]any{"api_key": "access|secret"}}
	token, err := videoProviderToken(a, "kling", now)
	require.NoError(t, err)
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	require.Equal(t, "access", gjson.GetBytes(claims, "iss").String())
	require.Equal(t, now.Unix()+1800, gjson.GetBytes(claims, "exp").Int())
	require.Equal(t, now.Unix()-5, gjson.GetBytes(claims, "nbf").Int())
	mac := hmac.New(sha256.New, []byte("secret"))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), parts[2])
	for _, key := range []string{"", "access|", "|secret", "a|b|c"} {
		a.Credentials["api_key"] = key
		_, err = videoProviderToken(a, "kling", now)
		require.Error(t, err)
	}
	a.Credentials["api_key"] = "proxy-key"
	token, err = videoProviderToken(a, "kling", now)
	require.NoError(t, err)
	require.Equal(t, "proxy-key", token)
}

func TestVideoProviderTaskNamespacesAndPaths(t *testing.T) {
	for _, tc := range []struct {
		endpoint           GrokMediaEndpoint
		base, id, expected string
	}{
		{"wan_status", "https://example.com/api/v1", "one", "https://example.com/api/v1/tasks/one"},
		{"kling_text_status", "https://example.com", "one", "https://example.com/v1/videos/text2video/one"},
		{"kling_image_status", "https://example.com/proxy/v1/", "one", "https://example.com/proxy/v1/videos/image2video/one"},
		{"sora_content", "https://example.com/v1", "sora_video_1", "https://example.com/v1/videos/sora_video_1/content"},
	} {
		key := VideoProviderTaskKey(tc.endpoint, tc.id)
		url, err := buildVideoProviderURL(tc.base, tc.endpoint, key)
		require.NoError(t, err)
		require.Equal(t, tc.expected, url)
	}
	require.NotEqual(t, VideoProviderTaskKey("kling_text_status", "one"), VideoProviderTaskKey("kling_image_status", "one"))
	for _, id := range []string{"", "..", "a/b", "%2f", "a?b", "a#b"} {
		_, err := buildVideoProviderURL("https://example.com", "wan_status", VideoProviderTaskKey("wan_status", id))
		require.Error(t, err, id)
	}
}

func TestSoraMultipartPreservesReferenceAndMapsModel(t *testing.T) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "public-video"))
	require.NoError(t, w.WriteField("prompt", "waves"))
	require.NoError(t, w.WriteField("seconds", "8"))
	f, err := w.CreateFormFile("input_reference", "first.png")
	require.NoError(t, err)
	_, err = f.Write([]byte{0, 1, 2, 255})
	require.NoError(t, err)
	require.NoError(t, w.Close())
	info, err := ParseVideoProviderRequest("sora_create", w.FormDataContentType(), buf.Bytes())
	require.NoError(t, err)
	require.Equal(t, "public-video", info.Model)
	require.Equal(t, 8, info.DurationSeconds)
	c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	c.Request = c.Request.WithContext(WithCompositeRouteDecision(c.Request.Context(), CompositeRouteDecision{Matched: true, PublicModel: "public-video", UpstreamModel: "sora-2", TargetPlatform: PlatformSora}))
	_, routedBody, routedType, err := PrepareUnifiedVideoRequest(c, PlatformSora, buf.Bytes())
	require.NoError(t, err)
	routed, err := ParseVideoProviderRequest("sora_create", routedType, routedBody)
	require.NoError(t, err)
	require.Equal(t, "sora-2", routed.Model)
	require.Len(t, routed.Uploads, 1)
	mapped, contentType, err := rewriteVideoMultipart(buf.Bytes(), w.Boundary(), "sora-2")
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(contentType)
	require.NoError(t, err)
	r := multipart.NewReader(bytes.NewReader(mapped), params["boundary"])
	seen := 0
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		if part.FormName() == "model" {
			require.Equal(t, "sora-2", string(data))
		}
		if part.FormName() == "input_reference" {
			require.Equal(t, []byte{0, 1, 2, 255}, data)
			require.Equal(t, "first.png", part.FileName())
		}
		seen++
	}
	require.Equal(t, 4, seen)
}

func TestVideoProviderCompletionOnlyBillsSuccess(t *testing.T) {
	for _, tc := range []struct {
		provider, success, pending string
		seconds                    int
	}{
		{"wan", `{"output":{"task_status":"SUCCEEDED"},"usage":{"duration":10,"input_video_duration":2}}`, `{"output":{"task_status":"FAILED"}}`, 12},
		{"kling", `{"data":{"task_status":"succeed","task_result":{"videos":[{"duration":"5.1"}]}}}`, `{"data":{"task_status":"processing"}}`, 6},
		{"kling", `{"data":{"task_status":"succeed"}}`, `{"data":{"task_status":"failed"}}`, 0},
		{"sora", `{"status":"completed","seconds":"8","size":"720x1280"}`, `{"status":"failed","seconds":"8"}`, 8},
	} {
		result := &OpenAIForwardResult{}
		parseVideoProviderCompletion(tc.provider, []byte(tc.pending), result)
		require.Zero(t, result.VideoCount)
		parseVideoProviderCompletion(tc.provider, []byte(tc.success), result)
		require.Equal(t, 1, result.VideoCount)
		require.Equal(t, tc.seconds, result.VideoDurationSeconds)
	}
}
