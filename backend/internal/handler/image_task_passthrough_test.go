//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type passthroughTaskStorage struct{ saves int }

func (s *passthroughTaskStorage) Save(context.Context, string, string, []byte) (string, error) {
	s.saves++
	return "https://cdn.test/offloaded.png", nil
}

type passthroughTaskTransport struct{ downloads int }

func (s *passthroughTaskTransport) RoundTrip(*http.Request) (*http.Response, error) {
	s.downloads++
	return nil, errors.New("direct results must not be downloaded")
}

func TestAsyncImageHandlerPassthroughUsesFinalAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	settings := service.NewSettingService(&toggleSettingRepo{values: map[string]string{}}, &config.Config{})
	policy, err := settings.GetAllSettings(ctx)
	require.NoError(t, err)
	policy.ImageUpstreamResponsePassthroughEnabled = true
	policy.ImageUpstreamResponsePassthroughAccountIDs = []int64{7}
	require.NoError(t, settings.UpdateSettings(ctx, policy))
	t.Cleanup(func() {
		policy.ImageUpstreamResponsePassthroughEnabled = false
		require.NoError(t, settings.UpdateSettings(ctx, policy))
	})
	gateway := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, settings, nil,
	)
	for _, tc := range []struct {
		name         string
		firstID      int64
		finalID      int64
		result       string
		wantDirect   bool
		itemPolicies []bool
	}{
		{"url after failover", 8, 7, `{"data":[{"url":"https://upstream.test/result.png?sig=kept"}]}`, true, nil},
		{"base64 after failover", 8, 7, `{"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZQAAAABJRU5ErkJggg=="}]}`, true, nil},
		{"unselected after failover", 7, 8, `{"data":[{"b64_json":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jZQAAAABJRU5ErkJggg=="}]}`, false, nil},
		{"Gemini mixed accounts final unselected", 7, 8, `{"data":[{"url":"https://upstream.test/direct.png"},{"b64_json":"aGVsbG8="}]}`, false, []bool{true, false}},
		{"Gemini mixed accounts final selected", 8, 7, `{"data":[{"b64_json":"aGVsbG8="},{"b64_json":"kept"}]}`, false, []bool{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &passthroughTaskStorage{}
			transport := &passthroughTaskTransport{}
			tasks := service.NewImageTaskServiceWithUploader(
				&asyncImageMemoryStore{tasks: map[string]*service.ImageTaskRecord{}},
				service.NewImageResultUploader(storage, "images/", 0, &http.Client{Transport: transport}),
				time.Hour, time.Minute,
			)
			owner := service.ImageTaskOwner{UserID: 7, APIKeyID: 9}
			task, err := tasks.Create(ctx, owner)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations/async", strings.NewReader(`{"prompt":"cat"}`))
			taskCtx, recorder, cancel := newAsyncImageContext(c, []byte(`{"prompt":"cat"}`), time.Minute)
			h := &AsyncImageHandler{tasks: tasks, openAI: &OpenAIGatewayHandler{gatewayService: gateway}}
			h.execute = func(_ string, c *gin.Context) {
				setOpsSelectedAccount(c, tc.firstID)
				setOpsSelectedAccount(c, tc.finalID)
				if tc.itemPolicies != nil {
					c.Set(imageTaskPassthroughItemsKey, tc.itemPolicies)
				}
				c.Data(http.StatusOK, "application/json", []byte(tc.result))
				cancel() // Completion must not inherit the execution deadline.
			}
			h.run(task.ID, service.PlatformOpenAI, taskCtx, recorder, cancel)
			got, err := tasks.Get(ctx, owner, task.ID)
			require.NoError(t, err)
			require.Equal(t, service.ImageTaskStatusCompleted, got.Status)
			if tc.wantDirect {
				require.Equal(t, tc.result, string(got.Result))
				require.Zero(t, storage.saves)
			} else if tc.itemPolicies == nil {
				require.Equal(t, 1, storage.saves)
				require.Contains(t, string(got.Result), "https://cdn.test/offloaded.png")
				require.NotContains(t, string(got.Result), "b64_json")
			} else {
				require.Equal(t, 1, storage.saves)
				items := gjson.GetBytes(got.Result, "data").Array()
				original := gjson.Get(tc.result, "data").Array()
				for i, direct := range tc.itemPolicies {
					if direct {
						require.JSONEq(t, original[i].Raw, items[i].Raw)
					} else {
						require.Equal(t, "https://cdn.test/offloaded.png", items[i].Get("url").String())
						require.False(t, items[i].Get("b64_json").Exists())
					}
				}
			}
			require.Zero(t, transport.downloads)

			// Polling serializes the same JSON result without changing its format.
			public, err := json.Marshal(got)
			require.NoError(t, err)
			require.Contains(t, string(public), `"result":`)
		})
	}
}
