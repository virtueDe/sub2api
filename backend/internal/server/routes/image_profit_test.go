package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestImageProfitRoutesRequireAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &handler.Handlers{Admin: &handler.AdminHandlers{Account: &adminhandler.AccountHandler{}, OpenAIOAuth: &adminhandler.OpenAIOAuthHandler{}, ImageProfit: adminhandler.NewImageProfitHandler(nil)}}
	auth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		status := http.StatusForbidden
		if c.GetHeader("Authorization") == "" {
			status = http.StatusUnauthorized
		}
		c.AbortWithStatus(status)
	})
	RegisterAdminRoutes(r.Group("/api/v1"), h, auth, middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), middleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }), nil, nil)
	for _, route := range []struct{ method, path string }{{"GET", "/prices"}, {"POST", "/prices"}, {"GET", "/reports"}, {"GET", "/reports/1"}, {"POST", "/recalculate"}} {
		for _, header := range []string{"", "Bearer ordinary-user"} {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(route.method, "/api/v1/admin/profit-accounting"+route.path, nil)
			req.Header.Set("Authorization", header)
			r.ServeHTTP(w, req)
			want := 403
			if header == "" {
				want = 401
			}
			require.Equal(t, want, w.Code, route.path)
		}
	}
}
