package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func cacheImagePassthroughSettings(t *testing.T, enabled bool, accounts []int64) {
	t.Helper()
	previous := gatewayForwardingCache.Load()
	t.Cleanup(func() {
		if previous != nil {
			gatewayForwardingCache.Store(previous)
		} else {
			gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
		}
	})
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		imageUpstreamResponsePassthroughEnabled:    enabled,
		imageUpstreamResponsePassthroughAccountIDs: accounts,
		imageURLResponseB64Enabled:                 true,
		expiresAt:                                  time.Now().Add(time.Hour).UnixNano(),
	})
}

func TestImageUpstreamResponsePassthroughAccountSelection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enabled   bool
		accounts  []int64
		accountID int64
		want      bool
	}{
		{name: "disabled", accounts: []int64{7}, accountID: 7},
		{name: "selected", enabled: true, accounts: []int64{7}, accountID: 7, want: true},
		{name: "not selected", enabled: true, accounts: []int64{7}, accountID: 8},
		{name: "empty selects all", enabled: true, accountID: 8, want: true},
		{name: "missing account", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cacheImagePassthroughSettings(t, tc.enabled, tc.accounts)
			require.Equal(t, tc.want, (&SettingService{}).IsImageUpstreamResponsePassthroughEnabledForAccount(context.Background(), tc.accountID))
		})
	}
}

func TestOpenAIImagesUpstreamResponsePassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheImagePassthroughSettings(t, true, []int64{7})
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	for _, async := range []bool{false, true} {
		for _, format := range []string{"", "url", "b64_json"} {
			for _, field := range []string{"url", "b64_json"} {
				name := field + "/" + format
				if async {
					name = "async/" + name
				} else {
					name = "sync/" + name
				}
				t.Run(name, func(t *testing.T) {
					body := []byte(`{"model":"gpt-image-2","prompt":"cat"}`)
					if format != "" {
						body = []byte(`{"model":"gpt-image-2","prompt":"cat","response_format":"` + format + `"}`)
					}
					payload := "https://upstream.test/image.png?signature=preserved"
					if field == "b64_json" {
						payload = b64
					}
					response := `{"created":123,"data":[{"` + field + `":"` + payload + `","revised_prompt":"kept"}],"usage":{"input_tokens":10,"output_tokens":20}}`
					upstream := &httpUpstreamRecorder{resp: b64BackfillImageResponse(http.StatusOK, "application/json", []byte(response))}
					storage := &fakeImageStorage{err: errors.New("must not upload")}
					svc := &OpenAIGatewayService{
						cfg: &config.Config{}, settingService: &SettingService{}, httpUpstream: upstream,
						imageResponseUploader: NewImageResultUploader(storage, "images/", 0, nil),
					}
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
					c.Request.Header.Set("Content-Type", "application/json")
					parsed, err := svc.ParseOpenAIImagesRequest(c, body)
					require.NoError(t, err)
					ctx := context.Background()
					if async {
						ctx = WithAsyncImageTaskExecution(ctx)
					}
					result, err := svc.ForwardImages(ctx, c, b64BackfillAccount(true), body, parsed, "")
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, response, rec.Body.String())
					require.Len(t, upstream.requests, 1, "must not fetch result URLs")
					require.Equal(t, format, gjson.GetBytes(upstream.lastBody, "response_format").String())
					require.Empty(t, storage.saved)
					require.Equal(t, 1, result.ImageCount)
					require.Equal(t, 20, result.Usage.OutputTokens)
					require.False(t, parsed.upstreamResponsePassthrough, "attempt policy must not mutate the original request")
				})
			}
		}
	}
}

