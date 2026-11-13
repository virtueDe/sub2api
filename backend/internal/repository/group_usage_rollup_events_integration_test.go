//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func groupRollupConn(t *testing.T, schema string) *sql.Conn {
	t.Helper()
	conn, err := integrationDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), "SET search_path TO "+pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = conn.ExecContext(context.Background(), "DISCARD ALL"); _ = conn.Close() })
	return conn
}

func groupSQL(t *testing.T, db sqlExecutor, query string, args ...any) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), query, args...)
	require.NoError(t, err)
}

func drainGroupRollups(t *testing.T, db sqlExecutor, today time.Time, zone string) {
	t.Helper()
	for range 256 {
		var processed int
		require.NoError(t, scanSingleRow(context.Background(), db, "SELECT process_group_usage_rollups($1,$2)", []any{today, zone}, &processed))
		if processed == 0 {
			return
		}
	}
	t.Fatal("group rollup did not drain")
}

func groupCost(t *testing.T, db sqlExecutor, today time.Time, total, wantedToday, yesterday float64) {
	t.Helper()
	got, err := newUsageLogRepositoryWithSQL(nil, db).GetAllGroupUsageSummary(context.Background(), today)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.InDelta(t, total, got[0].TotalCost, 1e-9)
	require.InDelta(t, wantedToday, got[0].TodayCost, 1e-9)
	require.InDelta(t, yesterday, got[0].YesterdayCost, 1e-9)
}

func groupScalar(t *testing.T, db sqlExecutor, query string) int {
	t.Helper()
	var value int
	require.NoError(t, scanSingleRow(context.Background(), db, query, nil, &value))
	return value
}

func TestGroupUsageEventsPreserveLateLowIDCommitAcrossMidnight(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	ctx := context.Background()
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	groupSQL(t, conn, "INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1)")
	// The transaction starts before publication and reserves the lower event ID.
	late := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = late.Rollback() }()
	groupSQL(t, late, "INSERT INTO usage_logs VALUES(1,1,10,3,'2026-08-13 23:59:59+08')")
	groupSQL(t, conn, "INSERT INTO usage_logs VALUES(2,1,10,4,'2026-08-13 23:59:58+08')")
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	require.Zero(t, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupCost(t, conn, today, 4, 0, 4)
	require.NoError(t, late.Commit())
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupCost(t, conn, today, 7, 0, 7)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupCost(t, conn, today, 7, 0, 7)
}

func waitGroupConsumerBlocked(t *testing.T, ctx context.Context, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, "SELECT cardinality(pg_blocking_pids($1))>0", pid).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond)
}

func TestGroupUsageEventsCommitDuringAggregationRemainsDirty(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	groupSQL(t, conn, "INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1); INSERT INTO usage_logs VALUES(1,1,10,1,'2026-08-13 12:00+08')")
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupSQL(t, conn, "UPDATE usage_logs SET actual_cost=2")
	blocker := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = blocker.Rollback() }()
	groupSQL(t, blocker, "SELECT * FROM usage_group_daily_rollups FOR UPDATE")
	consumer := groupRollupConn(t, schema)
	var pid int
	require.NoError(t, consumer.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	result := make(chan error, 1)
	go func() {
		_, err := consumer.ExecContext(ctx, "SELECT process_group_usage_rollups($1,'Asia/Shanghai')", today)
		result <- err
	}()
	waitGroupConsumerBlocked(t, ctx, pid)
	// A peer consumer skips promptly; the producer commits while aggregation waits.
	require.Zero(t, groupScalar(t, conn, "SELECT process_group_usage_rollups('2026-08-14 00:00+08','Asia/Shanghai')"))
	writeCtx, writeCancel := context.WithTimeout(ctx, time.Second)
	defer writeCancel()
	_, err := conn.ExecContext(writeCtx, "INSERT INTO usage_logs VALUES(2,1,10,4,'2026-08-13 13:00+08')")
	require.NoError(t, err)
	groupCost(t, conn, today, 6, 0, 6)
	require.NoError(t, blocker.Commit())
	require.NoError(t, <-result)
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupCost(t, conn, today, 6, 0, 6)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupCost(t, conn, today, 6, 0, 6)
}

