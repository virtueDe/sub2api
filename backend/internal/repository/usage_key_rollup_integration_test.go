//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func usageRollupFixture(t *testing.T, beforeMigration ...string) (*sql.Conn, string) {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("usage_rollup_%d", time.Now().UnixNano())
	_, err := integrationDB.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, "RESET search_path")
		_ = conn.Close()
		_, _ = integrationDB.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	})
	_, err = conn.ExecContext(ctx, `CREATE TABLE usage_logs(id bigserial,created_at timestamptz NOT NULL,api_key_id bigint NOT NULL,user_id bigint NOT NULL,input_tokens bigint DEFAULT 0,output_tokens bigint DEFAULT 0,cache_creation_tokens bigint DEFAULT 0,cache_read_tokens bigint DEFAULT 0,total_cost numeric(20,10) DEFAULT 0,actual_cost numeric(20,10) DEFAULT 0,duration_ms integer) PARTITION BY RANGE(created_at);
 CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT;
 CREATE INDEX ON usage_logs(created_at);`)
	require.NoError(t, err)
	for _, query := range beforeMigration {
		_, err = conn.ExecContext(ctx, query)
		require.NoError(t, err)
	}
	migration, err := migrations.FS.ReadFile("251_usage_key_hourly_rollups.sql")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	return conn, schema
}
func drainUsageRollups(t *testing.T, conn *sql.Conn) {
	t.Helper()
	for i := 0; i < 100; i++ {
		var n int
		require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT process_usage_key_rollups()").Scan(&n))
		if n == 0 {
			return
		}
	}
	t.Fatal("rollup did not drain")
}
func TestUsageKeyRollupMutationCoverageAndBoundaries(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,input_tokens,actual_cost,duration_ms) VALUES
 ('2026-11-09 00:10Z',7,8,10,0.1,100),('2026-11-09 00:40Z',7,8,20,0.2,NULL),('2026-11-09 01:10Z',7,8,30,0.3,300)`)
	require.NoError(t, err)
	repo := newUsageLogRepositoryWithSQL(nil, conn)
	repo.rollupReads = true
	_, err = repo.GetAPIKeyTotalStats(ctx, 7)
	require.ErrorIs(t, err, errUsageRollupNotReady)
	drainUsageRollups(t, conn)
	total, err := repo.GetAPIKeyTotalStats(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, int64(3), total.TotalRequests)
	require.Equal(t, 200.0, total.AverageDurationMs)
	require.InDelta(t, 0.6, total.TotalActualCost, 1e-10)
	start := time.Date(2026, 11, 9, 0, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	rangeStats, err := repo.GetAPIKeyStatsAggregated(ctx, 7, start, end)
	require.NoError(t, err)
	require.Equal(t, int64(2), rangeStats.TotalRequests)
	require.Equal(t, int64(50), rangeStats.TotalInputTokens)
	require.Equal(t, 150.0, rangeStats.AverageDurationMs)
	trend, err := repo.readUsageRollupTrend(ctx, 8, 7, start, end, "day")
	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.Equal(t, int64(2), trend[0].Requests)
	// A direct leaf insert must capture changes just like parent-targeted batches.
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs_default(created_at,api_key_id,user_id,input_tokens,actual_cost) VALUES('2026-11-09 00:45Z',7,8,40,0.4)`)
	require.NoError(t, err)
	_, err = repo.GetAPIKeyTotalStats(ctx, 7)
	require.ErrorIs(t, err, errUsageRollupNotReady)
	drainUsageRollups(t, conn)
	_, err = conn.ExecContext(ctx, `UPDATE usage_logs SET api_key_id=9,user_id=10,created_at='2026-11-09 02:00Z',actual_cost=0.8 WHERE input_tokens=40;
 DELETE FROM usage_logs WHERE api_key_id=7`)
	require.NoError(t, err)
	drainUsageRollups(t, conn)
	total, err = repo.GetAPIKeyTotalStats(ctx, 7)
	require.NoError(t, err)
	require.Zero(t, total.TotalRequests)
	total, err = repo.GetAPIKeyTotalStats(ctx, 9)
	require.NoError(t, err)
	require.Equal(t, int64(1), total.TotalRequests)
	require.Equal(t, 0.8, total.TotalActualCost)
	_, err = conn.ExecContext(ctx, "TRUNCATE usage_logs_default")
	require.ErrorContains(t, err, "TRUNCATE is disabled")
}

