package service

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func waitGatewayStatistics(t *testing.T, c *gatewayStatisticsCache) {
	t.Helper()
	require.Eventually(t, func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.running }, time.Second, time.Millisecond)
}

func TestGatewayStatisticsFailureCooldownCoversQueuedAndNewRanges(t *testing.T) {
	var tick atomic.Int64
	tick.Store(time.Now().UnixNano())
	c := gatewayStatisticsCache{now: func() time.Time { return time.Unix(0, tick.Load()) }}
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	failed := gatewayStatsSpec{"model_stats", "model:1:a:b", time.Minute, 3 * time.Minute, func(context.Context) (any, error) {
		calls.Add(1)
		close(entered)
		<-release
		return nil, context.DeadlineExceeded
	}}
	c.read(context.Background(), []gatewayStatsSpec{failed})
	<-entered
	next := gatewayStatsSpec{"model_stats", "model:1:c:d", time.Minute, 3 * time.Minute, func(context.Context) (any, error) { calls.Add(1); return 7, nil }}
	c.read(context.Background(), []gatewayStatsSpec{next})
	close(release)
	waitGatewayStatistics(t, &c)
	require.Equal(t, int32(1), calls.Load(), "queued ranges obey a newly established cooldown")
	for i := 0; i < 29; i++ {
		tick.Add(int64(time.Second))
		next.key = fmt.Sprintf("model:1:%d:z", i)
		require.Equal(t, "unavailable", c.read(context.Background(), []gatewayStatsSpec{next})["model_stats"].freshness.State)
	}
	tick.Add(int64(time.Second))
	c.read(context.Background(), []gatewayStatsSpec{next})
	waitGatewayStatistics(t, &c)
	require.Equal(t, int32(2), calls.Load(), "polling must not extend failure cooldown")
	require.Equal(t, "fresh", c.read(context.Background(), []gatewayStatsSpec{next})["model_stats"].freshness.State)
}

func TestGatewayStatisticsRejectsTodayAcrossMidnight(t *testing.T) {
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	now := day
	calls := 0
	load := func(context.Context) (*usagestats.UserDashboardStats, error) {
		calls++
		now = day.AddDate(0, 0, 1)
		return &usagestats.UserDashboardStats{TodayRequests: 5}, nil
	}
	stats, err := gatewayTodayStats(context.Background(), day, func() time.Time { return now }, load)
	require.ErrorIs(t, err, dbstats.ErrBusy)
	require.Nil(t, stats)
	_, err = gatewayTodayStats(context.Background(), day, func() time.Time { return now }, load)
	require.ErrorIs(t, err, dbstats.ErrBusy)
	require.Equal(t, 1, calls, "an old queued day must not query today's data")
}

type snapshotSuccessRepo struct{ UsageLogRepository }

func (*snapshotSuccessRepo) GetAPIKeyTotalStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return &usagestats.UserDashboardStats{TotalRequests: 8}, nil
}
func (*snapshotSuccessRepo) GetAPIKeyTodayStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return &usagestats.UserDashboardStats{}, nil
}
func (*snapshotSuccessRepo) GetAPIKeyPerformanceStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return &usagestats.UserDashboardStats{}, nil
}
func (*snapshotSuccessRepo) GetUsageTrendWithFilters(context.Context, time.Time, time.Time, string, int64, int64, int64, int64, string, *int16, *bool, *int8) ([]usagestats.TrendDataPoint, error) {
	return nil, nil
}
func (*snapshotSuccessRepo) GetModelStatsWithFilters(context.Context, time.Time, time.Time, int64, int64, int64, int64, *int16, *bool, *int8) ([]usagestats.ModelStat, error) {
	return []usagestats.ModelStat{{Model: "example", Requests: 3}}, nil
}