func TestGroupUsageEventsRetentionDoesNotRewindNinetyDays(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), true)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)
	groupSQL(t, conn, `INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1);
 INSERT INTO usage_logs SELECT d,1,10,1,'2026-08-15 12:00+08'::timestamptz+(d-1)*interval '24 hours' FROM generate_series(1,90) d`)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupSQL(t, conn, "CREATE TEMP TABLE old_group_buckets AS SELECT * FROM usage_group_daily_rollups")
	cutoff := time.Date(2026, 8, 15, 5, 0, 0, 0, time.UTC)
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(conn).cleanupUsageLogsBatches(context.Background(), cutoff))
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_state WHERE closed_before='2026-11-13'"))
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupCost(t, conn, today, 89, 0, 1)
	require.Equal(t, 1, groupScalar(t, conn, "SELECT process_group_usage_rollups('2026-11-13 00:00+08','Asia/Shanghai')"))
	require.Zero(t, groupScalar(t, conn, "SELECT process_group_usage_rollups('2026-11-13 00:00+08','Asia/Shanghai')"))
	require.Zero(t, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_daily_rollups r JOIN old_group_buckets o USING(bucket_date,group_id) WHERE r.computed_at<>o.computed_at"))
	groupCost(t, conn, today, 89, 0, 1)
	// History older than the original retained_from must never be hidden.
	groupSQL(t, conn, "INSERT INTO usage_logs VALUES(999,1,10,7,'2026-07-01 12:00+08')")
	groupCost(t, conn, today, 96, 0, 1)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupCost(t, conn, today, 96, 0, 1)
	// Manual cleanup uses the same transactional trigger capture.
	deleted, err := (&usageCleanupRepository{sql: conn}).DeleteUsageLogsBatch(context.Background(), service.UsageCleanupFilters{StartTime: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)}, 10)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	groupCost(t, conn, today, 89, 0, 1)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupCost(t, conn, today, 89, 0, 1)
}

func TestGroupUsageEventsMutationCoverageAndBoundedAcknowledgment(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), true)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	groupSQL(t, conn, `CREATE TABLE usage_logs_august PARTITION OF usage_logs FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
 SELECT install_usage_group_rollup_partition_triggers('usage_logs_august');
 INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1);
 INSERT INTO usage_logs VALUES(1,1,10,2,'2026-07-31 23:59Z');
 INSERT INTO usage_logs_august VALUES(2,1,10,3,'2026-08-13 23:59+08')`)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupCost(t, conn, today, 5, 0, 3)
	groupSQL(t, conn, "UPDATE usage_logs SET actual_cost=actual_cost; UPDATE usage_logs SET user_id=user_id")
	require.Zero(t, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupSQL(t, conn, "UPDATE usage_logs SET created_at='2026-08-14 00:01+08',actual_cost=4 WHERE id=1")
	groupCost(t, conn, today, 7, 4, 3)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupSQL(t, conn, "UPDATE usage_logs_august SET group_id=NULL WHERE id=2")
	groupCost(t, conn, today, 4, 4, 0)
	groupSQL(t, conn, "UPDATE usage_logs_august SET group_id=10 WHERE id=2; DELETE FROM usage_logs_august WHERE id=1")
	groupCost(t, conn, today, 3, 0, 3)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupSQL(t, conn, "INSERT INTO usage_group_rollup_events(bucket_start) SELECT '2026-08-13 00:00Z'::timestamptz FROM generate_series(1,513)")
	require.Equal(t, 1, groupScalar(t, conn, "SELECT process_group_usage_rollups('2026-08-14 00:00+08','Asia/Shanghai')"))
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupSQL(t, conn, "DELETE FROM groups WHERE id=10")
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	got, err := newUsageLogRepositoryWithSQL(nil, conn).GetAllGroupUsageSummary(context.Background(), today)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestGroupUsageEventsUTCDayCoversBothLocalDaysAndDST(t *testing.T) {
	for _, scenario := range []struct {
		zone, today, first, second string
		yesterday                  float64
	}{
		{"Asia/Shanghai", "2026-08-14T00:00:00+08:00", "2026-08-12 17:00Z", "2026-08-12 01:00Z", 3},
		{"America/New_York", "2026-03-09T00:00:00-04:00", "2026-03-08 05:30Z", "2026-03-08 04:30Z", 3},
		{"America/New_York", "2026-11-02T00:00:00-05:00", "2026-11-01 06:30Z", "2026-11-01 05:30Z", 10},
	} {
		t.Run(scenario.today, func(t *testing.T) {
			useGroupUsageRepositoryTestTimezone(t, scenario.zone)
			today, err := time.Parse(time.RFC3339, scenario.today)
			require.NoError(t, err)
			schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), false)
			conn := groupRollupConn(t, schema)
			groupSQL(t, conn, "INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1)")
			groupSQL(t, conn, "INSERT INTO usage_logs VALUES(1,1,10,3,$1),(2,1,10,7,$2)", scenario.first, scenario.second)
			require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
			drainGroupRollups(t, conn, today, scenario.zone)
			groupCost(t, conn, today, 10, 0, scenario.yesterday)
			groupSQL(t, conn, "UPDATE usage_logs SET actual_cost=actual_cost*2")
			groupCost(t, conn, today, 20, 0, scenario.yesterday*2)
			drainGroupRollups(t, conn, today, scenario.zone)
			groupCost(t, conn, today, 20, 0, scenario.yesterday*2)
		})
	}
}

