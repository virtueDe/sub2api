// Package dbstats bounds derived reporting work before it acquires a database
// connection. Billing, authorization and scheduling quotas do not use this gate.
package dbstats

import (
	"context"
	"sync/atomic"
	"time"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

var ErrBusy = apperrors.ServiceUnavailable("STATISTICS_BUSY", "Statistics are busy; retry later")

// Default is shared by reporting and maintenance repositories in this process.
// A deployment with multiple replicas must budget their combined concurrency.
var Default = NewBudget(1)

const ReadTimeout = 2 * time.Second
const BatchReadTimeout = 10 * time.Second
const MaintenanceTimeout = 30 * time.Second

type Budget struct {
	slots          chan struct{}
	started        atomic.Uint64
	rejected       atomic.Uint64
	failed         atomic.Uint64
	durationMS     atomic.Uint64
	lastLog        atomic.Int64
	batchReadAfter atomic.Int64
}

func NewBudget(concurrency int) *Budget {
	if concurrency < 1 {
		panic("statistics concurrency must be positive")
	}
	return &Budget{slots: make(chan struct{}, concurrency)}
}

// Run holds admission through row consumption and transaction cleanup. The
// callback must not acquire this budget again; repository decorators only wrap
// outer calls, while internal sequential calls stay on the undecorated receiver.
func Run[T any](ctx context.Context, b *Budget, timeout time.Duration, load func(context.Context) (T, error)) (value T, err error) {
	if err = ctx.Err(); err != nil {
		return value, err
	}
	if b == nil {
		b = Default
	}
	select {
	case b.slots <- struct{}{}:
	default:
		b.rejected.Add(1)
		b.logSnapshot()
		return value, ErrBusy
	}
	b.started.Add(1)
	started := time.Now()
	defer func() {
		<-b.slots
		b.durationMS.Add(uint64(time.Since(started).Milliseconds()))
		if err != nil {
			b.failed.Add(1)
		}
		b.logSnapshot()
	}()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return load(ctx)
}

func RunMaintenance(ctx context.Context, b *Budget, load func(context.Context) error) error {
	_, err := Run(ctx, b, MaintenanceTimeout, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, load(ctx)
	})
	return err
}

// Long batch reads share the existing admission slot. Leave two worker ticks
// between them so reporting traffic cannot continuously occupy maintenance's slot.
func RunBatchRead[T any](ctx context.Context, b *Budget, load func(context.Context) (T, error)) (value T, err error) {
	if err = ctx.Err(); err != nil {
		return value, err
	}
	if b == nil {
		b = Default
	}
	if time.Now().UnixNano() < b.batchReadAfter.Load() {
		return value, ErrBusy
	}
	return Run(ctx, b, BatchReadTimeout, func(ctx context.Context) (T, error) {
		if time.Now().UnixNano() < b.batchReadAfter.Load() {
			return value, ErrBusy
		}
		defer func() { b.batchReadAfter.Store(time.Now().Add(2 * time.Second).UnixNano()) }()
		return load(ctx)
	})
}

func (b *Budget) logSnapshot() {
	now := time.Now().Unix()
	last := b.lastLog.Load()
	if now-last < 60 || !b.lastLog.CompareAndSwap(last, now) {
		return
	}
	logger.L().Info("database.statistics_budget",
		zap.Int("active", len(b.slots)), zap.Int("capacity", cap(b.slots)),
		zap.Uint64("started_total", b.started.Load()), zap.Uint64("rejected_total", b.rejected.Load()),
		zap.Uint64("failed_total", b.failed.Load()), zap.Uint64("duration_ms_total", b.durationMS.Load()),
		zap.Bool(logger.OpsSystemLogSkipField, true))
}
