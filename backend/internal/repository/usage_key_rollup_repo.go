package repository

import (
	"context"
	"database/sql"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var errUsageRollupNotReady = apperrors.ServiceUnavailable("STATISTICS_BUILDING", "Statistics are catching up; retry later")

type usageKeyRollupRepository struct {
	db         sqlExecutor
	shadowMu   sync.Mutex
	shadowHour time.Time
}

func NewUsageRollupRepository(db *sql.DB) service.UsageRollupRepository {
	return &usageKeyRollupRepository{db: db}
}
func (r *usageKeyRollupRepository) Step(ctx context.Context) error {
	return dbstats.RunMaintenance(ctx, nil, func(ctx context.Context) error {
		_, err := r.db.ExecContext(ctx, "SELECT process_usage_key_rollups()")
		return err
	})
}
func (r *usageKeyRollupRepository) Status(ctx context.Context) (service.UsageRollupStatus, error) {
	return dbstats.Run(ctx, nil, dbstats.ReadTimeout, r.status)
}
func (r *usageKeyRollupRepository) status(ctx context.Context) (service.UsageRollupStatus, error) {
	var s service.UsageRollupStatus
	err := scanSingleRow(ctx, r.db, `SELECT (SELECT count(*) FROM usage_key_rollup_events),
 (SELECT count(*) FROM usage_key_rollup_backfill WHERE completed_at IS NULL),
 COALESCE((SELECT extract(epoch FROM clock_timestamp()-min(created_at)) FROM usage_key_rollup_events),0)`, nil, &s.PendingEvents, &s.PendingHours, &s.OldestEventAgeSeconds)
	return s, err
}
func ProvideUsageLogRepository(client *dbent.Client, db *sql.DB, cfg *config.Config, p *StatisticsPools) service.UsageLogRepository {
	return newGovernedUsageLogRepository(client, db, p, cfg)
}

func (r *usageLogRepository) useUsageRollup(keyID int64) bool {
	if !r.rollupReads {
		return false
	}
	if len(r.rollupKeys) == 0 {
		return true
	}
	for _, id := range r.rollupKeys {
		if id == keyID && keyID > 0 {
			return true
		}
	}
	return false
}

// Reconcile compares one complete hour in one MVCC snapshot, including missing
// and extra identities. Pending events are not mismatches. A real mismatch
// latches reads closed across replicas until an operator repairs/rechecks data.
func (r *usageKeyRollupRepository) Reconcile(ctx context.Context) (string, error) {
	r.shadowMu.Lock()
	defer r.shadowMu.Unlock()
	return dbstats.Run(ctx, nil, dbstats.ReadTimeout, func(ctx context.Context) (string, error) {
		var hour sql.NullTime
		var result string
		err := scanSingleRow(ctx, r.db, `WITH candidate AS MATERIALIZED (
 SELECT bucket_start FROM usage_key_rollup_backfill WHERE completed_at IS NOT NULL
 AND ($1::timestamptz IS NULL OR bucket_start>$1) ORDER BY bucket_start LIMIT 1
), expected AS MATERIALIZED (
 SELECT api_key_id,user_id,count(*) requests,COALESCE(sum(input_tokens),0) input_tokens,
 COALESCE(sum(output_tokens),0) output_tokens,COALESCE(sum(cache_creation_tokens),0) cache_creation_tokens,
 COALESCE(sum(cache_read_tokens),0) cache_read_tokens,COALESCE(sum(total_cost),0) total_cost,
 COALESCE(sum(actual_cost),0) actual_cost,COALESCE(sum(duration_ms::numeric),0) duration_sum,count(duration_ms) duration_count
 FROM usage_logs WHERE created_at>=(SELECT bucket_start FROM candidate)
 AND created_at<(SELECT bucket_start FROM candidate)+interval '1 hour' GROUP BY api_key_id,user_id
), actual AS MATERIALIZED (
 SELECT api_key_id,user_id,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum,duration_count
 FROM usage_key_hourly_rollups WHERE bucket_start=(SELECT bucket_start FROM candidate)
), result AS MATERIALIZED (
 SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM candidate) THEN 'empty'
 WHEN EXISTS(SELECT 1 FROM usage_key_rollup_events WHERE bucket_start=(SELECT bucket_start FROM candidate)) THEN 'not_caught_up'
 WHEN EXISTS((SELECT * FROM expected EXCEPT SELECT * FROM actual) UNION ALL (SELECT * FROM actual EXCEPT SELECT * FROM expected)) THEN 'mismatch'
 ELSE 'match' END value
), latch AS (
 UPDATE usage_key_rollup_state SET integrity_failed=true WHERE id=1 AND (SELECT value FROM result)='mismatch' RETURNING id
) SELECT (SELECT bucket_start FROM candidate),value FROM result`, []any{nullableUsageTime(r.shadowHour)}, &hour, &result)
		if err == nil {
			r.shadowHour = hour.Time
		}
		return result, err
	})
}

// Read complete UTC buckets and only partial-hour boundary detail in one MVCC
// snapshot. A pending source event prevents publishing stale sums as current.
const usageKeyRollupCTE = `WITH ready AS MATERIALIZED (
 SELECT initialized AND version=1 AND NOT integrity_failed
 AND NOT EXISTS(SELECT 1 FROM usage_key_rollup_backfill WHERE completed_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM usage_key_rollup_events WHERE ($1::bigint=0 OR user_id=$1) AND ($2::bigint=0 OR api_key_id=$2)) AS ok
 FROM usage_key_rollup_state WHERE id=1
), data AS (
 SELECT bucket_start AS created_at,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum,duration_count
 FROM usage_key_hourly_rollups
 WHERE ($1::bigint=0 OR user_id=$1) AND ($2::bigint=0 OR api_key_id=$2)
 AND ($3::timestamptz IS NULL OR bucket_start >= $3)
 AND ($4::timestamptz IS NULL OR bucket_start+interval '1 hour' <= $4)
 UNION ALL
 SELECT created_at,1,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,COALESCE(duration_ms,0),CASE WHEN duration_ms IS NULL THEN 0 ELSE 1 END
 FROM usage_logs
 WHERE ($1::bigint=0 OR user_id=$1) AND ($2::bigint=0 OR api_key_id=$2)
 AND ($3::timestamptz IS NULL OR created_at >= $3) AND ($4::timestamptz IS NULL OR created_at < $4)
 AND (($3::timestamptz IS NOT NULL AND created_at < (date_trunc('hour',$3 AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour' AND date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'<$3)
 OR ($4::timestamptz IS NOT NULL AND created_at >= date_trunc('hour',$4 AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AND (date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour'>$4))
) `

func nullableUsageTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
func rollupTrendAligned(start, end time.Time, granularity string) bool {
	if granularity != "day" || end.Sub(start) > 366*24*time.Hour {
		return false
	}
	for t := start.In(timezone.Location()); !t.After(end); t = t.AddDate(0, 0, 1) {
		_, offset := t.Zone()
		if offset%3600 != 0 {
			return false
		}
	}
	return true
}
func (r *usageLogRepository) readUsageRollup(ctx context.Context, userID, keyID int64, start, end time.Time, ignoreNullDuration bool) (*usagestats.UsageStats, error) {
	var s usagestats.UsageStats
	var ready bool
	denominator := "SUM(requests)"
	if ignoreNullDuration {
		denominator = "SUM(duration_count)"
	}
	err := scanSingleRow(ctx, r.sql, usageKeyRollupCTE+`SELECT COALESCE((SELECT ok FROM ready),false),
 COALESCE(SUM(requests),0),COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0),COALESCE(SUM(cache_creation_tokens),0),COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(total_cost),0),COALESCE(SUM(actual_cost),0),COALESCE(SUM(duration_sum)/NULLIF(`+denominator+`,0),0) FROM data`,
		[]any{userID, keyID, nullableUsageTime(start), nullableUsageTime(end)}, &ready, &s.TotalRequests, &s.TotalInputTokens, &s.TotalOutputTokens, &s.TotalCacheCreationTokens, &s.TotalCacheReadTokens, &s.TotalCost, &s.TotalActualCost, &s.AverageDurationMs)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errUsageRollupNotReady
	}
	s.TotalCacheTokens = s.TotalCacheCreationTokens + s.TotalCacheReadTokens
	s.TotalTokens = s.TotalInputTokens + s.TotalOutputTokens + s.TotalCacheTokens
	return &s, nil
}

