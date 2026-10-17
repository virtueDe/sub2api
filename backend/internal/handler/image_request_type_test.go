package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageRequestTypeErrors(t *testing.T) {
	for _, tc := range []struct {
		path  string
		async bool
		want  service.RequestType
	}{
		{"/v1/images/generations", false, service.RequestTypeImageSync},
		{"/v1/images/edits/async", false, service.RequestTypeImageAsync},
		{"/v1/images/generations", true, service.RequestTypeImageAsync},
		{"/v1/images/batches", false, service.RequestTypeImageAsync},
		{"/v1/chat/completions", false, service.RequestTypeStream},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
		c.Set(opsRequestTypeKey, int16(service.RequestTypeStream))
		if tc.async {
			c.Request = c.Request.WithContext(service.WithAsyncImageTaskExecution(context.Background()))
		}
		got := opsRequestTypeFromContext(c)
		require.NotNil(t, got)
		require.Equal(t, int16(tc.want), *got)
	}
}

func TestUserUsageImageRequestTypeFilters(t *testing.T) {
	for _, kind := range []service.RequestType{service.RequestTypeImageSync, service.RequestTypeImageAsync} {
		for _, endpoint := range []string{"/usage", "/usage/stats"} {
			repo := &userUsageRepoCapture{}
			router := newUserUsageRequestTypeTestRouter(repo)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"?request_type="+kind.String()+"&stream=bad", nil))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			filters := repo.listFilters
			if endpoint == "/usage/stats" {
				filters = repo.statsFilters
			}
			require.NotNil(t, filters.RequestType)
			require.Equal(t, int16(kind), *filters.RequestType)
			require.Nil(t, filters.Stream)
		}
	}
}
