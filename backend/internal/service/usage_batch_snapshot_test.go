package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type batchSnapshotRepo struct {
	UsageLogRepository
	load func(context.Context, []int64, time.Time, time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error)
}

func (r *batchSnapshotRepo) GetBatchAPIKeyUsageStats(ctx context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	return r.load(ctx, ids, start, end)
}

func TestBatchSnapshotDeduplicatesAndRetainsBoundedStaleValues(t *testing.T) {
	var tick atomic.Int64
	tick.Store(time.Now().Truncate(time.Hour).Add(10 * time.Minute).UnixNano())
	var calls atomic.Int32
	var fail atomic.Bool
	r := &batchSnapshotRepo{load: func(ctx context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 8*time.Second)
		require.Equal(t, end.AddDate(0, 0, -30), start)
		if fail.Load() {
			return nil, context.DeadlineExceeded
		}
		return map[int64]*usagestats.BatchAPIKeyUsageStats{7: {APIKeyID: 7, TotalActualCost: 12}, 9: {APIKeyID: 9}}, nil
	}}
	s := NewUsageService(r, nil, nil, nil)
	t.Cleanup(s.Stop)
	s.batchStats.now = func() time.Time { return time.Unix(0, tick.Load()) }
	ctx := context.Background()
	first := s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{9, 7, 7})
	require.Equal(t, "pending", first.Statistics.State)
	require.Empty(t, first.Stats)
	waitGatewayStatistics(t, &s.batchStats)
	fresh := s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{7, 9})
	require.Equal(t, "fresh", fresh.Statistics.State)
	require.Equal(t, int32(1), calls.Load())
	require.Zero(t, fresh.Stats[9].TotalActualCost, "successful zero must be preserved")
	fresh.Stats[7].TotalActualCost = 99
	require.Equal(t, 12.0, s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{9, 7}).Stats[7].TotalActualCost)
	fail.Store(true)
	tick.Add(int64(31 * time.Second))
	stale := s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{9, 7})
	require.Equal(t, "stale", stale.Statistics.State)
	require.True(t, stale.Statistics.Refreshing)
	waitGatewayStatistics(t, &s.batchStats)
	after := s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{7, 9})
	require.Equal(t, stale.Statistics.UpdatedAt, after.Statistics.UpdatedAt)
	require.False(t, after.Statistics.Refreshing)
	tick.Add(int64(5 * time.Minute))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.Empty(t, s.GetBatchAPIKeyUsageSnapshot(cancelled, 1, []int64{7, 9}).Stats)
	require.Empty(t, s.GetBatchAPIKeyUsageSnapshot(cancelled, 2, []int64{7, 9}).Stats, "user must be part of cache identity")
}

func TestBatchSnapshotColdRequestCancellationDoesNotCancelSharedRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s := NewUsageService(&batchSnapshotRepo{load: func(ctx context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
		return map[int64]*usagestats.BatchAPIKeyUsageStats{7: {APIKeyID: 7}}, nil
	}}, nil, nil, nil)
	t.Cleanup(s.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{7})
	require.Less(t, time.Since(start), 100*time.Millisecond)
	<-entered
	cancel()
	for i := 0; i < 10; i++ {
		s.GetBatchAPIKeyUsageSnapshot(context.Background(), 1, []int64{7})
	}
	close(release)
	waitGatewayStatistics(t, &s.batchStats)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "fresh", s.GetBatchAPIKeyUsageSnapshot(context.Background(), 1, []int64{7}).Statistics.State)
}

func TestBatchSnapshotStopsWorkerAndBoundsQueue(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int32
	s := NewUsageService(&batchSnapshotRepo{load: func(ctx context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}, nil, nil, nil)
	s.GetBatchAPIKeyUsageSnapshot(context.Background(), 1, []int64{1})
	<-entered
	for i := int64(2); i < 20; i++ {
		s.GetBatchAPIKeyUsageSnapshot(context.Background(), i, []int64{i})
	}
	s.batchStats.mu.Lock()
	queued := len(s.batchStats.queue)
	s.batchStats.mu.Unlock()
	require.Equal(t, 8, queued)
	s.Stop()
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, "unavailable", s.GetBatchAPIKeyUsageSnapshot(context.Background(), 99, []int64{99}).Statistics.State)
}

func TestBatchSnapshotDiscardsWorkAcrossMidnight(t *testing.T) {
	var tick atomic.Int64
	tick.Store(time.Now().UnixNano())
	s := NewUsageService(&batchSnapshotRepo{load: func(ctx context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
		tick.Add(int64(24 * time.Hour))
		return map[int64]*usagestats.BatchAPIKeyUsageStats{7: {APIKeyID: 7, TodayActualCost: 10}}, nil
	}}, nil, nil, nil)
	t.Cleanup(s.Stop)
	s.batchStats.now = func() time.Time { return time.Unix(0, tick.Load()) }
	s.GetBatchAPIKeyUsageSnapshot(context.Background(), 1, []int64{7})
	waitGatewayStatistics(t, &s.batchStats)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Empty(t, s.GetBatchAPIKeyUsageSnapshot(ctx, 1, []int64{7}).Stats)
}
