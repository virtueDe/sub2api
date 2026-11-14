package repository

import (
	"context"
	"time"

	"github.com/lib/pq"
)

// Dirty identities and both cost windows are read in the same snapshot. Raw
// ranges are disjoint from clean buckets and from each other; events are never
// joined directly to detail rows. A large backlog must yield to maintenance.
const batchAPIKeyRollupQuery = `WITH
keys AS MATERIALIZED (SELECT unnest($1::bigint[]) AS id),
windows(kind, start_at, end_at) AS (VALUES (0, $2::timestamptz, $3::timestamptz), (1, $4::timestamptz, NULL::timestamptz)),
pending_events AS MATERIALIZED (
 SELECT api_key_id, user_id, bucket_start
 FROM usage_key_rollup_events
 WHERE api_key_id=ANY($1) AND bucket_start >= $5
 LIMIT 4097
), dirty AS MATERIALIZED (
 SELECT DISTINCT api_key_id, user_id, bucket_start FROM pending_events
 LIMIT 49
), ready AS MATERIALIZED (
 SELECT COALESCE((SELECT initialized AND version=1 AND NOT integrity_failed
 FROM usage_key_rollup_state WHERE id=1),false)
 AND NOT EXISTS(SELECT 1 FROM usage_key_rollup_backfill WHERE completed_at IS NULL)
 AND (SELECT count(*) FROM pending_events)<=4096
 AND (SELECT count(*) FROM dirty)<=48 AS ok
), clean AS (
 SELECT r.api_key_id,w.kind,SUM(r.actual_cost) AS cost
 FROM ready CROSS JOIN windows w JOIN usage_key_hourly_rollups r
 ON r.api_key_id=ANY($1) AND r.bucket_start>=w.start_at
 AND (w.end_at IS NULL OR r.bucket_start+interval '1 hour'<=w.end_at)
 WHERE ready.ok AND NOT EXISTS(SELECT 1 FROM dirty d WHERE
 d.api_key_id=r.api_key_id AND d.user_id=r.user_id AND d.bucket_start=r.bucket_start)
 GROUP BY r.api_key_id,w.kind
), ranges AS MATERIALIZED (
 SELECT k.id AS api_key_id,NULL::bigint AS user_id,w.kind,w.start_at AS lo,
 LEAST(w.end_at,(date_trunc('hour',w.start_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')+interval '1 hour') AS hi
 FROM ready CROSS JOIN keys k CROSS JOIN windows w
 WHERE ready.ok AND w.start_at<>(date_trunc('hour',w.start_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
 UNION ALL
 SELECT k.id,NULL::bigint,w.kind,(date_trunc('hour',w.end_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),w.end_at
 FROM ready CROSS JOIN keys k CROSS JOIN windows w
 WHERE ready.ok AND w.end_at IS NOT NULL
 AND w.end_at<>(date_trunc('hour',w.end_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
 AND (date_trunc('hour',w.end_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')>=w.start_at
 UNION ALL
 SELECT d.api_key_id,d.user_id,w.kind,d.bucket_start,d.bucket_start+interval '1 hour'
 FROM ready CROSS JOIN dirty d CROSS JOIN windows w
 WHERE ready.ok AND d.bucket_start>=w.start_at
 AND (w.end_at IS NULL OR d.bucket_start+interval '1 hour'<=w.end_at)
), detail AS (
 SELECT b.api_key_id,b.kind,v.cost FROM ranges b CROSS JOIN LATERAL (
 SELECT SUM(l.actual_cost) AS cost FROM usage_logs l
 WHERE l.api_key_id=b.api_key_id AND l.created_at>=b.lo AND l.created_at<b.hi
 AND (b.user_id IS NULL OR l.user_id=b.user_id)
 ) v
), costs AS (SELECT * FROM clean UNION ALL SELECT * FROM detail)
SELECT k.id,ready.ok,
 COALESCE(SUM(c.cost) FILTER (WHERE c.kind=0),0),
 COALESCE(SUM(c.cost) FILTER (WHERE c.kind=1),0)
FROM keys k CROSS JOIN ready LEFT JOIN costs c ON c.api_key_id=k.id
GROUP BY k.id,ready.ok`

func (r *usageLogRepository) readBatchAPIKeyRollup(ctx context.Context, ids []int64, start, end, today time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	lower := start
	if today.Before(lower) {
		lower = today
	}
	rows, err := r.sql.QueryContext(ctx, batchAPIKeyRollupQuery, pq.Array(ids), start, end, today, lower.UTC().Truncate(time.Hour))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[int64]*BatchAPIKeyUsageStats, len(ids))
	for rows.Next() {
		var s BatchAPIKeyUsageStats
		var ready bool
		if err := rows.Scan(&s.APIKeyID, &ready, &s.TotalActualCost, &s.TodayActualCost); err != nil {
			return nil, err
		}
		if !ready {
			return nil, errUsageRollupNotReady
		}
		result[s.APIKeyID] = &s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
