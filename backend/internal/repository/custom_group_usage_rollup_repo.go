package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// groupUsageRollupSnapshot 是一次汇总水位的快照。
// closedBefore / tailStart 在水位无效时退化为 1970-01-01，
// 与此前 SQL 里 CASE WHEN valid 的语义一一对应。
type groupUsageRollupSnapshot struct {
	valid        bool
	closedBefore string    // date，历史日桶的右开边界
	tailStart    time.Time // 尾段（还没进日桶的那部分 usage_logs）的起点
}

// readGroupUsageRollupSnapshot 读一次汇总水位并在 Go 侧判定有效性。
//
// 判定条件与原先 SQL 里的 state_values CTE 完全一致：
// 恰好一行、时区名与当前服务端配置相同、且水位不在未来。
func (r *usageLogRepository) readGroupUsageRollupSnapshot(ctx context.Context, timezoneName, todayDate string) (groupUsageRollupSnapshot, error) {
	epoch := time.Unix(0, 0).UTC()
	invalid := groupUsageRollupSnapshot{
		closedBefore: "1970-01-01",
		tailStart:    epoch,
	}

	var rowCount int
	var closedBefore sql.NullString
	var retainedFrom sql.NullTime
	var stateTimezone sql.NullString
	if err := scanSingleRow(ctx, r.sql, `
		SELECT
			COUNT(*),
			MAX(closed_before)::text,
			MAX(retained_from),
			MAX(timezone_name)
		FROM usage_group_rollup_state
		WHERE id = 1
	`, nil, &rowCount, &closedBefore, &retainedFrom, &stateTimezone); err != nil {
		return groupUsageRollupSnapshot{}, fmt.Errorf("读取分组用量汇总水位: %w", err)
	}
	if rowCount != 1 || !closedBefore.Valid || !retainedFrom.Valid ||
		!stateTimezone.Valid || stateTimezone.String != timezoneName ||
		closedBefore.String > todayDate {
		return invalid, nil
	}

	tailStart, err := service.ParseGroupUsageDate(closedBefore.String)
	if err != nil {
		// 水位值解析不出来，按无效处理：宁可多扫一次，也不要算错钱。
		return invalid, nil //nolint:nilerr // 与上面的 valid 判定同语义，均降级为全量重算
	}
	return groupUsageRollupSnapshot{
		valid:        true,
		closedBefore: closedBefore.String,
		tailStart:    tailStart.UTC(),
	}, nil
}

