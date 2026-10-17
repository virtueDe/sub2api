package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSettingsImageForceURLResponseRoundTrip(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	read := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(c)
		require.Equal(t, http.StatusOK, rec.Code)
		return rec
	}
	require.False(t, gjson.Get(read().Body.String(), "data.image_force_url_response_enabled").Bool())
	updated := doUpdateSettings(t, h, map[string]any{
		"image_force_url_response_enabled":     true,
		"image_force_url_response_account_ids": []int64{9, 7, 9, -1},
	}, nil)
	require.Equal(t, http.StatusOK, updated.Code)
	require.True(t, gjson.Get(updated.Body.String(), "data.image_force_url_response_enabled").Bool())
	require.Equal(t, "true", repo.values[service.SettingKeyImageForceURLResponseEnabled])
	require.JSONEq(t, "[7,9]", repo.values[service.SettingKeyImageForceURLResponseAccountIDs])
	require.JSONEq(t, "[7,9]", gjson.Get(read().Body.String(), "data.image_force_url_response_account_ids").Raw)

	// An unrelated settings save must retain the account policy.
	partial := doUpdateSettings(t, h, map[string]any{"site_name": "kept"}, nil)
	require.Equal(t, http.StatusOK, partial.Code)
	require.Equal(t, "true", repo.values[service.SettingKeyImageForceURLResponseEnabled])
	require.JSONEq(t, "[7,9]", repo.values[service.SettingKeyImageForceURLResponseAccountIDs])

	cleared := doUpdateSettings(t, h, map[string]any{
		"image_force_url_response_enabled":     false,
		"image_force_url_response_account_ids": []int64{},
	}, nil)
	require.Equal(t, http.StatusOK, cleared.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyImageForceURLResponseEnabled])
	require.JSONEq(t, "[]", repo.values[service.SettingKeyImageForceURLResponseAccountIDs])
}
