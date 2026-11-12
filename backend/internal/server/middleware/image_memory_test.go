package middleware

import (
	"context"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type observedImageReader struct {
	io.Reader
	reads atomic.Int32
}

func (r *observedImageReader) Read(p []byte) (int, error) { r.reads.Add(1); return r.Reader.Read(p) }

func TestImageMemoryMiddlewareWaitsBeforeReadAndCancels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	b := pkghttputil.NewBodyMemoryBudget(32 << 20)
	base, closeBase := pkghttputil.WithRequestBodyMemoryBudget(context.Background(), b)
	defer closeBase()
	_, release, err := pkghttputil.AdmitImageRequestMemory(base, 1, -1, false)
	require.NoError(t, err)
	defer release()
	r := gin.New()
	r.Use(RequestBodyLimit(64<<20, b), func(c *gin.Context) { c.Set(string(ContextKeyUser), AuthSubject{UserID: 2}); c.Next() }, ImageRequestMemory())
	r.POST("/v1/images/edits", func(c *gin.Context) {
		_, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err == nil {
			c.Status(200)
		}
	})
	reader := &observedImageReader{Reader: strings.NewReader("payload")}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", reader).WithContext(ctx)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { r.ServeHTTP(w, req); close(done) }()
	select {
	case <-done:
		t.Fatal("request bypassed capacity")
	case <-time.After(20 * time.Millisecond):
	}
	require.Zero(t, reader.reads.Load())
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not remove waiter")
	}
	require.Equal(t, 499, w.Code)
	require.Zero(t, reader.reads.Load())
}

func TestImageMemoryAdmissionDeadlineDoesNotCancelRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestBodyLimit(8<<20, pkghttputil.NewBodyMemoryBudget(32<<20)), func(c *gin.Context) { c.Set(string(ContextKeyUser), AuthSubject{UserID: 1}); c.Next() }, ImageRequestMemory())
	r.POST("/images/generations", func(c *gin.Context) {
		require.NoError(t, c.Request.Context().Err())
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		require.NoError(t, err)
		c.Data(200, "application/json", body)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/images/generations", strings.NewReader(`{"prompt":"hello"}`)))
	require.Equal(t, 200, w.Code)
	require.JSONEq(t, `{"prompt":"hello"}`, w.Body.String())
}
