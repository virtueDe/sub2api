package imagetiming

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Begin covers a handler invocation, including writes into async/Gemini
// recorders. Nested handlers on the same writer do not double-count writes.
func Begin(c *gin.Context, operation string) func() {
	c.Request = c.Request.WithContext(Enable(c.Request.Context()))
	ctx := c.Request.Context()
	span := Start(ctx, operation)
	c.Request = c.Request.WithContext(span.Context(ctx))
	if w, already := c.Writer.(*writer); already {
		return func() {
			w.mu.Lock()
			err := w.writeErr
			w.mu.Unlock()
			span.End(errors.Join(err, statusError(c.Writer.Status())), zap.Int("http_status", c.Writer.Status()))
		}
	}
	w := &writer{ResponseWriter: c.Writer, ctx: c.Request.Context()}
	c.Writer = w
	return func() {
		n, err := w.finish()
		span.End(errors.Join(err, statusError(c.Writer.Status())), zap.Int("http_status", c.Writer.Status()), zap.Int64("response_bytes", n))
	}
}

type writer struct {
	gin.ResponseWriter
	ctx            context.Context
	mu             sync.Mutex
	writeSpan      *Span
	bytes          int64
	writes         int
	writeTime      time.Duration
	flushTime      time.Duration
	writeErr       error
	heartbeatBytes int64
}

func (w *writer) Write(p []byte) (int, error) {
	w.beginWrite()
	start := time.Now()
	n, err := w.ResponseWriter.Write(p)
	w.recordWrite(start, n, err, len(p) == 2 && p[0] == ' ' && p[1] == '\n')
	return n, err
}

func (w *writer) beginWrite() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writeSpan == nil {
		w.writeSpan = Start(w.ctx, "downstream_write")
	}
}

func (w *writer) recordWrite(start time.Time, n int, err error, heartbeat bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeTime += time.Since(start)
	w.bytes += int64(n)
	w.writes++
	if heartbeat {
		w.heartbeatBytes += int64(n)
	}
	if err != nil {
		w.writeErr = err
	}
}

func (w *writer) WriteString(s string) (int, error) {
	w.beginWrite()
	start := time.Now()
	n, err := w.ResponseWriter.WriteString(s)
	w.recordWrite(start, n, err, s == " \n")
	return n, err
}

func (w *writer) Flush() {
	w.beginWrite()
	start := time.Now()
	w.ResponseWriter.Flush()
	w.mu.Lock()
	w.flushTime += time.Since(start)
	w.mu.Unlock()
}

func (w *writer) finish() (int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Accumulated blocking time is separate from the wall time of a stream.
	w.writeSpan.End(w.writeErr, zap.Float64("write_wait_ms", milliseconds(w.writeTime)),
		zap.Float64("flush_wait_ms", milliseconds(w.flushTime)), zap.Int64("body_bytes", w.bytes),
		zap.Int("write_count", w.writes), zap.Int64("heartbeat_bytes", w.heartbeatBytes))
	return w.bytes, w.writeErr
}

func statusError(status int) error {
	if status >= http.StatusBadRequest {
		return errors.New("image request failed")
	}
	return nil
}