func TestUsageKeyRollupHourlyTrendMatchesDetail(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,input_tokens,output_tokens,actual_cost,total_cost) VALUES
 ('2026-11-09 23:10Z',7,8,10,1,0.1,0.2),('2026-11-09 23:40Z',7,8,20,2,0.2,0.4),
 ('2026-11-10 00:10Z',7,8,30,3,0.3,0.6),('2026-11-10 01:10Z',9,8,40,4,0.4,0.8),
 ('2026-11-10 01:40Z',7,8,50,5,0.5,1),('2026-11-10 00:10Z',10,11,60,6,0.6,1.2)`)
	require.NoError(t, err)
	start := time.Date(2026, 11, 9, 23, 30, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	repo := newUsageLogRepositoryWithSQL(nil, conn)
	repo.rollupReads = true
	_, err = repo.GetUsageTrendWithUsageFilters(ctx, start, end, "hour", usagestats.UsageLogFilters{UserID: 8})
	require.ErrorIs(t, err, errUsageRollupNotReady, "hourly queries must honor readiness instead of scanning detail")
	drainUsageRollups(t, conn)
	for _, filters := range []usagestats.UsageLogFilters{{UserID: 8}, {APIKeyID: 7}, {UserID: 8, APIKeyID: 7}} {
		for _, granularity := range []string{"hour", "day"} {
			repo.rollupReads = false
			detail, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, granularity, filters)
			require.NoError(t, err)
			repo.rollupReads = true
			rollup, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, granularity, filters)
			require.NoError(t, err)
			require.NotEmpty(t, rollup)
			require.Equal(t, detail, rollup, "granularity=%s filters=%+v", granularity, filters)
		}
	}
	// The configured key canary must still exclude a user-wide query.
	repo.rollupKeys = []int64{7}
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES('2026-11-10 00:20Z',7,8)`)
	require.NoError(t, err)
	_, err = repo.GetUsageTrendWithUsageFilters(ctx, start, end, "hour", usagestats.UsageLogFilters{UserID: 8, APIKeyID: 7})
	require.ErrorIs(t, err, errUsageRollupNotReady)
	_, err = repo.GetUsageTrendWithUsageFilters(ctx, start, end, "hour", usagestats.UsageLogFilters{UserID: 8})
	require.NoError(t, err)
}

