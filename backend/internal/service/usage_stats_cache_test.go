package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestUsageStatsCacheMergesAndExpires(t *testing.T) {
	var cache usageStatsCache
	var calls atomic.Int64
	loader := func(context.Context) (int, error) { calls.Add(1); time.Sleep(80 * time.Millisecond); return 7, nil }
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := loadUsageStats(context.Background(), &cache, "same", 1, loader)
			require.NoError(t, err)
			require.Equal(t, 7, v)
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), calls.Load())
	cache.mu.Lock()
	cache.entries["same"] = usageStatsEntry{value: 7, expires: time.Now().Add(-time.Second)}
	cache.mu.Unlock()
	_, err := loadUsageStats(context.Background(), &cache, "same", 1, loader)
	require.NoError(t, err)
	require.Equal(t, int64(2), calls.Load())
}

func TestUsageStatsCacheLimitsDistinctQueriesWithoutCoolingBusyKeys(t *testing.T) {
	var cache usageStatsCache
	started := make(chan struct{}, usageStatsConcurrency)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < usageStatsConcurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_, err := loadUsageStats(context.Background(), &cache, fmt.Sprint(id), int64(id), func(context.Context) (int, error) { started <- struct{}{}; <-release; return 1, nil })
			require.NoError(t, err)
		}(i)
	}
	for i := 0; i < usageStatsConcurrency; i++ {
		<-started
	}
	_, err := loadUsageStats(context.Background(), &cache, "overflow", 99, func(context.Context) (int, error) { t.Error("overflow must not query DB"); return 0, nil })
	require.ErrorIs(t, err, errUsageStatsBusy)
	close(release)
	wg.Wait()
	value, err := loadUsageStats(context.Background(), &cache, "overflow", 99, func(context.Context) (int, error) { return 9, nil })
	require.NoError(t, err)
	require.Equal(t, 9, value)
}

