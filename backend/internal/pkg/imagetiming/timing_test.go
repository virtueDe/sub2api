package imagetiming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func observedContext() (context.Context, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	ctx := logger.IntoContext(context.Background(), zap.New(core).With(zap.String("request_id", "request-1"), zap.String("client_request_id", "client-1")))
	return Enable(ctx), logs
}

func stages(logs *observer.ObservedLogs, stage string) []observer.LoggedEntry {
	return logs.FilterMessage("image.timing").FilterField(zap.String("stage", stage)).All()
}

func TestHTTPSeparatesHeadersAndBodyAndRecordsNegotiatedProtocol(t *testing.T) {
	ctx, logs := observedContext()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	defer unblock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := HTTP(req, server.Client().Do)
	require.NoError(t, err)
	defer resp.Body.Close()
	headers := stages(logs, "http_headers")
	require.Len(t, headers, 1)
	require.Equal(t, "HTTP/2.0", headers[0].ContextMap()["http_protocol"])
	require.Empty(t, stages(logs, "response_body"))
	// The response has arrived but no body is available yet.
	time.AfterFunc(25*time.Millisecond, unblock)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"data":[]}`, string(data))
	require.NoError(t, resp.Body.Close())
	reads := stages(logs, "response_body")
	require.Len(t, reads, 1)
	fields := reads[0].ContextMap()
	require.Equal(t, "eof", fields["body_end"])
	require.EqualValues(t, len(data), fields["body_bytes"])
	require.GreaterOrEqual(t, fields["read_wait_ms"].(float64), 15.0)
	require.Equal(t, "request-1", fields["request_id"])
	require.Equal(t, "client-1", fields["client_request_id"])
	require.NotEmpty(t, stages(logs, "tls_handshake"))
}

func TestHTTPNoTraceIsTransparentAndNestedCallsAreNotDuplicated(t *testing.T) {
	originalBody := io.NopCloser(strings.NewReader("ok"))
	req, _ := http.NewRequest(http.MethodGet, "https://example.test/image", nil)
	response := &http.Response{StatusCode: 200, Proto: "HTTP/1.1", Body: originalBody}
	resp, err := HTTP(req, func(got *http.Request) (*http.Response, error) {
		require.Same(t, req, got)
		return response, nil
	})
	require.NoError(t, err)
	require.Equal(t, originalBody, resp.Body)
	ctx, logs := observedContext()
	resp, err = HTTP(req.WithContext(ctx), func(inner *http.Request) (*http.Response, error) {
		return HTTP(inner, func(*http.Request) (*http.Response, error) { return response, nil })
	})
	require.NoError(t, err)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Len(t, stages(logs, "http_headers"), 1)
	require.Len(t, stages(logs, "response_body"), 1)
}

func TestHTTPFailureDoesNotLogSecrets(t *testing.T) {
	ctx, logs := observedContext()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://user:private-password@example.test/private-object?signature=private-signature", nil)
	expected := errors.New("failed request with private-signature and private-password")
	_, err := HTTP(req, func(*http.Request) (*http.Response, error) { return nil, expected })
	require.ErrorIs(t, err, expected)
	for _, entry := range logs.All() {
		for _, field := range entry.ContextMap() {
			if value, ok := field.(string); ok {
				require.NotContains(t, value, "private-")
			}
		}
	}
	require.Equal(t, "error", stages(logs, "http_headers")[0].ContextMap()["outcome"])
}

type blockingBody struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (b *blockingBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.closed
	return 0, context.Canceled
}

func (b *blockingBody) Close() error { b.once.Do(func() { close(b.closed) }); return nil }

func TestBodyCloseUnblocksReadWithoutDoubleLogging(t *testing.T) {
	ctx, logs := observedContext()
	original := &blockingBody{entered: make(chan struct{}), closed: make(chan struct{})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/image", nil)
	resp, err := HTTP(req, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Proto: "HTTP/1.1", Body: original}, nil
	})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, readErr := resp.Body.Read(make([]byte, 1)); done <- readErr }()
	<-original.entered
	require.NoError(t, resp.Body.Close())
	select {
	case readErr := <-done:
		require.ErrorIs(t, readErr, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Read")
	}
	require.Len(t, stages(logs, "response_body"), 1)
	require.NotEqual(t, "eof", stages(logs, "response_body")[0].ContextMap()["body_end"])
	require.Equal(t, "error", stages(logs, "response_body")[0].ContextMap()["outcome"])
	require.Equal(t, "canceled", stages(logs, "response_body")[0].ContextMap()["error_kind"])
}

type slowWriter struct{ gin.ResponseWriter }

func (w slowWriter) Write(p []byte) (int, error) {
	time.Sleep(20 * time.Millisecond)
	return w.ResponseWriter.Write(p)
}

func TestGinMeasuresDownstreamWaitAndPreservesResponse(t *testing.T) {
	ctx, logs := observedContext()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
	c.Writer = slowWriter{c.Writer}
	finish := Begin(c, "image_request")
	c.Data(http.StatusOK, "application/json", []byte(`{"data":[]}`))
	finish()
	require.Equal(t, `{"data":[]}`, recorder.Body.String())
	require.Equal(t, 200, recorder.Code)
	writes := stages(logs, "downstream_write")
	require.Len(t, writes, 1)
	require.GreaterOrEqual(t, writes[0].ContextMap()["write_wait_ms"].(float64), 15.0)
}

func TestSpanOnceAndDetachedTaskCorrelation(t *testing.T) {
	ctx, logs := observedContext()
	ctx = WithFields(ctx, zap.String("task_id", "task-1"))
	ctx = context.WithoutCancel(ctx)
	span := Start(ctx, "result_store")
	span.End(context.DeadlineExceeded)
	span.End(nil)
	entries := stages(logs, "result_store")
	require.Len(t, entries, 1)
	require.Equal(t, "task-1", entries[0].ContextMap()["task_id"])
	require.Equal(t, "deadline_exceeded", entries[0].ContextMap()["error_kind"])
}

func TestHTTPFirstByteBeforeUploadCompletes(t *testing.T) {
	ctx, logs := observedContext()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/image", nil)
	_, err := HTTP(req, func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		trace.GotConn(httptrace.GotConnInfo{})
		trace.GotFirstResponseByte()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return &http.Response{StatusCode: 200}, nil
	})
	require.NoError(t, err)
	entries := stages(logs, "wait_first_response_byte")
	require.Len(t, entries, 1)
	require.Equal(t, true, entries[0].ContextMap()["early_response"])
	require.NotEqual(t, false, entries[0].ContextMap()["phase_completed"])
}

type failedWriter struct{ gin.ResponseWriter }

func (w failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestGinRequestReportsClientWriteFailure(t *testing.T) {
	ctx, logs := observedContext()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
	c.Writer = failedWriter{c.Writer}
	finish := Begin(c, "image_request")
	_, err := c.Writer.Write([]byte("image"))
	require.ErrorIs(t, err, io.ErrClosedPipe)
	finish()
	require.Equal(t, "error", stages(logs, "image_request")[0].ContextMap()["outcome"])
}

type closingDataBody struct {
	entered chan struct{}
	closed  chan struct{}
	release chan struct{}
}

func (b *closingDataBody) Read(p []byte) (int, error) {
	close(b.entered)
	<-b.closed
	<-b.release
	return copy(p, "data"), context.Canceled
}

func (b *closingDataBody) Close() error { close(b.closed); return nil }

func TestBodyCloseWaitsForActiveReadAccounting(t *testing.T) {
	ctx, logs := observedContext()
	original := &closingDataBody{entered: make(chan struct{}), closed: make(chan struct{}), release: make(chan struct{})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/image", nil)
	resp, err := HTTP(req, func(*http.Request) (*http.Response, error) { return &http.Response{Body: original}, nil })
	require.NoError(t, err)
	done := make(chan struct{})
	go func() { _, _ = resp.Body.Read(make([]byte, 8)); close(done) }()
	<-original.entered
	require.NoError(t, resp.Body.Close())
	require.Empty(t, stages(logs, "response_body"))
	time.AfterFunc(20*time.Millisecond, func() { close(original.release) })
	<-done
	entries := stages(logs, "response_body")
	require.Len(t, entries, 1)
	require.EqualValues(t, 4, entries[0].ContextMap()["body_bytes"])
	require.Equal(t, "canceled", entries[0].ContextMap()["error_kind"])
	require.Greater(t, entries[0].ContextMap()["read_wait_ms"].(float64), 0.0)
}

type slowFailedCloseBody struct {
	entered    chan struct{}
	closing    chan struct{}
	allowClose chan struct{}
}

func (b *slowFailedCloseBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.closing
	return 0, io.EOF
}

func (b *slowFailedCloseBody) Close() error {
	close(b.closing)
	<-b.allowClose
	return io.ErrClosedPipe
}

func TestBodyIncludesConcurrentCloseErrorAfterReadEOF(t *testing.T) {
	ctx, logs := observedContext()
	original := &slowFailedCloseBody{entered: make(chan struct{}), closing: make(chan struct{}), allowClose: make(chan struct{})}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/image", nil)
	resp, err := HTTP(req, func(*http.Request) (*http.Response, error) { return &http.Response{Body: original}, nil })
	require.NoError(t, err)
	readDone := make(chan struct{})
	go func() { _, _ = resp.Body.Read(make([]byte, 1)); close(readDone) }()
	<-original.entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- resp.Body.Close() }()
	<-readDone
	require.Empty(t, stages(logs, "response_body"))
	close(original.allowClose)
	require.ErrorIs(t, <-closeDone, io.ErrClosedPipe)
	entries := stages(logs, "response_body")
	require.Len(t, entries, 1)
	require.Equal(t, "error", entries[0].ContextMap()["outcome"])
}