func TestUsageKeyRollupTrendTimezoneTransitions(t *testing.T) {
	for _, tc := range []struct{ name, zone, start, end, first, second string }{
		{"half-hour DST", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-04T16:15:00Z", "2026-04-04T15:10:00Z", "2026-04-04T15:40:00Z"},
		{"half-hour midnight", "Australia/Lord_Howe", "2026-04-04T14:45:00Z", "2026-04-05T14:15:00Z", "2026-04-05T13:10:00Z", "2026-04-05T13:40:00Z"},
		{"non-hour transition", "America/Goose_Bay", "2010-11-07T03:00:00Z", "2010-11-07T04:00:00Z", "2010-11-07T03:00:30Z", "2010-11-07T03:10:00Z"},
		{"repeated hour", "America/New_York", "2026-11-01T05:00:00Z", "2026-11-01T08:00:00Z", "2026-11-01T05:10:00Z", "2026-11-01T06:10:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useGroupUsageRepositoryTestTimezone(t, tc.zone)
			conn, _ := usageRollupFixture(t)
			ctx := context.Background()
			_, err := conn.ExecContext(ctx, "SELECT set_config('TimeZone', $1, false)", tc.zone)
			require.NoError(t, err)
			t.Cleanup(func() { _, _ = conn.ExecContext(ctx, "RESET TimeZone") })
			_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,input_tokens,actual_cost) VALUES ($1,7,8,10,0.1),($2,7,8,20,0.2)`, tc.first, tc.second)
			require.NoError(t, err)
			drainUsageRollups(t, conn)
			start, err := time.Parse(time.RFC3339, tc.start)
			require.NoError(t, err)
			end, err := time.Parse(time.RFC3339, tc.end)
			require.NoError(t, err)
			repo := newUsageLogRepositoryWithSQL(nil, conn)
			for _, granularity := range []string{"hour", "day"} {
				repo.rollupReads = false
				detail, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, granularity, usagestats.UsageLogFilters{APIKeyID: 7})
				require.NoError(t, err)
				repo.rollupReads = true
				rollup, err := repo.GetUsageTrendWithUsageFilters(ctx, start, end, granularity, usagestats.UsageLogFilters{APIKeyID: 7})
				require.NoError(t, err)
				require.NotEmpty(t, rollup)
				require.Equal(t, detail, rollup, granularity)
			}
		})
	}
}

func TestUsageKeyRollupLateCommitCannotBeSkipped(t *testing.T) {
	conn, schema := usageRollupFixture(t)
	ctx := context.Background()
	other, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _, _ = other.ExecContext(ctx, "RESET search_path"); _ = other.Close() }()
	_, err = other.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	tx, err := other.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES('2026-11-09 00:10Z',1,2,0.1)`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES('2026-11-09 00:20Z',1,2,0.2)`)
	require.NoError(t, err)
	drainUsageRollups(t, conn)
	require.NoError(t, tx.Commit())
	repo := newUsageLogRepositoryWithSQL(nil, conn)
	repo.rollupReads = true
	_, err = repo.GetAPIKeyTotalStats(ctx, 1)
	require.ErrorIs(t, err, errUsageRollupNotReady)
	drainUsageRollups(t, conn)
	value, err := repo.GetAPIKeyTotalStats(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), value.TotalRequests)
	require.InDelta(t, 0.3, value.TotalActualCost, 1e-10)
}

func TestUsageKeyRollupBackfillAndReconciliation(t *testing.T) {
	ctx := context.Background()
	conn, _ := usageRollupFixture(t, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES
 ('2026-11-09 00:10Z',1,2,0.1),('2026-11-09 02:10Z',1,2,0.2)`)
	repo := newUsageLogRepositoryWithSQL(nil, conn)
	repo.rollupReads = true
	for i := 0; i < 2; i++ {
		_, err := conn.ExecContext(ctx, "SELECT process_usage_key_rollups()")
		require.NoError(t, err)
		_, err = repo.GetAPIKeyTotalStats(ctx, 1)
		require.ErrorIs(t, err, errUsageRollupNotReady)
	}
	drainUsageRollups(t, conn)
	value, err := repo.GetAPIKeyTotalStats(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), value.TotalRequests)
	worker := &usageKeyRollupRepository{db: conn}
	result, err := worker.Reconcile(ctx)
	require.NoError(t, err)
	require.Equal(t, "match", result)
	// Corrupt the next, originally empty hour: missing/extra identities must fail.
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_key_hourly_rollups SELECT bucket_start+interval '1 hour',api_key_id,user_id,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum,duration_count,computed_at FROM usage_key_hourly_rollups WHERE bucket_start='2026-11-09 00:00Z'`)
	require.NoError(t, err)
	result, err = worker.Reconcile(ctx)
	require.NoError(t, err)
	require.Equal(t, "mismatch", result)
	_, err = repo.GetAPIKeyTotalStats(ctx, 1)
	require.ErrorIs(t, err, errUsageRollupNotReady)
}

func TestUsageKeyRollupCrossPartitionMove(t *testing.T) {
	ctx := context.Background()
	conn, _ := usageRollupFixture(t, `CREATE TABLE usage_logs_202610 PARTITION OF usage_logs FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
 CREATE TABLE usage_logs_202611 PARTITION OF usage_logs FOR VALUES FROM ('2026-11-01') TO ('2026-12-01')`)
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES('2026-10-31 23:59Z',1,2,0.1)`)
	require.NoError(t, err)
	drainUsageRollups(t, conn)
	_, err = conn.ExecContext(ctx, `UPDATE usage_logs SET created_at='2026-11-01 00:01Z',api_key_id=3,user_id=4`)
	require.NoError(t, err)
	repo := newUsageLogRepositoryWithSQL(nil, conn)
	repo.rollupReads = true
	for _, key := range []int64{1, 3} {
		_, err = repo.GetAPIKeyTotalStats(ctx, key)
		require.ErrorIs(t, err, errUsageRollupNotReady)
	}
	drainUsageRollups(t, conn)
	old, err := repo.GetAPIKeyTotalStats(ctx, 1)
	require.NoError(t, err)
	require.Zero(t, old.TotalRequests)
	newValue, err := repo.GetAPIKeyTotalStats(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, int64(1), newValue.TotalRequests)
	// Future partitions are published with their direct-write triggers installed.
	maintenance := newDashboardAggregationRepositoryWithSQL(conn)
	require.NoError(t, maintenance.createUsageLogsPartition(ctx, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)))
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs_202612(created_at,api_key_id,user_id) VALUES('2026-12-01 00:00Z',3,4)`)
	require.NoError(t, err)
	_, err = repo.GetAPIKeyTotalStats(ctx, 3)
	require.ErrorIs(t, err, errUsageRollupNotReady)
	drainUsageRollups(t, conn)
	_, err = conn.ExecContext(ctx, `CREATE TABLE usage_group_rollup_state(id smallint PRIMARY KEY,closed_before date,updated_at timestamptz);
 INSERT INTO usage_group_rollup_state VALUES(1,'2026-12-02',now())`)
	require.NoError(t, err)
	require.NoError(t, dropUsageLogsPartitionWithRollupInvalidation(ctx, conn, "usage_logs_202612", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)))
	_, err = repo.GetAPIKeyTotalStats(ctx, 3)
	require.ErrorIs(t, err, errUsageRollupNotReady)
	drainUsageRollups(t, conn)
	newValue, err = repo.GetAPIKeyTotalStats(ctx, 3)
	require.NoError(t, err)
	require.Equal(t, int64(1), newValue.TotalRequests)
}