type groupSummaryStateRace struct {
	sqlExecutor
	beforeData func()
	query      string
}

func (q *groupSummaryStateRace) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "WITH ready AS") {
		q.query = query
		if q.beforeData != nil {
			q.beforeData()
			q.beforeData = nil
		}
	}
	return q.sqlExecutor.QueryContext(ctx, query, args...)
}

func TestGroupUsageSummaryRejectsStateChangeBetweenQueries(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), false)
	conn := groupRollupConn(t, schema)
	groupSQL(t, conn, "UPDATE usage_group_rollup_state SET closed_before='2026-08-14'")
	race := &groupSummaryStateRace{sqlExecutor: conn, beforeData: func() { groupSQL(t, conn, "UPDATE usage_group_rollup_state SET closed_before='2026-08-13'") }}
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	_, err := newUsageLogRepositoryWithSQL(nil, race).GetAllGroupUsageSummary(context.Background(), today)
	require.ErrorIs(t, err, errUsageRollupNotReady, "must also detect the race with zero groups")
}

func TestGroupUsageRollupEmptyInitializationAndFutureWatermark(t *testing.T) {
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), false)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_state WHERE closed_before='2026-08-14'"))
	_, err := conn.ExecContext(context.Background(), "SELECT process_group_usage_rollups($1,'Asia/Shanghai')", today.Add(-24*time.Hour))
	require.ErrorContains(t, err, "watermark is in the future")
	_, err = conn.ExecContext(context.Background(), "SELECT process_group_usage_rollups($1,'Asia/Shanghai')", today.Add(time.Hour))
	require.ErrorContains(t, err, "local midnight")
}

