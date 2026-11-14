package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

type BatchUsageFreshness struct {
	StatisticsFreshness
	ValidUntil *time.Time `json:"valid_until,omitempty"`
	FreshUntil *time.Time `json:"fresh_until,omitempty"`
}

type BatchAPIKeyUsageSnapshot struct {
	Stats      map[int64]*usagestats.BatchAPIKeyUsageStats `json:"stats"`
	Statistics BatchUsageFreshness                         `json:"statistics"`
}

type batchUsageValue struct {
	stats      map[int64]*usagestats.BatchAPIKeyUsageStats
	start, end time.Time
}

type batchUsageSnapshotRepository interface {
	GetBatchAPIKeyUsageStatsForSnapshot(context.Context, []int64, time.Time, time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error)
}

// IDs must have passed ownership verification on every request, including hits.
// User/date/range isolation keeps rolling snapshots separate from financial state.
func (s *UsageService) GetBatchAPIKeyUsageSnapshot(ctx context.Context, userID int64, ids []int64) BatchAPIKeyUsageSnapshot {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	ids = slices.DeleteFunc(ids, func(id int64) bool { return id <= 0 })
	result := BatchAPIKeyUsageSnapshot{Stats: make(map[int64]*usagestats.BatchAPIKeyUsageStats), Statistics: BatchUsageFreshness{StatisticsFreshness: StatisticsFreshness{State: "fresh"}}}
	if len(ids) == 0 {
		return result
	}
	today := func() time.Time {
		now := s.batchStats.clock().In(timezone.Location())
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
	day := today()
	spec := gatewayStatsSpec{
		name: "api_keys_usage", key: fmt.Sprintf("batch:%d:%s:rolling30d:%v", userID, day.Format("2006-01-02Z07:00"), ids),
		ttl: 30 * time.Second, maxAge: 5 * time.Minute,
		load: func(ctx context.Context) (any, error) {
			if !day.Equal(today()) {
				return nil, dbstats.ErrBusy
			}
			end := s.batchStats.clock()
			start := end.AddDate(0, 0, -30)
			load := s.GetBatchAPIKeyUsageStats
			if repository, ok := s.usageRepo.(batchUsageSnapshotRepository); ok {
				load = repository.GetBatchAPIKeyUsageStatsForSnapshot
			}
			stats, err := load(ctx, ids, start, end)
			if err != nil {
				return nil, err
			}
			if !day.Equal(today()) {
				return nil, dbstats.ErrBusy
			}
			for _, id := range ids {
				if stats[id] == nil {
					return nil, fmt.Errorf("batch statistics omitted a requested key")
				}
			}
			return batchUsageValue{stats: stats, start: start, end: end}, nil
		},
	}
	v := s.batchStats.read(ctx, []gatewayStatsSpec{spec})[spec.name]
	result.Statistics.StatisticsFreshness = v.freshness
	if data, ok := v.value.(batchUsageValue); ok {
		validUntil := data.end.Add(5 * time.Minute)
		if midnight := day.AddDate(0, 0, 1); midnight.Before(validUntil) {
			validUntil = midnight
		}
		if !s.batchStats.clock().Before(validUntil) {
			result.Statistics.State = "unavailable"
			result.Statistics.UpdatedAt = nil
			return result
		}
		for id, stats := range data.stats {
			copy := *stats
			result.Stats[id] = &copy
		}
		result.Statistics.RangeStart, result.Statistics.RangeEnd = &data.start, &data.end
		result.Statistics.ValidUntil = &validUntil
		freshUntil := v.freshness.UpdatedAt.Add(30 * time.Second)
		if validUntil.Before(freshUntil) {
			freshUntil = validUntil
		}
		result.Statistics.FreshUntil = &freshUntil
	}
	return result
}

func (s *UsageService) Stop() {
	if s == nil {
		return
	}
	s.batchStats.stop()
	s.gatewayStats.stop()
}