func TestGatewayStatisticsRollingModelWindowAndReturnedValues(t *testing.T) {
	s := NewUsageService(&snapshotSuccessRepo{}, nil, nil, nil)
	var tick atomic.Int64
	start := time.Now()
	tick.Store(start.UnixNano())
	s.gatewayStats.now = func() time.Time { return time.Unix(0, tick.Load()) }
	window := UsageModelRange{Start: start.AddDate(0, 0, -30), End: start, RollingStart: true, RollingEnd: true}
	s.GetAPIKeyUsageSnapshot(context.Background(), 1, 2, start, start, window)
	waitGatewayStatistics(t, &s.gatewayStats)
	for i := 0; i < 3; i++ {
		v := s.GetAPIKeyUsageSnapshot(context.Background(), 1, 2, start, start, window)
		require.Len(t, v.ModelStats, 1, "minute polls must see the prior completed rolling window")
		require.Equal(t, int64(3), v.ModelStats[0].Requests)
		require.NotNil(t, v.DailyUsage, "successful empty result is not unavailable")
		require.Empty(t, v.DailyUsage)
		require.NotNil(t, v.Statistics["model_stats"].RangeEnd)
		total, ok := v.Usage["total"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, int64(8), total["requests"])
		v.ModelStats[0].Requests = 999
		total["requests"] = 999
		waitGatewayStatistics(t, &s.gatewayStats)
		tick.Add(int64(time.Minute))
		window.Start, window.End = window.Start.Add(time.Minute), window.End.Add(time.Minute)
	}
	explicit := window
	explicit.RollingStart, explicit.RollingEnd = false, false
	v := s.GetAPIKeyUsageSnapshot(context.Background(), 1, 2, start, start, explicit)
	require.Nil(t, v.ModelStats, "an explicit range cannot reuse the rolling snapshot")
	waitGatewayStatistics(t, &s.gatewayStats)
}

func TestGatewayStatisticsColdReadDoesNotWaitAndComponentsProgress(t *testing.T) {
	var c gatewayStatisticsCache
	entered, release := make(chan struct{}), make(chan struct{})
	var totalCalls, todayCalls atomic.Int32
	specs := []gatewayStatsSpec{
		{"total", "total:1", time.Minute, 15 * time.Minute, func(context.Context) (any, error) {
			totalCalls.Add(1)
			close(entered)
			<-release
			return nil, context.DeadlineExceeded
		}},
		{"today", "today:1:day", time.Minute, 3 * time.Minute, func(context.Context) (any, error) { todayCalls.Add(1); return 0, nil }},
	}
	start := time.Now()
	result := c.read(context.Background(), specs)
	require.Less(t, time.Since(start), 250*time.Millisecond)
	require.Equal(t, "pending", result["total"].freshness.State)
	require.Nil(t, result["total"].value)
	<-entered
	for i := 0; i < 20; i++ {
		c.read(context.Background(), specs)
	}
	close(release)
	waitGatewayStatistics(t, &c)
	result = c.read(context.Background(), specs)
	require.Equal(t, "unavailable", result["total"].freshness.State)
	require.Equal(t, "fresh", result["today"].freshness.State)
	require.Equal(t, 0, result["today"].value, "real zero is distinct from missing")
	require.Equal(t, int32(1), totalCalls.Load())
	require.Equal(t, int32(1), todayCalls.Load())
}