func TestOpenAIImagesPassthroughDoesNotLeakAcrossAccountAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cacheImagePassthroughSettings(t, true, []int64{7})
	for _, order := range [][]int64{{7, 8}, {8, 7}} {
		t.Run(fmt.Sprintf("%d to %d", order[0], order[1]), func(t *testing.T) {
			b64 := base64.StdEncoding.EncodeToString(pngBytes)
			response := `{"data":[{"b64_json":"` + b64 + `"}]}`
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				b64BackfillImageResponse(http.StatusServiceUnavailable, "application/json", []byte(`{"error":{"message":"retry"}}`)),
				b64BackfillImageResponse(http.StatusOK, "application/json", []byte(response)),
			}}
			storage := &fakeImageStorage{}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, settingService: &SettingService{}, httpUpstream: upstream,
				imageResponseUploader: NewImageResultUploader(storage, "images/", 0, nil)}
			body := []byte(`{"model":"gpt-image-2","prompt":"cat","response_format":"url"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
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
			}
			if order[1] == 7 {
				require.Equal(t, response, rec.Body.String())
				require.Empty(t, storage.saved)
			} else {
				require.Len(t, storage.saved, 1)
				require.Contains(t, rec.Body.String(), "https://cdn.test/")
				require.NotContains(t, rec.Body.String(), "b64_json")
			}
		})
	}
}

func TestOpenAIImagesOAuthPassthroughPreservesBase64(t *testing.T) {
	cacheImagePassthroughSettings(t, true, []int64{35})
	for _, route := range []string{"native", "responses", "fallback"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", route, stream), func(t *testing.T) {
				model := "gpt-image-2"
				if route == "responses" {
					model = "gpt-image-1"
				}
				body := []byte(fmt.Sprintf(`{"model":%q,"prompt":"cat","response_format":"url","stream":%t}`, model, stream))
				c, rec := newOpenAIImagesTestContext(t, body)
				response := openAIImagesJSONResponse()
				if route != "native" {
					response = b64BackfillImageResponse(http.StatusOK, "text/event-stream", []byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\"}]}}\n\n"))
				} else if stream {
					response = b64BackfillImageResponse(http.StatusOK, "text/event-stream", []byte("data: {\"type\":\"image_generation.completed\",\"b64_json\":\"aGVsbG8=\"}\n\n"))
				}
				upstream := &httpUpstreamRecorder{resp: response}
				if route == "fallback" {
					upstream.responses = []*http.Response{
						b64BackfillImageResponse(http.StatusNotFound, "application/json", []byte(`{"error":{"message":"not found"}}`)),
						response,
					}
				}
				svc := newOpenAIImagesTestService(upstream)
				svc.settingService = &SettingService{}
				parsed, err := svc.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				result, err := svc.ForwardImages(context.Background(), c, directImagesTestAccount(), body, parsed, "")
				require.NoError(t, err)
				require.Equal(t, 1, result.ImageCount)
				require.Contains(t, rec.Body.String(), `"b64_json":"aGVsbG8="`)
				require.NotContains(t, rec.Body.String(), `"url"`)
				require.NotContains(t, rec.Body.String(), "data:image/")
			})
		}
	}
}

func TestImageTaskServiceCompletePassthrough(t *testing.T) {
	for _, result := range []string{
		`{"created":1,"data":[{"url":"https://upstream.test/image.png?sig=kept"}]}`,
		`{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`,
		`{"data":[{"url":"https://upstream.test/a.png"},{"b64_json":"aGVsbG8="}]}`,
	} {
		t.Run(result, func(t *testing.T) {
			downloads := 0
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				downloads++
				return nil, errors.New("must not download")
			})}
			storage := &fakeImageStorage{err: errors.New("must not upload")}
			svc := NewImageTaskServiceWithUploader(&imageTaskMemoryStore{}, NewImageResultUploader(storage, "images/", 0, client), time.Hour, time.Minute)
			owner := ImageTaskOwner{UserID: 1, APIKeyID: 2}
			task, err := svc.Create(context.Background(), owner)
			require.NoError(t, err)
			ctx := WithImageUpstreamResponsePassthrough(context.Background(), true)
			require.NoError(t, svc.Complete(ctx, task.ID, http.StatusOK, json.RawMessage(result)))
			got, err := svc.Get(context.Background(), owner, task.ID)
			require.NoError(t, err)
			require.Equal(t, ImageTaskStatusCompleted, got.Status)
			require.Equal(t, result, string(got.Result))
			require.Zero(t, downloads)
			require.Empty(t, storage.saved)
		})
	}
}