func (r *usageLogRepository) readUsageRollupTrend(ctx context.Context, userID, keyID int64, start, end time.Time, granularity string) ([]TrendDataPoint, error) {
	// A UTC hour crossing a local day boundary cannot be assigned to one date.
	// The caller only uses this path when its server timezone has whole-hour offsets.
	rows, err := r.sql.QueryContext(ctx, usageKeyRollupCTE+`SELECT COALESCE((SELECT ok FROM ready),false),date,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_tokens,cost,actual_cost FROM (SELECT TO_CHAR(created_at,'`+safeDateFormat(granularity)+`') AS date,
 SUM(requests) requests,SUM(input_tokens) input_tokens,SUM(output_tokens) output_tokens,SUM(cache_creation_tokens) cache_creation_tokens,SUM(cache_read_tokens) cache_read_tokens,SUM(input_tokens+output_tokens+cache_creation_tokens+cache_read_tokens) total_tokens,SUM(total_cost) cost,SUM(actual_cost) actual_cost FROM data GROUP BY date) t
 RIGHT JOIN (SELECT 1) sentinel ON true ORDER BY date`, userID, keyID, nullableUsageTime(start), nullableUsageTime(end))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]TrendDataPoint, 0)
	for rows.Next() {
		var ready bool
		var date sql.NullString
		var requests, input, output, creation, read, total sql.NullInt64
		var cost, actual sql.NullFloat64
		if err = rows.Scan(&ready, &date, &requests, &input, &output, &creation, &read, &total, &cost, &actual); err != nil {
			return nil, err
		}
		if !ready {
			return nil, errUsageRollupNotReady
		}
		if !date.Valid {
			continue
		}
		result = append(result, TrendDataPoint{Date: date.String, Requests: requests.Int64, InputTokens: input.Int64, OutputTokens: output.Int64, CacheCreationTokens: creation.Int64, CacheReadTokens: read.Int64, TotalTokens: total.Int64, Cost: cost.Float64, ActualCost: actual.Float64})
	}
	return result, rows.Err()
}