func TestGatewayStatisticsPreservesStaleWithBoundedAgeAndBackoff(t *testing.T) {
	var tick atomic.Int64
	tick.Store(time.Now().UnixNano())
	c := gatewayStatisticsCache{now: func() time.Time { return time.Unix(0, tick.Load()) }}
	var fail atomic.Bool
	var calls atomic.Int32
	spec := gatewayStatsSpec{"total", "total:1", 5 * time.Minute, 15 * time.Minute, func(context.Context) (any, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, context.DeadlineExceeded
		}
		return 42, nil
	}}
	c.read(context.Background(), []gatewayStatsSpec{spec})
	waitGatewayStatistics(t, &c)
	initial := c.read(context.Background(), []gatewayStatsSpec{spec})["total"]
	require.Equal(t, "fresh", initial.freshness.State)
	fail.Store(true)
	tick.Add(int64(6 * time.Minute))
	stale := c.read(context.Background(), []gatewayStatsSpec{spec})["total"]
	require.Equal(t, "stale", stale.freshness.State)
	require.Equal(t, 42, stale.value)
	waitGatewayStatistics(t, &c)
	for i := 0; i < 20; i++ {
		c.read(context.Background(), []gatewayStatsSpec{spec})
	}
	require.Equal(t, int32(2), calls.Load())
	last := c.read(context.Background(), []gatewayStatsSpec{spec})["total"]
	require.Equal(t, initial.freshness.UpdatedAt, last.freshness.UpdatedAt)
	tick.Add(int64(10 * time.Minute))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expired := c.read(ctx, []gatewayStatsSpec{spec})["total"]
	require.Nil(t, expired.value)
	require.Nil(t, expired.freshness.UpdatedAt)
	require.Equal(t, "unavailable", expired.freshness.State)
}

func TestGatewayStatisticsBusyDoesNotBecomeDatabaseFailure(t *testing.T) {
	var tick atomic.Int64
	tick.Store(time.Now().UnixNano())
	c := gatewayStatisticsCache{now: func() time.Time { return time.Unix(0, tick.Load()) }}
	var calls atomic.Int32
	spec := gatewayStatsSpec{"total", "total:1", time.Minute, 15 * time.Minute, func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			return nil, dbstats.ErrBusy
		}
		return 9, nil
	}}
	c.read(context.Background(), []gatewayStatsSpec{spec})
	waitGatewayStatistics(t, &c)
	tick.Add(int64(2 * time.Second))
	c.read(context.Background(), []gatewayStatsSpec{spec})
	waitGatewayStatistics(t, &c)
	require.Equal(t, "fresh", c.read(context.Background(), []gatewayStatsSpec{spec})["total"].freshness.State)
	require.Zero(t, c.failures)
}

func TestGatewayStatisticsQueueAndEntriesAreBounded(t *testing.T) {
	var c gatewayStatisticsCache
	entered, release := make(chan struct{}), make(chan struct{})
	c.read(context.Background(), []gatewayStatsSpec{{"block", "block", time.Minute, time.Minute, func(context.Context) (any, error) { close(entered); <-release; return 1, nil }}})
	<-entered
	for i := 0; i < gatewayStatsCapacity+100; i++ {
		key := fmt.Sprint(i)
		c.read(context.Background(), []gatewayStatsSpec{{key, key, time.Minute, time.Minute, func(context.Context) (any, error) { return 1, nil }}})
	}
	c.mu.Lock()
	entries, queued := len(c.entries), len(c.queue)
	c.mu.Unlock()
	require.Equal(t, gatewayStatsQueueCapacity+1, entries, "rejected cold misses must not reserve cache capacity")
	require.Equal(t, gatewayStatsQueueCapacity, queued)
	close(release)
	waitGatewayStatistics(t, &c)
	spec := gatewayStatsSpec{"recovered", "recovered", time.Minute, time.Minute, func(context.Context) (any, error) { return 42, nil }}
	c.read(context.Background(), []gatewayStatsSpec{spec})
	waitGatewayStatistics(t, &c)
	require.Equal(t, 42, c.read(context.Background(), []gatewayStatsSpec{spec})["recovered"].value, "queue recovery must admit new cache identities")
}

func TestStatisticsFailureKindsRetainSafeDatabaseCause(t *testing.T) {
	for _, tc := range []struct {
		err         error
		kind, state string
	}{
		{context.DeadlineExceeded, "deadline_exceeded", ""},
		{fmt.Errorf("wrapped: %w", &pq.Error{Code: "42703"}), "postgres_error", "42703"},
		{&pq.Error{Code: "42501"}, "postgres_error", "42501"},
		{driver.ErrBadConn, "connection_error", ""},
	} {
		kind, state := statisticsFailureKind(tc.err)
		require.Equal(t, tc.kind, kind)
		require.Equal(t, tc.state, state)
	}
}
