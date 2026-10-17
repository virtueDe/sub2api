package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func cacheImageForceURLSettings(t *testing.T, enabled bool, accounts []int64, passthrough bool) {
	t.Helper()
	cacheImagePassthroughSettings(t, passthrough, nil)
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		imageForceURLResponseEnabled: enabled, imageForceURLResponseAccountIDs: accounts,
		imageUpstreamResponsePassthroughEnabled: passthrough,
		imageURLResponseB64Enabled:              true,
		expiresAt:                               time.Now().Add(time.Hour).UnixNano(),
	})
}

func TestImageForceURLAccountSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enabled  bool
		accounts []int64
		account  int64
		want     bool
	}{
		{"disabled", false, []int64{7}, 7, false},
		{"selected", true, []int64{7}, 7, true},
		{"not selected", true, []int64{7}, 8, false},
		{"all", true, nil, 8, true},
		{"invalid account", true, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheImageForceURLSettings(t, tc.enabled, tc.accounts, false)
			require.Equal(t, tc.want, (&SettingService{}).IsImageForceURLResponseEnabledForAccount(context.Background(), tc.account))
		})
	}
}

func TestOpenAIImagesForceURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, async := range []bool{false, true} {
		for _, multipartBody := range []bool{false, true} {
			for _, format := range []string{"", "url", "b64_json"} {
				for _, passthrough := range []bool{false, true} {
					t.Run(fmt.Sprintf("async=%t/multipart=%t/format=%s/passthrough=%t", async, multipartBody, format, passthrough), func(t *testing.T) {
						cacheImageForceURLSettings(t, true, []int64{7}, passthrough)
						payload := map[string]string{"model": "gpt-image-2", "prompt": "cat"}
						if format != "" {
							payload["response_format"] = format
						}
						body, err := json.Marshal(payload)
						require.NoError(t, err)
						contentType, endpoint := "application/json", "/v1/images/generations"
						if multipartBody {
							var buf bytes.Buffer
							writer := multipart.NewWriter(&buf)
							for k, v := range payload {
								require.NoError(t, writer.WriteField(k, v))
							}
							file, err := writer.CreateFormFile("image", "input.png")
							require.NoError(t, err)
							_, err = file.Write(pngBytes)
							require.NoError(t, err)
							require.NoError(t, writer.Close())
							body, contentType, endpoint = buf.Bytes(), writer.FormDataContentType(), "/v1/images/edits"
						}
						original := append([]byte(nil), body...)
						response := []byte(`{"data":[{"url":"https://upstream.test/image.png"}]}`)
						upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "application/json", response)}
						// No object storage: forced URL must bypass the b64 compatibility prerequisites.
						svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: &SettingService{}, httpUpstream: upstream}
						rec := httptest.NewRecorder()
						c, _ := gin.CreateTestContext(rec)
						c.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
						c.Request.Header.Set("Content-Type", contentType)
						parsed, err := svc.ParseOpenAIImagesRequest(c, body)
						require.NoError(t, err)
						ctx := context.Background()
						if async {
							ctx = WithAsyncImageTaskExecution(ctx)
						}
						_, err = svc.ForwardImages(ctx, c, b64BackfillAccount(true), body, parsed, "")
						require.NoError(t, err)
						require.JSONEq(t, string(response), rec.Body.String())
						require.Len(t, upstream.requests, 1, "must not backfill URL responses")
						require.Equal(t, original, body)
						require.Equal(t, format, parsed.ResponseFormat, "do not mutate the next account attempt")
						if multipartBody {
							_, options, err := mime.ParseMediaType(upstream.requests[0].Header.Get("Content-Type"))
							require.NoError(t, err)
							reader := multipart.NewReader(bytes.NewReader(upstream.lastBody), options["boundary"])
							fields := map[string]string{}
							for {
								part, err := reader.NextPart()
								if err == io.EOF {
									break
								}
								require.NoError(t, err)
								value, err := io.ReadAll(part)
								require.NoError(t, err)
								if part.FileName() != "" {
									require.Equal(t, "input.png", part.FileName())
									require.Equal(t, pngBytes, value)
								} else {
									fields[part.FormName()] = string(value)
								}
							}
							require.Equal(t, "url", fields["response_format"])
							require.Equal(t, "cat", fields["prompt"])
						} else {
							require.Equal(t, "url", gjson.GetBytes(upstream.lastBody, "response_format").String())
						}
					})
				}
			}
		}
	}
}

