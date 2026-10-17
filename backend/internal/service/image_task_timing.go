package service

import (
	"context"
	"errors"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagetiming"
	"go.uber.org/zap"
)

type imageTaskTimingKey struct{}

type imageTaskTiming struct {
	mu         sync.Mutex
	status     string
	statusCode int
	err        error
}

// BeginImageTaskTiming tracks the background lifetime and its final business
// outcome without changing Complete/Fail's persistence-error return contract.
func BeginImageTaskTiming(ctx context.Context) (context.Context, func(error)) {
	if !imagetiming.Enabled(ctx) {
		return ctx, func(error) {}
	}
	span := imagetiming.Start(ctx, "async_task")
	state := &imageTaskTiming{status: ImageTaskStatusProcessing}
	ctx = context.WithValue(span.Context(ctx), imageTaskTimingKey{}, state)
	return ctx, func(err error) {
		state.mu.Lock()
		defer state.mu.Unlock()
		err = errors.Join(err, state.err)
		if err == nil && state.status != ImageTaskStatusCompleted {
			err = errors.New("image task did not complete")
		}
		span.End(err, zap.String("task_status", state.status), zap.Int("task_http_status", state.statusCode))
	}
}

func recordImageTaskTimingOutcome(ctx context.Context, status string, statusCode int, err error) {
	state, _ := ctx.Value(imageTaskTimingKey{}).(*imageTaskTiming)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	state.status, state.statusCode, state.err = status, statusCode, err
}
