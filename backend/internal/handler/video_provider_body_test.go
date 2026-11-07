//go:build unit

package handler

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestProviderVideosRequestBodyLimits(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxBody    int64
		budget     int64
		wantStatus int
		wantRetry  string
	}{
		{"size limit", 8, 1024, http.StatusRequestEntityTooLarge, ""},
		{"memory budget", 1024, 1, http.StatusServiceUnavailable, "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(middleware.RequestBodyLimit(tc.maxBody, pkghttputil.NewBodyMemoryBudget(tc.budget)))
			router.POST("/v1/videos", (&OpenAIGatewayHandler{}).ProviderVideos)
			req := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"wan2.6-t2v","prompt":"cat"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			require.Equal(t, tc.wantRetry, rec.Header().Get("Retry-After"))
		})
	}
}

func TestProviderVideosRequestBodyReadFailures(t *testing.T) {
	timeoutErr := &net.OpError{Op: "read", Net: "tcp", Source: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 8080}, Err: os.ErrDeadlineExceeded}
	for _, tc := range []struct {
		name       string
		readErr    error
		wantStatus int
		wantText   string
	}{
		{"timeout", timeoutErr, http.StatusRequestTimeout, "Timed out reading request body"},
		{"truncated", io.ErrUnexpectedEOF, http.StatusBadRequest, "Failed to read request body"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", iotest.ErrReader(tc.readErr))
			(&OpenAIGatewayHandler{}).ProviderVideos(c)

			require.Equal(t, tc.wantStatus, writer.Code, writer.Body.String())
			require.Contains(t, writer.Body.String(), tc.wantText)
			require.NotContains(t, writer.Body.String(), "10.0.0.1")
		})
	}
}