func TestUsageStatsCacheTimeoutAndCancelledCaller(t *testing.T) {
	var cache usageStatsCache
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := loadUsageStats(ctx, &cache, "slow", 1, func(loadCtx context.Context) (int, error) { close(started); <-loadCtx.Done(); return 0, loadCtx.Err() })
		done <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	_, err := loadUsageStats(context.Background(), &cache, "slow", 1, func(context.Context) (int, error) { t.Error("shared query should still be running"); return 0, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	_, err = loadUsageStats(context.Background(), &cache, "different-range", 1, func(context.Context) (int, error) { t.Error("timeout must cool other ranges"); return 0, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestUsageStatsCacheFailureBackoffPreservesHealthyResults(t *testing.T) {
	var cache usageStatsCache
	ctx := context.Background()
	dbErr := errors.New("database unavailable")
	_, err := loadUsageStats(ctx, &cache, "healthy-range", 1, func(context.Context) ([]int, error) { return []int{}, nil })
	require.NoError(t, err)
	_, err = loadUsageStats(ctx, &cache, "failed-range", 1, func(context.Context) (int, error) { return 0, dbErr })
	require.ErrorIs(t, err, dbErr)
	retryAfter := cache.scopes[1].retryAfter
	for i := 0; i < 10; i++ {
		value, err := loadUsageStats(ctx, &cache, fmt.Sprint("new-range-", i), 1, func(context.Context) (int, error) { t.Error("must not retry during backoff"); return 9, nil })
		require.ErrorIs(t, err, dbErr)
		require.Zero(t, value)
	}
	require.Equal(t, retryAfter, cache.scopes[1].retryAfter, "polling must not extend cooldown")
	value, err := loadUsageStats(ctx, &cache, "healthy-range", 1, func(context.Context) ([]int, error) {
		t.Error("valid cached empty result must be served")
		return nil, dbErr
	})
	require.NoError(t, err)
	require.NotNil(t, value)
	_, err = loadUsageStats(ctx, &cache, "other-key", 2, func(context.Context) (int, error) { return 8, nil })
	require.NoError(t, err)

	cache.mu.Lock()
	cache.scopes[1] = usageStatsScopeState{err: dbErr, retryAfter: time.Now().Add(-time.Second)}
	cache.mu.Unlock()
	var calls atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := loadUsageStats(ctx, &cache, "recovery", 1, func(context.Context) (int, error) { calls.Add(1); close(started); <-release; return 9, nil })
		done <- err
	}()
	<-started
	for i := 0; i < 16; i++ {
		_, err = loadUsageStats(ctx, &cache, fmt.Sprint("probe-", i), 1, func(context.Context) (int, error) { calls.Add(1); return 10, nil })
		require.ErrorIs(t, err, errUsageStatsBusy)
	}
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, int64(1), calls.Load(), "only one range may probe a recovering key")
	otherRange, err := loadUsageStats(ctx, &cache, "next-range", 1, func(context.Context) (int, error) { return 10, nil })
	require.NoError(t, err)
	require.Equal(t, 10, otherRange, "different ranges must not share results")
}

func TestUsageStatsCacheCancelledWaiterDoesNotPoisonSuccessfulLoad(t *testing.T) {
	var cache usageStatsCache
	ctx, cancel := context.WithCancel(context.Background())
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := loadUsageStats(ctx, &cache, "shared", 1, func(context.Context) (int, error) { close(started); <-release; return 7, nil })
		done <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(release)
	value, err := loadUsageStats(context.Background(), &cache, "shared", 1, func(context.Context) (int, error) { t.Error("must join shared load or use its cache"); return 0, nil })
	require.NoError(t, err)
	require.Equal(t, 7, value)
	_, err = loadUsageStats(context.Background(), &cache, "next", 1, func(context.Context) (int, error) { return 8, nil })
	require.NoError(t, err)
}

func TestUsageStatsCacheBoundsFailureStateAndRecoversCapacity(t *testing.T) {
	var cache usageStatsCache
	dbErr := errors.New("db unavailable")
	for i := 0; i < usageStatsMaxEntries; i++ {
		_, err := loadUsageStats(context.Background(), &cache, fmt.Sprint(i), int64(i), func(context.Context) (int, error) { return 0, dbErr })
		require.ErrorIs(t, err, dbErr)
	}
	for i := usageStatsMaxEntries; i < usageStatsMaxEntries+10; i++ {
		_, err := loadUsageStats(context.Background(), &cache, fmt.Sprint(i), int64(i), func(context.Context) (int, error) {
			t.Error("full failure state must reject unknown scopes")
			return 0, nil
		})
		require.ErrorIs(t, err, errUsageStatsBusy)
	}
	require.Len(t, cache.scopes, usageStatsMaxEntries)
	require.Empty(t, cache.entries)
	cache.mu.Lock()
	cache.scopes[0] = usageStatsScopeState{err: dbErr, retryAfter: time.Now().Add(-time.Second)}
	cache.mu.Unlock()
	_, err := loadUsageStats(context.Background(), &cache, "admitted", -1, func(context.Context) (int, error) { return 7, nil })
	require.NoError(t, err)
	require.Less(t, len(cache.scopes), usageStatsMaxEntries)
}

type failingSummaryUsageRepo struct {
	UsageLogRepository
}

func (*failingSummaryUsageRepo) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return nil, context.DeadlineExceeded
}

func TestUsageServiceBackoffCoversAllStatisticsAndRollingRanges(t *testing.T) {
	svc := &UsageService{usageRepo: &failingSummaryUsageRepo{}}
	ctx := context.Background()
	_, err := svc.GetAPIKeyDashboardStats(ctx, 503)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	for i := 0; i < 3; i++ {
		end := time.Now().Add(time.Duration(i) * time.Minute)
		start := end.Add(-24 * time.Hour)
		// Unimplemented repository methods panic if admission reaches SQL.
		_, err = svc.GetAPIKeyModelStats(ctx, 503, start, end)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		_, err = svc.GetAPIKeyDailyUsage(ctx, 440, 503, start, end)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	}
}