func TestGroupUsagePartitionDropQueuesActualDaysAndRollsBackOnFailure(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), true)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	groupSQL(t, conn, `ALTER TABLE usage_logs ADD COLUMN api_key_id bigint DEFAULT 1;
 CREATE TABLE usage_key_rollup_events(bucket_start timestamptz,api_key_id bigint,user_id bigint);
 CREATE TABLE usage_logs_202607 PARTITION OF usage_logs FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
 SELECT install_usage_group_rollup_partition_triggers('usage_logs_202607');
 INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1);
 INSERT INTO usage_logs(id,user_id,group_id,actual_cost,created_at) VALUES(1,1,10,3,'2026-07-10 00:00Z'),(2,1,10,4,'2026-08-13 00:00Z');
 CREATE VIEW protect_partition AS SELECT * FROM usage_logs_202607`)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	require.Error(t, dropUsageLogsPartitionWithRollupInvalidation(context.Background(), conn, "usage_logs_202607"))
	require.Zero(t, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupCost(t, conn, today, 7, 0, 4)
	groupSQL(t, conn, "DROP VIEW protect_partition")
	// A consumer already reading the parent must delay DROP before it locks
	// the leaf. Otherwise a concurrent parent/leaf lock inversion deadlocks.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	consumer := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = consumer.Rollback() }()
	groupSQL(t, consumer, "SELECT pg_advisory_xact_lock(742981313); SELECT COUNT(*) FROM usage_logs")
	dropConn := groupRollupConn(t, schema)
	var pid int
	require.NoError(t, dropConn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	result := make(chan error, 1)
	go func() { result <- dropUsageLogsPartitionWithRollupInvalidation(ctx, dropConn, "usage_logs_202607") }()
	waitGroupConsumerBlocked(t, ctx, pid)
	readCtx, readCancel := context.WithTimeout(ctx, time.Second)
	var count int
	err := conn.QueryRowContext(readCtx, "SELECT COUNT(*) FROM usage_logs_202607").Scan(&count)
	readCancel()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.NoError(t, consumer.Commit())
	require.NoError(t, <-result)
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	groupCost(t, conn, today, 4, 0, 4)
	drainGroupRollups(t, conn, today, "Asia/Shanghai")
	groupCost(t, conn, today, 4, 0, 4)
}

func TestGroupUsageSummaryGenericPlanKeepsTimestampIndex(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), false)
	conn := groupRollupConn(t, schema)
	groupSQL(t, conn, `INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1);
 INSERT INTO usage_logs SELECT d,1,10,1,'2026-01-01'::timestamptz+d*interval '10 minutes' FROM generate_series(1,20000) d;
 ANALYZE usage_logs; SET plan_cache_mode=force_generic_plan`)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	// Seed a valid watermark; query capture is only used to inspect the real reader.
	groupSQL(t, conn, "UPDATE usage_group_rollup_state SET closed_before='2026-08-14'")
	capture := &groupSummaryStateRace{sqlExecutor: conn}
	_, err := newUsageLogRepositoryWithSQL(nil, capture).GetAllGroupUsageSummary(context.Background(), today)
	require.NoError(t, err)
	groupSQL(t, conn, "PREPARE group_summary(timestamptz,timestamptz,date,boolean,date,text,timestamptz) AS "+capture.query)
	rows, err := conn.QueryContext(context.Background(), `EXPLAIN EXECUTE group_summary('2026-08-14 00:00+08','2026-08-13 00:00+08','2026-08-13',true,'2026-08-14','Asia/Shanghai','2026-08-14 00:00+08')`)
	require.NoError(t, err)
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		fmt.Fprintln(&plan, line)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Contains(t, plan.String(), "Index Cond: (created_at >= $7)")
	t.Log(plan.String())
}

func TestGroupUsageSyncKeepsCommittedDaysAfterLaterFailure(t *testing.T) {
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), false)
	conn := groupRollupConn(t, schema)
	today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	groupSQL(t, conn, `INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1);
 INSERT INTO usage_logs VALUES(1,1,10,2,'2026-08-12 12:00+08'),(2,1,10,3,'2026-08-13 12:00+08');
 CREATE FUNCTION reject_second_day() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.bucket_date='2026-08-13' THEN RAISE EXCEPTION 'injected second day failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_day BEFORE INSERT ON usage_group_daily_rollups FOR EACH ROW EXECUTE FUNCTION reject_second_day()`)
	err := newDashboardAggregationRepositoryWithSQL(conn).SyncGroupUsageRollups(context.Background(), today)
	require.ErrorContains(t, err, "injected second day failure")
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_state WHERE closed_before='2026-08-13'"))
	require.Equal(t, 1, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_daily_rollups WHERE bucket_date='2026-08-12' AND actual_cost=2"))
	groupCost(t, conn, today, 5, 0, 3)
	groupSQL(t, conn, "DROP TRIGGER fail_day ON usage_group_daily_rollups")
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(conn).SyncGroupUsageRollups(context.Background(), today))
	groupCost(t, conn, today, 5, 0, 3)
}

func TestGroupUsageRecomputeRangeEventsRespectExclusiveEnd(t *testing.T) {
	schema := createGroupUsageRollupTriggerTestSchema(t, context.Background(), false)
	conn := groupRollupConn(t, schema)
	start := time.Date(2026, 8, 12, 23, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	require.NoError(t, invalidateGroupUsageRollupsRange(context.Background(), conn, start, end))
	require.Equal(t, 2, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events"))
	require.Zero(t, groupScalar(t, conn, "SELECT COUNT(*) FROM usage_group_rollup_events WHERE bucket_start>='2026-08-14 00:00Z'"))
}