func TestOpenAIImagesForceURLDoesNotLeakAcrossAccounts(t *testing.T) {
	for _, order := range [][]int64{{7, 8}, {8, 7}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			cacheImageForceURLSettings(t, true, []int64{7}, true)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				b64BackfillImageResponse(http.StatusServiceUnavailable, "application/json", []byte(`{"error":{"message":"retry"}}`)),
				b64BackfillImageResponse(http.StatusOK, "application/json", []byte(`{"data":[{"url":"https://upstream.test/image.png"}]}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: &SettingService{}, httpUpstream: upstream}
			body := []byte(`{"model":"gpt-image-2","prompt":"cat","response_format":"b64_json"}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			for i, id := range order {
				account := b64BackfillAccount(false)
				account.ID = id
				_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
				if i == 0 {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				want := "b64_json"
				if id == 7 {
					want = "url"
				}
				require.Equal(t, want, gjson.GetBytes(upstream.lastBody, "response_format").String())
			}
		})
	}
}

func TestOpenAIImagesForceURLPreservesInputCompatibility(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", passthrough), func(t *testing.T) {
			cacheImageForceURLSettings(t, true, []int64{7}, passthrough)
			body := []byte(`{"model":"gpt-image-2","prompt":"cat","response_format":"b64_json","images":[{"image_url":"https://source.example.com/reference.png?sig=kept"}],"extra":{"preserve":true}}`)
			response := []byte(`{"data":[{"url":"https://upstream.test/image.png"}]}`)
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				b64BackfillImageResponse(http.StatusOK, "image/png", pngBytes),
				b64BackfillImageResponse(http.StatusOK, "application/json", response),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: &SettingService{}, httpUpstream: upstream}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			_, err = svc.ForwardImages(context.Background(), c, b64BackfillAccount(true), body, parsed, "")
			require.NoError(t, err)
			require.Len(t, upstream.requests, 2, "only download the input image, never the output URL")
			require.Equal(t, "https://source.example.com/reference.png?sig=kept", upstream.requests[0].URL.String())
			require.Equal(t, "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBytes), gjson.GetBytes(upstream.lastBody, "images.0.image_url").String())
			require.Equal(t, "url", gjson.GetBytes(upstream.lastBody, "response_format").String())
			require.True(t, gjson.GetBytes(upstream.lastBody, "extra.preserve").Bool())
			require.JSONEq(t, string(response), rec.Body.String())
		})
	}
}

func TestGrokImagesForceURLAfterBodyRebuild(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	for _, async := range []bool{false, true} {
		for _, endpoint := range []GrokMediaEndpoint{GrokMediaEndpointImagesGenerations, GrokMediaEndpointImagesEdits, GrokMediaEndpointVideosGenerations} {
			t.Run(fmt.Sprintf("%s/async=%t", endpoint, async), func(t *testing.T) {
				cacheImageForceURLSettings(t, true, []int64{7}, false)
				body := []byte(`{"model":"grok-imagine-image","prompt":"cat","response_format":"b64_json"}`)
				contentType := "application/json"
				if endpoint == GrokMediaEndpointImagesEdits {
					var buf bytes.Buffer
					writer := multipart.NewWriter(&buf)
					require.NoError(t, writer.WriteField("model", "grok-imagine-edit"))
					require.NoError(t, writer.WriteField("prompt", "cat"))
					part, err := writer.CreateFormFile("image", "input.png")
					require.NoError(t, err)
					_, err = part.Write(pngBytes)
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					body, contentType = buf.Bytes(), writer.FormDataContentType()
				} else if endpoint == GrokMediaEndpointVideosGenerations {
					body = []byte(`{"model":"grok-imagine-video","prompt":"cat"}`)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", contentType)
				account := &Account{ID: 7, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://xai.test/v1"}}
				response := `{"data":[{"url":"https://images.test/cat.png"}]}`
				if endpoint == GrokMediaEndpointVideosGenerations {
					response = `{"request_id":"video-task"}`
				}
				upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "application/json", []byte(response))}
				svc := &OpenAIGatewayService{settingService: &SettingService{}, httpUpstream: upstream}
				ctx := context.Background()
				if async {
					ctx = WithAsyncImageTaskExecution(ctx)
				}
				_, err := svc.ForwardGrokMedia(ctx, c, account, endpoint, "", body, contentType)
				require.NoError(t, err)
				format := gjson.GetBytes(upstream.lastBody, "response_format")
				if endpoint == GrokMediaEndpointVideosGenerations {
					require.False(t, format.Exists())
				} else {
					require.Equal(t, "url", format.String())
				}
				if endpoint == GrokMediaEndpointImagesEdits {
					require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "image.url").String())
				}
			})
		}
	}
}
