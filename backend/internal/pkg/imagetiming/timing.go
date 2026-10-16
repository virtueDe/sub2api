// Package imagetiming records image request stages without retaining payloads,
// credentials, object keys, or signed URLs. Parent and child spans overlap;
// their durations must not be added together.
package imagetiming

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

type contextKey struct{}
type fieldsKey struct{}
type parentKey struct{}

type trace struct {
	started time.Time
	next    atomic.Int64
}

// Enable preserves an existing trace across retries and asynchronous execution.
func Enable(ctx context.Context) context.Context {
	if !Enabled(ctx) {
		return context.WithValue(ctx, contextKey{}, &trace{started: time.Now()})
	}
	return ctx
}

func Enabled(ctx context.Context) bool {
	return ctx != nil && ctx.Value(contextKey{}) != nil
}

func WithFields(ctx context.Context, fields ...zap.Field) context.Context {
	if !Enabled(ctx) {
		return ctx
	}
	previous, _ := ctx.Value(fieldsKey{}).([]zap.Field)
	combined := make([]zap.Field, 0, len(previous)+len(fields))
	combined = append(combined, previous...)
	combined = append(combined, fields...)
	return context.WithValue(ctx, fieldsKey{}, combined)
}

// Span emits a start event so a still-running request can also be diagnosed.
type Span struct {
	ctx     context.Context
	started time.Time
	id      int64
	fields  []zap.Field
	once    sync.Once
}

func Start(ctx context.Context, stage string, fields ...zap.Field) *Span {
	if !Enabled(ctx) {
		return nil
	}
	t := ctx.Value(contextKey{}).(*trace)
	s := &Span{ctx: ctx, started: time.Now(), id: t.next.Add(1)}
	s.fields = append(s.fields, zap.String("stage", stage), zap.Int64("span_id", s.id),
		zap.Float64("start_offset_ms", milliseconds(s.started.Sub(t.started))), zap.Bool(logger.OpsSystemLogSkipField, true))
	if parent, ok := ctx.Value(parentKey{}).(int64); ok {
		s.fields = append(s.fields, zap.Int64("parent_span_id", parent))
	}
	if account, ok := ctx.Value(ctxkey.AccountID).(int64); ok {
		s.fields = append(s.fields, zap.Int64("account_id", account))
	}
	metadata, _ := ctx.Value(fieldsKey{}).([]zap.Field)
	s.fields = append(s.fields, metadata...)
	s.fields = append(s.fields, fields...)
	logger.FromContext(ctx).Info("image.timing.start", s.fields...)
	return s
}

func (s *Span) Context(ctx context.Context) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, parentKey{}, s.id)
}

func (s *Span) End(err error, fields ...zap.Field) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		all := append([]zap.Field(nil), s.fields...)
		all = append(all, zap.Float64("duration_ms", milliseconds(time.Since(s.started))), zap.String("outcome", outcome))
		if err != nil {
			all = append(all, zap.String("error_kind", errorKind(err)))
		}
		all = append(all, fields...)
		logger.FromContext(s.ctx).Info("image.timing", all...)
	})
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Error strings can contain authorization data and signed image URLs.
func errorKind(err error) string {
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "network_timeout"
	}
	return "operation_failed"
}
