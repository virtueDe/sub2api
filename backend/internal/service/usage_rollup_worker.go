package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

type UsageRollupStatus struct {
	PendingEvents         int64
	PendingHours          int64
	OldestEventAgeSeconds float64
}
type UsageRollupRepository interface {
	Step(context.Context) error
	Status(context.Context) (UsageRollupStatus, error)
	Reconcile(context.Context) (string, error)
}

type UsageRollupWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func ProvideUsageRollupWorker(repo UsageRollupRepository, cfg *config.Config) *UsageRollupWorker {
	w := &UsageRollupWorker{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	if !cfg.Database.UsageRollupEnabled {
		close(w.done)
		return w
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastLog time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			err := repo.Step(ctx)
			if time.Since(lastLog) < time.Minute {
				continue
			}
			lastLog = time.Now()
			if err != nil && !errors.Is(err, dbstats.ErrBusy) && ctx.Err() == nil {
				logger.L().Warn("usage.rollup_worker_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
			}
			statusCtx, stop := context.WithTimeout(ctx, time.Second)
			status, statusErr := repo.Status(statusCtx)
			stop()
			if statusErr == nil {
				logger.L().Info("usage.rollup_progress", zap.Int64("pending_events", status.PendingEvents), zap.Int64("pending_hours", status.PendingHours), zap.Float64("oldest_event_age_seconds", status.OldestEventAgeSeconds), zap.Bool(logger.OpsSystemLogSkipField, true))
			}
			result, checkErr := repo.Reconcile(ctx)
			if checkErr == nil {
				logger.L().Info("usage.rollup_reconciliation", zap.String("result", result), zap.Bool(logger.OpsSystemLogSkipField, true))
			} else if !errors.Is(checkErr, dbstats.ErrBusy) && ctx.Err() == nil {
				logger.L().Warn("usage.rollup_reconciliation_failed", zap.Error(checkErr), zap.Bool(logger.OpsSystemLogSkipField, true))
			}
		}
	}()
	return w
}
func (w *UsageRollupWorker) Stop() {
	if w != nil {
		w.once.Do(func() { w.cancel(); <-w.done })
	}
}
