package service

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

type billingLoadMetrics struct {
	cacheHits, cacheMisses, loads, failures, rejected, durationNS atomic.Uint64
	lastLog                                                       atomic.Int64
}

// BillingBalanceReader keeps the hot-path read separate from full user loading.
type BillingBalanceReader interface {
	GetBalance(context.Context, int64) (float64, error)
}

// Only the leader accesses the database and reports breaker outcomes. A caller
// cancellation stops that caller's wait without canceling the shared lookup.
func (s *BillingCacheService) loadBillingData(ctx context.Context, group *singleflight.Group, key string, load func(context.Context) (any, error)) (any, error) {
	ch := group.DoChan(key, func() (any, error) {
		if s.circuitBreaker != nil && !s.circuitBreaker.Allow() {
			s.loadMetrics.rejected.Add(1)
			s.logBillingLoads()
			return nil, ErrBillingServiceUnavailable
		}
		loadCtx, cancel := context.WithTimeout(context.Background(), balanceLoadTimeout)
		defer cancel()
		start := time.Now()
		s.loadMetrics.loads.Add(1)
		value, err := load(loadCtx)
		s.loadMetrics.durationNS.Add(uint64(time.Since(start)))
		// Missing users/subscriptions are business rejections, not DB outages.
		dependencyFailed := err != nil && !errors.Is(err, ErrUserNotFound) && !errors.Is(err, ErrSubscriptionNotFound)
		if dependencyFailed {
			s.loadMetrics.failures.Add(1)
			dbstats.Record("billing_load", time.Since(start), errors.Join(err, loadCtx.Err()))
			dbstats.TriggerBillingFailure()
		} else {
			dbstats.Record("billing_load", time.Since(start), nil)
		}
		if s.circuitBreaker != nil {
			if dependencyFailed {
				s.circuitBreaker.OnFailure(err)
			} else {
				s.circuitBreaker.OnSuccess()
			}
		}
		s.logBillingLoads()
		return value, err
	})
	select {
	case result := <-ch:
		return result.Val, result.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *BillingCacheService) logBillingLoads() {
	now := time.Now().Unix()
	last := s.loadMetrics.lastLog.Load()
	if now-last < 60 || !s.loadMetrics.lastLog.CompareAndSwap(last, now) {
		return
	}
	logger.L().Info("billing.database_loads",
		zap.Uint64("cache_hits_total", s.loadMetrics.cacheHits.Load()),
		zap.Uint64("cache_misses_total", s.loadMetrics.cacheMisses.Load()),
		zap.Uint64("database_loads_total", s.loadMetrics.loads.Load()),
		zap.Uint64("database_failures_total", s.loadMetrics.failures.Load()),
		zap.Uint64("circuit_rejected_loads_total", s.loadMetrics.rejected.Load()),
		zap.Uint64("database_duration_ms_total", s.loadMetrics.durationNS.Load()/uint64(time.Millisecond)),
		zap.Bool(logger.OpsSystemLogSkipField, true))
}
