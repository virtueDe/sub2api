//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type displayHandlerSettings struct {
	service.SettingRepository
	value  string
	writes int
}

func (r *displayHandlerSettings) GetValue(context.Context, string) (string, error) {
	if r.value == "" {
		return "", service.ErrSettingNotFound
	}
	return r.value, nil
}
func (r *displayHandlerSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{service.SettingKeyModelPlazaEnabled: "true"}, nil
}
func (r *displayHandlerSettings) Set(_ context.Context, _ string, value string) error {
	r.value = value
	r.writes++
	return nil
}

type displayHandlerGroups struct{ service.GroupRepository }

func (displayHandlerGroups) ListActive(context.Context) ([]service.Group, error) {
	return []service.Group{
		{ID: 21, Name: "Selected", Platform: "openai", Status: service.StatusActive, RateMultiplier: 1},
		{ID: 22, Name: "Exclusive", Platform: "openai", IsExclusive: true, RateMultiplier: 1},
		{ID: 23, Name: "Not selected", Platform: "openai", RateMultiplier: 1},
	}, nil
}

func (r displayHandlerGroups) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	groups, err := r.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if group.ID == id {
			return &group, nil
		}
	}
	return nil, service.ErrGroupNotFound
}

type displayHandlerAccounts struct{ service.AccountRepository }

func (displayHandlerAccounts) ListModelAvailabilityCandidates(_ context.Context, groupID *int64, _ []string, _ bool) ([]service.Account, error) {
	if *groupID != 21 {
		return nil, nil
	}
	return []service.Account{{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": map[string]any{"custom-model": "upstream-model"}}}}, nil
}

func TestModelPlazaDisplayHandlers(t *testing.T) {
	repo := &displayHandlerSettings{}
	h := NewModelPlazaHandler(service.NewModelPlazaService(plazaVaryChannelRepo{}, displayHandlerGroups{}, nil, nil, nil, displayHandlerAccounts{}, nil), nil, service.NewSettingService(repo, nil))
	request := func(handler gin.HandlerFunc, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		return w
	}
	w := request(h.GetDisplayConfig, "")
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), "automatic")
	require.Zero(t, repo.writes)
	for _, body := range []string{`{`, `{"mode":"manual","groups":[{"group_id":999}]}`, `{"mode":"manual","groups":[{"group_id":21,"models":[{"name":"gpt-*","platform":"openai"}]}]}`} {
		w = request(h.UpdateDisplayConfig, body)
		require.Equal(t, 400, w.Code, w.Body.String())
		require.Zero(t, repo.writes)
	}
	w = request(h.UpdateDisplayConfig, `{"mode":"manual","groups":[{"group_id":21},{"group_id":22}]}`)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Equal(t, 1, repo.writes)
	w = request(h.Get, "")
	require.Equal(t, 200, w.Code, w.Body.String())
	var envelope struct {
		Data modelPlazaResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Groups, 1, "manual selection must still hide unauthorized exclusive groups")
	require.Equal(t, int64(21), envelope.Data.Groups[0].ID)
	require.Equal(t, "custom-model", envelope.Data.Groups[0].Models[0].Name)
	require.Equal(t, 1, repo.writes, "viewing the plaza is read-only")
	w = request(h.UpdateDisplayConfig, `{"mode":"manual","groups":[]}`)
	require.Equal(t, 200, w.Code)
	w = request(h.Get, "")
	require.Equal(t, 200, w.Code)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
	require.Empty(t, envelope.Data.Groups)
	repo.value = "invalid json"
	w = request(h.Get, "")
	require.Equal(t, 500, w.Code, "bad stored config must not broaden visibility")
}

func TestModelPlazaDisplayGroupModels(t *testing.T) {
	repo := &displayHandlerSettings{}
	h := NewModelPlazaHandler(service.NewModelPlazaService(nil, displayHandlerGroups{}, nil, nil, nil, displayHandlerAccounts{}, nil), nil, service.NewSettingService(repo, nil))
	r := gin.New()
	r.GET("/groups/:id/models", h.GetDisplayGroupModels)
	for _, tc := range []struct {
		id     string
		status int
	}{{"21", 200}, {"999", 404}, {"22", 404}, {"bad", 400}, {"0", 400}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/groups/"+tc.id+"/models", nil))
		require.Equal(t, tc.status, w.Code, w.Body.String())
		if w.Code == 200 {
			require.Contains(t, w.Body.String(), `"name":"custom-model"`)
			require.Contains(t, w.Body.String(), `"platform":"openai"`)
			require.NotContains(t, w.Body.String(), "upstream-model")
		}
	}
	require.Zero(t, repo.writes)
}
