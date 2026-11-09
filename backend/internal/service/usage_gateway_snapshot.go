package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"go.uber.org/zap"
)

const gatewayStatsCapacity = 1024
const gatewayStatsQueueCapacity = 64
const gatewayStatsQueueAge = 10 * time.Second

// StatisticsFreshness describes a derived component, never a wallet balance.
type StatisticsFreshness struct {
	State      string     `json:"state"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	Refreshing bool       `json:"refreshing"`
	RangeStart *time.Time `json:"range_start,omitempty"`
	RangeEnd   *time.Time `json:"range_end,omitempty"`
}

// Rolling boundaries share a cache entry across polls. Metadata reports the
// actual boundaries of the cached computation; explicit dates remain isolated.
type UsageModelRange struct {
	Start, End               time.Time
	RollingStart, RollingEnd bool
}

func (r UsageModelRange) cacheKey(apiKeyID int64) string {
	start, end := fmt.Sprint(r.Start.UnixNano()), fmt.Sprint(r.End.UnixNano())
	if r.RollingStart {
		start = "rolling30d"
	}
	if r.RollingEnd {
		end = "now"
	}
	return fmt.Sprintf("model:%d:%s:%s", apiKeyID, start, end)
}

type gatewayModelStats struct {
	rows       []usagestats.ModelStat
	start, end time.Time
}

type APIKeyUsageSnapshot struct {
	Usage      map[string]any
	DailyUsage []usagestats.APIKeyDailyUsagePoint
	ModelStats []usagestats.ModelStat
	Statistics map[string]StatisticsFreshness
}

type usageDashboardComponents interface {
	GetAPIKeyTotalStats(context.Context, int64) (*usagestats.UserDashboardStats, error)
	GetAPIKeyTodayStats(context.Context, int64) (*usagestats.UserDashboardStats, error)
	GetAPIKeyPerformanceStats(context.Context, int64) (*usagestats.UserDashboardStats, error)
}

type gatewayStatsSpec struct {
	name, key   string
	ttl, maxAge time.Duration
	load        func(context.Context) (any, error)
}

// Failure cooldown spans date ranges within one component, but a failing total
// scan must not suppress today's data or performance for the same key.
func (s gatewayStatsSpec) scopeKey() string {
	parts := strings.Split(s.key, ":")
	if s.name == "daily_usage" && len(parts) >= 3 {
		return strings.Join(parts[:3], ":")
	}
	if len(parts) >= 2 {
		return strings.Join(parts[:2], ":")
	}
	return s.key
}

type gatewayStatsEntry struct {
	value                                                any
	updatedAt, expires, usableUntil, retryAfter, touched time.Time
	pending                                              bool
}
type gatewayStatsJob struct {
	spec     gatewayStatsSpec
	queuedAt time.Time
}
type gatewayStatsValue struct {
	value     any
	freshness StatisticsFreshness
}

// One bounded, lazy worker drains a FIFO then exits. No goroutine is created per
// waiter or per cache key, and no database connection is held while queued.
type gatewayStatisticsCache struct {
	mu                        sync.Mutex
	entries                   map[string]*gatewayStatsEntry
	queue                     []gatewayStatsJob
	running                   bool
	now                       func() time.Time
	lastLog                   time.Time
	lastFailureLog            time.Time
	cooldowns                 map[string]time.Time
	loads, failures, rejected uint64
	hits, stale, missing      uint64
}

func (c *gatewayStatisticsCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *gatewayStatisticsCache) read(ctx context.Context, specs []gatewayStatsSpec) map[string]gatewayStatsValue {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*gatewayStatsEntry)
		c.cooldowns = make(map[string]time.Time)
	}
	now := c.clock()
	result := make(map[string]gatewayStatsValue, len(specs))
	for _, spec := range specs {
		e := c.entries[spec.key]
		if e == nil && len(c.entries) >= gatewayStatsCapacity {
			for key, old := range c.entries {
				if !old.pending && now.Sub(old.touched) >= 15*time.Minute {
					delete(c.entries, key)
				}
			}
		}
		if e == nil && len(c.entries) < gatewayStatsCapacity {
			e = &gatewayStatsEntry{}
			c.entries[spec.key] = e
		}
		v := gatewayStatsValue{freshness: StatisticsFreshness{State: "unavailable"}}
		if e == nil {
			c.rejected++
			result[spec.name] = v
			continue
		}
		e.touched = now
		if !e.pending && !now.Before(e.expires) && !now.Before(e.retryAfter) && !now.Before(c.cooldowns[spec.scopeKey()]) && ctx.Err() == nil {
			if len(c.queue) < gatewayStatsQueueCapacity {
				e.pending = true
				c.queue = append(c.queue, gatewayStatsJob{spec: spec, queuedAt: now})
			} else {
				c.rejected++
			}
		}
		v.freshness.Refreshing = e.pending
		if e.value != nil && now.Before(e.usableUntil) {
			v.value = e.value
			t := e.updatedAt
			v.freshness.UpdatedAt = &t
			v.freshness.State = "stale"
			if now.Before(e.expires) {
				v.freshness.State = "fresh"
			}
		} else if e.pending {
			v.freshness.State = "pending"
		}
		switch v.freshness.State {
		case "fresh":
			c.hits++
		case "stale":
			c.stale++
		default:
			c.missing++
		}
		result[spec.name] = v
	}
	if !c.running && len(c.queue) != 0 {
		c.running = true
		go c.drain()
	}
	return result
}

func (c *gatewayStatisticsCache) drain() {
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.running = false
			c.mu.Unlock()
			return
		}
		job := c.queue[0]
		c.queue[0] = gatewayStatsJob{}
		c.queue = c.queue[1:]
		now := c.clock()
		expired := now.Sub(job.queuedAt) >= gatewayStatsQueueAge
		cooldown := c.cooldowns[job.spec.scopeKey()]
		if now.Before(cooldown) {
			// A previously queued range failed. Do not execute other ranges or
			// extend the original failure's deadline when draining this queue.
			e := c.entries[job.spec.key]
			e.pending, e.retryAfter = false, cooldown
			c.mu.Unlock()
			continue
		}
		c.mu.Unlock()
		var value any
		var err error
		if expired {
			err = dbstats.ErrBusy
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), usageStatsTimeout)
			value, err = job.spec.load(ctx)
			if err == nil {
				err = ctx.Err()
			}
			cancel()
		}
		c.mu.Lock()
		now = c.clock()
		var failureFields, snapshotFields []zap.Field
		e := c.entries[job.spec.key]
		e.pending = false
		if err != nil {
			e.retryAfter = now.Add(usageStatsFailureTTL)
			if errors.Is(err, dbstats.ErrBusy) {
				e.retryAfter = now.Add(time.Second)
				c.rejected++
			} else {
				c.failures++
				// Active cooldowns cannot outnumber entries: entries are retained
				// at least 15 minutes, while failures cool down for 30 seconds.
				if len(c.cooldowns) >= gatewayStatsCapacity {
					for scope, until := range c.cooldowns {
						if !now.Before(until) {
							delete(c.cooldowns, scope)
						}
					}
				}
				c.cooldowns[job.spec.scopeKey()] = e.retryAfter
				if now.Sub(c.lastFailureLog) >= time.Minute {
					c.lastFailureLog = now
					kind := "repository_error"
					if errors.Is(err, context.DeadlineExceeded) {
						kind = "deadline_exceeded"
					}
					if errors.Is(err, context.Canceled) {
						kind = "canceled"
					}
					failureFields = []zap.Field{zap.String("component", job.spec.name), zap.String("error_kind", kind), zap.Bool(logger.OpsSystemLogSkipField, true)}
				}
			}
		} else {
			e.value, e.updatedAt = value, now
			e.expires, e.usableUntil = now.Add(job.spec.ttl), now.Add(job.spec.maxAge)
			e.retryAfter = time.Time{}
		}
		if !expired {
			c.loads++
		}
		if now.Sub(c.lastLog) >= time.Minute {
			c.lastLog = now
			snapshotFields = []zap.Field{zap.Int("entries", len(c.entries)), zap.Int("queued", len(c.queue)),
				zap.Uint64("loads_total", c.loads), zap.Uint64("failures_total", c.failures), zap.Uint64("rejected_total", c.rejected),
				zap.Uint64("hits_total", c.hits), zap.Uint64("stale_total", c.stale), zap.Uint64("missing_total", c.missing),
				zap.Bool(logger.OpsSystemLogSkipField, true)}
		}
		c.mu.Unlock()
		if failureFields != nil {
			logger.L().Warn("usage.statistics_refresh_failed", failureFields...)
		}
		if snapshotFields != nil {
			logger.L().Info("usage.statistics_cache", snapshotFields...)
		}
	}
}

// GetAPIKeyUsageSnapshot never waits for a reporting query. The existing
// synchronous statistics APIs keep their contracts; only /v1/usage uses this.
func (s *UsageService) GetAPIKeyUsageSnapshot(ctx context.Context, userID, apiKeyID int64, dailyStart, dailyEnd time.Time, modelRange UsageModelRange) APIKeyUsageSnapshot {
	components, supported := s.usageRepo.(usageDashboardComponents)
	today := timezone.Today()
	componentLoad := func(kind string) func(context.Context) (any, error) {
		return func(ctx context.Context) (any, error) {
			if !supported {
				return nil, errors.New("usage repository does not implement dashboard components")
			}
			var stats *usagestats.UserDashboardStats
			var err error
			switch kind {
			case "total":
				stats, err = components.GetAPIKeyTotalStats(ctx, apiKeyID)
			case "today":
				stats, err = gatewayTodayStats(ctx, today, timezone.Today, func(ctx context.Context) (*usagestats.UserDashboardStats, error) {
					return components.GetAPIKeyTodayStats(ctx, apiKeyID)
				})
			case "performance":
				stats, err = components.GetAPIKeyPerformanceStats(ctx, apiKeyID)
			}
			if err == nil && stats == nil {
				err = errors.New("statistics repository returned no result")
			}
			if err != nil {
				return nil, err
			}
			return *stats, nil
		}
	}
	specs := []gatewayStatsSpec{
		{"total", fmt.Sprintf("total:%d", apiKeyID), 5 * time.Minute, 15 * time.Minute, componentLoad("total")},
		{"today", fmt.Sprintf("today:%d:%s", apiKeyID, today.Format("2006-01-02")), time.Minute, 3 * time.Minute, componentLoad("today")},
		{"performance", fmt.Sprintf("performance:%d", apiKeyID), 20 * time.Second, time.Minute, componentLoad("performance")},
		{"daily_usage", fmt.Sprintf("daily:%d:%d:%d:%d", userID, apiKeyID, dailyStart.UnixNano(), dailyEnd.UnixNano()), time.Minute, 3 * time.Minute, func(ctx context.Context) (any, error) {
			rows, err := s.usageRepo.GetUsageTrendWithFilters(ctx, dailyStart, dailyEnd, "day", userID, apiKeyID, 0, 0, "", nil, nil, nil)
			if err != nil {
				return nil, err
			}
			points := make([]usagestats.APIKeyDailyUsagePoint, 0, len(rows))
			for _, r := range rows {
				points = append(points, usagestats.APIKeyDailyUsagePoint{Date: r.Date, Requests: r.Requests, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheCreationTokens, TotalTokens: r.TotalTokens, Cost: r.Cost, ActualCost: r.ActualCost})
			}
			return points, nil
		}},
		{"model_stats", modelRange.cacheKey(apiKeyID), time.Minute, 3 * time.Minute, func(ctx context.Context) (any, error) {
			rows, err := s.usageRepo.GetModelStatsWithFilters(ctx, modelRange.Start, modelRange.End, 0, apiKeyID, 0, 0, nil, nil, nil)
			if err != nil {
				return nil, err
			}
			if rows == nil {
				rows = []usagestats.ModelStat{}
			}
			return gatewayModelStats{rows: rows, start: modelRange.Start, end: modelRange.End}, nil
		}},
	}
	values := s.gatewayStats.read(ctx, specs)
	result := APIKeyUsageSnapshot{Statistics: make(map[string]StatisticsFreshness, len(values))}
	usage := make(map[string]any)
	for name, v := range values {
		result.Statistics[name] = v.freshness
		if v.value == nil {
			continue
		}
		switch value := v.value.(type) {
		case usagestats.UserDashboardStats:
			switch name {
			case "total":
				usage["total"] = map[string]any{"requests": value.TotalRequests, "input_tokens": value.TotalInputTokens, "output_tokens": value.TotalOutputTokens, "cache_creation_tokens": value.TotalCacheCreationTokens, "cache_read_tokens": value.TotalCacheReadTokens, "total_tokens": value.TotalTokens, "cost": value.TotalCost, "actual_cost": value.TotalActualCost}
				usage["average_duration_ms"] = value.AverageDurationMs
			case "today":
				usage["today"] = map[string]any{"requests": value.TodayRequests, "input_tokens": value.TodayInputTokens, "output_tokens": value.TodayOutputTokens, "cache_creation_tokens": value.TodayCacheCreationTokens, "cache_read_tokens": value.TodayCacheReadTokens, "total_tokens": value.TodayTokens, "cost": value.TodayCost, "actual_cost": value.TodayActualCost}
			case "performance":
				usage["rpm"], usage["tpm"] = value.Rpm, value.Tpm
			}
		case []usagestats.APIKeyDailyUsagePoint:
			result.DailyUsage = slices.Clone(value)
		case gatewayModelStats:
			result.ModelStats = slices.Clone(value.rows)
			meta := result.Statistics[name]
			meta.RangeStart, meta.RangeEnd = &value.start, &value.end
			result.Statistics[name] = meta
		}
	}
	if len(usage) > 0 {
		result.Usage = usage
	}
	return result
}

func gatewayTodayStats(ctx context.Context, day time.Time, today func() time.Time, load func(context.Context) (*usagestats.UserDashboardStats, error)) (*usagestats.UserDashboardStats, error) {
	if !day.Equal(today()) {
		return nil, dbstats.ErrBusy
	}
	stats, err := load(ctx)
	if err == nil && !day.Equal(today()) {
		return nil, dbstats.ErrBusy
	}
	return stats, err
}
