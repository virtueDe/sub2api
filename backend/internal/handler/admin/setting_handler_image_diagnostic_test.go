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

func TestSettingsImageDiagnosticRoundTrip(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	read := func() string {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(c)
		require.Equal(t, http.StatusOK, rec.Code)
		return rec.Body.String()
	}
	initial := read()
	require.False(t, gjson.Get(initial, "data.image_diagnostic_enabled").Bool())
	require.JSONEq(t, "[]", gjson.Get(initial, "data.image_diagnostic_user_ids").Raw)
	updated := doUpdateSettings(t, h, map[string]any{"image_diagnostic_enabled": true, "image_diagnostic_user_ids": []int64{9, 7, 9}}, nil)
	require.Equal(t, http.StatusOK, updated.Code)
	require.True(t, gjson.Get(updated.Body.String(), "data.image_diagnostic_enabled").Bool())
	require.Equal(t, "true", repo.values[service.SettingKeyImageDiagnosticEnabled])
	require.JSONEq(t, "[7,9]", gjson.Get(read(), "data.image_diagnostic_user_ids").Raw)
	partial := doUpdateSettings(t, h, map[string]any{"site_name": "unchanged policy"}, nil)
	require.Equal(t, http.StatusOK, partial.Code)
	require.JSONEq(t, "[7,9]", repo.values[service.SettingKeyImageDiagnosticUserIDs])
	require.Equal(t, "true", repo.values[service.SettingKeyImageDiagnosticEnabled])
	disabled := doUpdateSettings(t, h, map[string]any{"image_diagnostic_enabled": false}, nil)
	require.Equal(t, http.StatusOK, disabled.Code)
	require.Equal(t, "false", repo.values[service.SettingKeyImageDiagnosticEnabled])
	require.JSONEq(t, "[7,9]", repo.values[service.SettingKeyImageDiagnosticUserIDs])
	cleared := doUpdateSettings(t, h, map[string]any{"image_diagnostic_enabled": true, "image_diagnostic_user_ids": []int64{}}, nil)
	require.Equal(t, http.StatusOK, cleared.Code)
	require.JSONEq(t, "[]", gjson.Get(read(), "data.image_diagnostic_user_ids").Raw)
}

func TestSettingsImageDiagnosticRejectsInvalidUsersWithoutWriting(t *testing.T) {
	tooMany := make([]int64, 51)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	for _, ids := range [][]int64{{0}, {-1}, {9007199254740992}, tooMany} {
		h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeyImageDiagnosticEnabled: "false", service.SettingKeyImageDiagnosticUserIDs: "[7]"})
		result := doUpdateSettings(t, h, map[string]any{"image_diagnostic_enabled": true, "image_diagnostic_user_ids": ids}, nil)
		require.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
		require.Equal(t, "false", repo.values[service.SettingKeyImageDiagnosticEnabled])
		require.Equal(t, "[7]", repo.values[service.SettingKeyImageDiagnosticUserIDs])
	}
	h, _ := newStepUpSwitchTestHandler(t, map[string]string{})
	result := doUpdateSettings(t, h, map[string]any{"image_diagnostic_enabled": true, "image_diagnostic_user_ids": tooMany[:50]}, nil)
	require.Equal(t, http.StatusOK, result.Code, result.Body.String())
}
