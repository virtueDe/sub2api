package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const (
	usageStatsTTL         = 20 * time.Second
	usageStatsTimeout     = 2 * time.Second
	usageStatsMaxEntries  = 1024
	usageStatsConcurrency = 2
)

var errUsageStatsBusy = errors.New("usage statistics concurrency limit reached")

type usageStatsEntry struct {
	value   any
	expires time.Time
}
type usageStatsCache struct {
	once         sync.Once
	mu           sync.Mutex
	entries      map[string]usageStatsEntry
	gate         chan struct{}
	flights      singleflight.Group
	lastErrorLog atomic.Int64
}

func usageSummaryKey(apiKeyID int64) string {
	return fmt.Sprintf("summary:%d:%s", apiKeyID, timezone.Today().Format("2006-01-02"))
}

func (c *usageStatsCache) get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if ok && time.Now().Before(entry.expires) {
		return entry.value, true
	}
	delete(c.entries, key)
	return nil, false
}

// Cache only derived statistics, never the live balance or authorization data.
// Unique requests are rejected when the small DB budget is full instead of
// creating another unbounded queue alongside billing.
func loadUsageStats[T any](ctx context.Context, c *usageStatsCache, key string, load func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	c.once.Do(func() {
		c.entries = make(map[string]usageStatsEntry)
		c.gate = make(chan struct{}, usageStatsConcurrency)
	})
	if value, ok := c.get(key); ok {
		return value.(T), nil
	}
	ch := c.flights.DoChan(key, func() (any, error) {
		if value, ok := c.get(key); ok {
			return value, nil
		}
		select {
		case c.gate <- struct{}{}:
			defer func() { <-c.gate }()
		default:
			c.logFailure(errUsageStatsBusy)
			return nil, errUsageStatsBusy
		}
		loadCtx, cancel := context.WithTimeout(context.Background(), usageStatsTimeout)
		defer cancel()
		value, err := load(loadCtx)
		if err != nil {
			c.logFailure(err)
			return nil, err
		}
		c.mu.Lock()
		now := time.Now()
		if len(c.entries) >= usageStatsMaxEntries {
			for k, entry := range c.entries {
				if !now.Before(entry.expires) {
					delete(c.entries, k)
				}
			}
		}
		if len(c.entries) < usageStatsMaxEntries {
			c.entries[key] = usageStatsEntry{value: value, expires: now.Add(usageStatsTTL)}
		}
		c.mu.Unlock()
		return value, nil
	})
	select {
	case result := <-ch:
		if result.Err != nil {
			return zero, result.Err
		}
		return result.Val.(T), nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (c *usageStatsCache) logFailure(err error) {
	now := time.Now().Unix()
	last := c.lastErrorLog.Load()
	if now-last >= 10 && c.lastErrorLog.CompareAndSwap(last, now) {
		logger.L().Warn("usage.statistics_unavailable", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
	}
}