func (r *usageLogRepository) getAllGroupUsageSummaryFromRollups(ctx context.Context, todayStart time.Time) (results []usagestats.GroupUsageSummary, err error) {
	todayStart = service.GroupUsageTodayStart(todayStart)
	yesterdayStart := service.GroupUsageYesterdayStart(todayStart)
	timezoneName := service.GroupUsageTimezoneName()
	todayDate := service.GroupUsageDate(todayStart)
	yesterdayDate := service.GroupUsageDate(yesterdayStart)

	state, err := r.readGroupUsageRollupSnapshot(ctx, timezoneName, todayDate)
	if err != nil {
		return nil, err
	}

	// 尾段起点必须以**查询参数**的形式给进来。
	//
	// 此前它是在同一条 SQL 里由 state CTE 算出、再 CROSS JOIN 给 tail 用的，
	// 于是 created_at 的下界对 planner 来说是个运行期才知道的值：既进不了
	// index cond，也估不准选择性，只能退化成 usage_logs 全表扫。
	// 生产实测（1721 万行 / PostgreSQL 17）：
	//   Seq Scan on usage_logs … rows=17212870, Rows Removed by Join Filter: 17178082
	//   Execution Time: 48720 ms
	// 换成参数之后走 idx_usage_logs_created_at：
	//   Index Scan … Index Cond: (created_at >= $7)
	//   Execution Time: 34 ms
	const query = `
		WITH ready AS MATERIALIZED (
			SELECT NOT $4::boolean OR EXISTS (
				SELECT 1 FROM usage_group_rollup_state WHERE id=1
				AND closed_before=$5::date AND timezone_name=$6::text
			) AS ok
		), dirty_days AS MATERIALIZED (
			SELECT DISTINCT d.bucket_date::date AS bucket_date
			FROM usage_group_rollup_events e
			CROSS JOIN LATERAL generate_series(
				(e.bucket_start AT TIME ZONE $6::text)::date::timestamp,
				((e.bucket_start+interval '24 hours'-interval '1 microsecond') AT TIME ZONE $6::text)::date::timestamp,
				interval '1 day'
			) d(bucket_date)
			WHERE $4::boolean AND d.bucket_date::date<$5::date
		), historical AS (
			SELECT
				rollup.group_id,
				COALESCE(SUM(rollup.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(rollup.actual_cost) FILTER (
					WHERE rollup.bucket_date = $3::date
				), 0) AS yesterday_cost
			FROM usage_group_daily_rollups rollup
			WHERE $4::boolean
				AND rollup.bucket_date < $5::date
				AND NOT EXISTS (SELECT 1 FROM dirty_days d WHERE d.bucket_date=rollup.bucket_date)
			GROUP BY rollup.group_id
		), dirty AS (
			SELECT a.group_id,SUM(a.actual_cost) AS actual_cost,
				COALESCE(SUM(a.actual_cost) FILTER (WHERE d.bucket_date=$3::date),0) AS yesterday_cost
			FROM dirty_days d CROSS JOIN LATERAL (
				SELECT ul.group_id,COALESCE(SUM(ul.actual_cost),0) AS actual_cost
				FROM usage_logs ul WHERE ul.group_id IS NOT NULL
					AND ul.created_at>=d.bucket_date::timestamp AT TIME ZONE $6::text
					AND ul.created_at<(d.bucket_date+1)::timestamp AT TIME ZONE $6::text
				GROUP BY ul.group_id
			) a GROUP BY a.group_id
		),
		tail AS (
			SELECT
				ul.group_id,
				COALESCE(SUM(ul.actual_cost), 0) AS actual_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (WHERE ul.created_at >= $1), 0) AS today_cost,
				COALESCE(SUM(ul.actual_cost) FILTER (
					WHERE ul.created_at >= $2
						AND ul.created_at < $1
				), 0) AS yesterday_cost
			FROM usage_logs ul
			WHERE ul.created_at >= $7
			GROUP BY ul.group_id
		), totals AS (
		SELECT g.id AS group_id,
			COALESCE(historical.actual_cost, 0) + COALESCE(dirty.actual_cost, 0) + COALESCE(tail.actual_cost, 0) AS total_cost,
			COALESCE(tail.today_cost, 0) AS today_cost,
			COALESCE(historical.yesterday_cost, 0) + COALESCE(dirty.yesterday_cost, 0) + COALESCE(tail.yesterday_cost, 0) AS yesterday_cost
		FROM groups g
		LEFT JOIN historical ON historical.group_id = g.id
		LEFT JOIN dirty ON dirty.group_id = g.id
		LEFT JOIN tail ON tail.group_id = g.id
		)
		SELECT ready.ok,totals.group_id,totals.total_cost,totals.today_cost,totals.yesterday_cost
		FROM ready LEFT JOIN totals ON ready.ok ORDER BY totals.group_id
	`

	rows, err := r.sql.QueryContext(
		ctx,
		query,
		todayStart,
		yesterdayStart,
		yesterdayDate,
		state.valid,
		state.closedBefore,
		timezoneName,
		state.tailStart,
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]usagestats.GroupUsageSummary, 0)
	for rows.Next() {
		var ready bool
		var id sql.NullInt64
		var total, today, yesterday sql.NullFloat64
		if err := rows.Scan(&ready, &id, &total, &today, &yesterday); err != nil {
			return nil, err
		}
		if !ready {
			return nil, errUsageRollupNotReady
		}
		if id.Valid {
			results = append(results, usagestats.GroupUsageSummary{GroupID: id.Int64, TotalCost: total.Float64, TodayCost: today.Float64, YesterdayCost: yesterday.Float64})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// SyncGroupUsageRollups publishes bounded days in separate transactions. A slow
// day cannot roll back earlier progress or hold a lock needed by billing writes.
func (r *dashboardAggregationRepository) SyncGroupUsageRollups(ctx context.Context, todayStart time.Time) error {
	if r == nil || r.sql == nil {
		return nil
	}
	todayStart = service.GroupUsageTodayStart(todayStart)
	for range 32 {
		var processed int
		if err := scanSingleRow(ctx, r.sql, "SELECT process_group_usage_rollups($1, $2)",
			[]any{todayStart.UTC(), service.GroupUsageTimezoneName()}, &processed); err != nil {
			return fmt.Errorf("刷新分组用量日桶: %w", err)
		}
		if processed == 0 {
			return nil
		}
	}
	return nil
}

// Recompute marks exactly the requested UTC days. Readers use source rows for
// these days until the background consumer publishes their replacement buckets.
func invalidateGroupUsageRollupsRange(ctx context.Context, sqlq sqlExecutor, start, end time.Time) error {
	_, err := sqlq.ExecContext(ctx, `
		INSERT INTO usage_group_rollup_events(bucket_start)
		SELECT day AT TIME ZONE 'UTC' FROM generate_series(
			date_trunc('day', $1::timestamptz AT TIME ZONE 'UTC'),
			date_trunc('day', ($2::timestamptz - interval '1 microsecond') AT TIME ZONE 'UTC'),
			interval '1 day'
		) day
	`, start.UTC(), end.UTC())
	return err
}
