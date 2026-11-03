package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
			v, err := loadUsageStats(context.Background(), &cache, "same", loader)
			require.NoError(t, err)
			require.Equal(t, 7, v)
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), calls.Load())
	cache.mu.Lock()
	cache.entries["same"] = usageStatsEntry{value: 7, expires: time.Now().Add(-time.Second)}
	cache.mu.Unlock()
	_, err := loadUsageStats(context.Background(), &cache, "same", loader)
	require.NoError(t, err)
	require.Equal(t, int64(2), calls.Load())
}

func TestUsageStatsCacheLimitsDistinctQueriesAndDoesNotCacheErrors(t *testing.T) {
	var cache usageStatsCache
	started := make(chan struct{}, usageStatsConcurrency)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < usageStatsConcurrency; i++ {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := loadUsageStats(context.Background(), &cache, key, func(context.Context) (int, error) { started <- struct{}{}; <-release; return 1, nil })
			require.NoError(t, err)
		}(fmt.Sprint(i))
	}
	for i := 0; i < usageStatsConcurrency; i++ {
		<-started
	}
	_, err := loadUsageStats(context.Background(), &cache, "overflow", func(context.Context) (int, error) { t.Error("overflow must not query DB"); return 0, nil })
	require.ErrorIs(t, err, errUsageStatsBusy)
	close(release)
	wg.Wait()
	_, err = loadUsageStats(context.Background(), &cache, "overflow", func(context.Context) (int, error) { return 0, errors.New("db failure") })
	require.Error(t, err)
	value, err := loadUsageStats(context.Background(), &cache, "overflow", func(context.Context) (int, error) { return 9, nil })
	require.NoError(t, err)
	require.Equal(t, 9, value)
}

func TestUsageStatsCacheTimeoutAndCancelledCaller(t *testing.T) {
	var cache usageStatsCache
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := loadUsageStats(ctx, &cache, "slow", func(loadCtx context.Context) (int, error) { close(started); <-loadCtx.Done(); return 0, loadCtx.Err() })
		done <- err
	}()
	<-started
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	_, err := loadUsageStats(context.Background(), &cache, "slow", func(context.Context) (int, error) { t.Error("shared query should still be running"); return 0, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
