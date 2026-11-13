//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func applyTargetedUsageRollupMigration(t *testing.T, conn *sql.Conn) {
	t.Helper()
	migration, err := migrations.FS.ReadFile("254_usage_key_rollup_targeted_refresh.sql")
	require.NoError(t, err)
	_, err = conn.ExecContext(context.Background(), string(migration))
	require.NoError(t, err)
}

func assertUsageRollupsMatchSource(t *testing.T, conn *sql.Conn) {
	t.Helper()
	drainUsageRollups(t, conn)
	var differences int
	err := conn.QueryRowContext(context.Background(), `WITH expected AS (
 SELECT date_trunc('hour',created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' bucket_start,api_key_id,user_id,count(*) requests,
 COALESCE(sum(input_tokens),0) input_tokens,COALESCE(sum(output_tokens),0) output_tokens,
 COALESCE(sum(cache_creation_tokens),0) cache_creation_tokens,COALESCE(sum(cache_read_tokens),0) cache_read_tokens,
 COALESCE(sum(total_cost),0) total_cost,COALESCE(sum(actual_cost),0) actual_cost,
 COALESCE(sum(duration_ms::numeric),0) duration_sum,count(duration_ms) duration_count
 FROM usage_logs GROUP BY 1,2,3
 ), actual AS (
 SELECT bucket_start,api_key_id,user_id,requests,input_tokens,output_tokens,cache_creation_tokens,cache_read_tokens,total_cost,actual_cost,duration_sum,duration_count FROM usage_key_hourly_rollups
 ), difference AS (
 (SELECT * FROM expected EXCEPT ALL SELECT * FROM actual) UNION ALL (SELECT * FROM actual EXCEPT ALL SELECT * FROM expected)
 ) SELECT count(*) FROM difference`).Scan(&differences)
	require.NoError(t, err)
	require.Zero(t, differences, "rollups must match source dimensions, counts, costs, tokens and nullable duration")
}

func TestUsageKeyRollupIgnoresUnchangedStatistics(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,input_tokens,duration_ms) VALUES
 ('2026-11-09 00:10Z',1,2,1,NULL),('2026-11-09 00:20Z',1,2,1,NULL),('2026-11-09 00:30Z',1,2,2,NULL)`)
	require.NoError(t, err)
	assertUsageRollupsMatchSource(t, conn)
	var before, after time.Time
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT computed_at FROM usage_key_hourly_rollups").Scan(&before))
	_, err = conn.ExecContext(ctx, `UPDATE usage_logs SET image_generation='{"test":true}';
 UPDATE usage_logs SET actual_cost=actual_cost,created_at=created_at+interval '1 minute'`)
	require.NoError(t, err)
	var events int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_events").Scan(&events))
	require.Zero(t, events)
	assertUsageRollupsMatchSource(t, conn)
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT computed_at FROM usage_key_hourly_rollups").Scan(&after))
	require.Equal(t, before, after)
	// Same distinct values, different multiplicities: EXCEPT (without ALL)
	// would lose this change from [1,1,2] to [2,2,1].
	_, err = conn.ExecContext(ctx, "UPDATE usage_logs SET input_tokens=3-input_tokens")
	require.NoError(t, err)
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_events").Scan(&events))
	require.Positive(t, events)
	assertUsageRollupsMatchSource(t, conn)
	for _, field := range []string{"input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_cost", "actual_cost", "duration_ms"} {
		t.Run(field, func(t *testing.T) {
			_, err := conn.ExecContext(ctx, fmt.Sprintf("UPDATE usage_logs SET %s=COALESCE(%s,0)+1", field, field))
			require.NoError(t, err)
			require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_events").Scan(&events))
			require.Positive(t, events)
			assertUsageRollupsMatchSource(t, conn)
		})
	}
	for _, value := range []string{"NULL", "0"} {
		_, err = conn.ExecContext(ctx, "UPDATE usage_logs SET duration_ms="+value)
		require.NoError(t, err)
		assertUsageRollupsMatchSource(t, conn)
	}
}

func TestUsageKeyRollupWritesOnlyChangedIdentities(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) SELECT '2026-11-09 00:10Z',v,v FROM generate_series(1,40) v`)
	require.NoError(t, err)
	assertUsageRollupsMatchSource(t, conn)
	var untouched time.Time
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT computed_at FROM usage_key_hourly_rollups WHERE api_key_id=40").Scan(&untouched))
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "SET LOCAL plan_cache_mode=force_generic_plan")
	require.NoError(t, err)
	const writeCounts = `SELECT pg_stat_get_xact_tuples_inserted('usage_key_hourly_rollups'::regclass),
 pg_stat_get_xact_tuples_updated('usage_key_hourly_rollups'::regclass),pg_stat_get_xact_tuples_deleted('usage_key_hourly_rollups'::regclass),
 pg_stat_get_xact_tuples_inserted('usage_key_rollup_events'::regclass)`
	var initialInsert, initialUpdate, initialDelete, initialEvents int64
	require.NoError(t, tx.QueryRowContext(ctx, writeCounts).Scan(&initialInsert, &initialUpdate, &initialDelete, &initialEvents))
	_, err = tx.ExecContext(ctx, `DO $$ BEGIN FOR i IN 1..60 LOOP
 INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES('2026-11-09 00:30Z',1,1,0.007);
 IF i%6=0 THEN UPDATE usage_logs SET image_generation=jsonb_build_object('test',i) WHERE id=i/6; END IF;
 PERFORM process_usage_key_rollups();
 END LOOP; END $$`)
	require.NoError(t, err)
	var inserted, updated, deleted, events int64
	require.NoError(t, tx.QueryRowContext(ctx, writeCounts).Scan(&inserted, &updated, &deleted, &events))
	// Compare the same backend's counters inside one transaction so fixture
	// writes still awaiting a statistics flush cannot affect the assertion.
	require.Equal(t, initialInsert, inserted, "existing identities must not be deleted and reinserted")
	require.Equal(t, initialDelete, deleted)
	require.Equal(t, int64(60), updated-initialUpdate)
	require.Equal(t, int64(60), events-initialEvents, "detail enrichment must not invalidate statistics")
	require.NoError(t, tx.Commit())
	var after time.Time
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT computed_at FROM usage_key_hourly_rollups WHERE api_key_id=40").Scan(&after))
	require.Equal(t, untouched, after)
	assertUsageRollupsMatchSource(t, conn)
}

func TestUsageKeyRollupTargetedUpgradePreservesPendingWork(t *testing.T) {
	conn, _ := usageRollupFixtureVersion(t, false, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES
 ('2026-11-09 00:10Z',1,1),('2026-11-09 02:10Z',2,2)`)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `SELECT process_usage_key_rollups();
 INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES('2026-11-09 02:20Z',3,3)`)
	require.NoError(t, err)
	applyTargetedUsageRollupMigration(t, conn)
	applyTargetedUsageRollupMigration(t, conn) // Idempotent replacement, no reset.
	assertUsageRollupsMatchSource(t, conn)
	var completed int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_backfill WHERE completed_at IS NOT NULL").Scan(&completed))
	require.Equal(t, 3, completed, "historical empty hours must still be completed")
}

func TestUsageKeyRollupTargetedNewHour(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	drainUsageRollups(t, conn) // Empty initialized database has no backfill rows.
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES
 ('2026-11-09 00:10Z',1,1),('2026-11-09 00:20Z',2,2)`)
	require.NoError(t, err)
	assertUsageRollupsMatchSource(t, conn)
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES('2026-11-10 00:10Z',3,3)`)
	require.NoError(t, err)
	assertUsageRollupsMatchSource(t, conn)
	var completed int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_backfill WHERE completed_at IS NOT NULL").Scan(&completed))
	require.Equal(t, 2, completed)
}

func TestUsageKeyRollupTargetedClaimLimitAndRollback(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) SELECT '2026-11-09 00:10Z',v,v FROM generate_series(1,600) v`)
	require.NoError(t, err)
	assertUsageRollupsMatchSource(t, conn)
	_, err = conn.ExecContext(ctx, "DELETE FROM usage_logs")
	require.NoError(t, err)
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, "SELECT process_usage_key_rollups()")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())
	var pending, remaining int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_events").Scan(&pending))
	require.Equal(t, 600, pending)
	_, err = conn.ExecContext(ctx, "SELECT process_usage_key_rollups()")
	require.NoError(t, err)
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_events").Scan(&pending))
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_hourly_rollups").Scan(&remaining))
	require.Equal(t, 88, pending)
	require.Equal(t, 88, remaining, "unclaimed identities remain guarded by pending events")
	assertUsageRollupsMatchSource(t, conn)
}

func TestUsageKeyRollupCommitDuringRefreshRemainsPending(t *testing.T) {
	conn, schema := usageRollupFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES('2026-11-09 00:10Z',1,1)`)
	require.NoError(t, err)
	assertUsageRollupsMatchSource(t, conn)
	lockConn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _, _ = lockConn.ExecContext(context.Background(), "RESET search_path"); _ = lockConn.Close() }()
	_, err = lockConn.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	lock, err := lockConn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = lock.Rollback() }()
	_, err = lock.ExecContext(ctx, "SELECT 1 FROM usage_key_hourly_rollups FOR UPDATE")
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES('2026-11-09 00:20Z',1,1)`)
	require.NoError(t, err)
	consumer, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _, _ = consumer.ExecContext(context.Background(), "RESET search_path"); _ = consumer.Close() }()
	_, err = consumer.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	var pid int
	require.NoError(t, consumer.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	done := make(chan error, 1)
	go func() { _, err := consumer.ExecContext(ctx, "SELECT process_usage_key_rollups()"); done <- err }()
	require.Eventually(t, func() bool {
		var waiting bool
		err := conn.QueryRowContext(ctx, "SELECT wait_event_type='Lock' FROM pg_stat_activity WHERE pid=$1", pid).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond, "consumer must reach the aggregate row-lock barrier")
	// The consumer owns the advisory lock and has already read its snapshot.
	var claimed int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT process_usage_key_rollups()").Scan(&claimed))
	require.Zero(t, claimed, "a second consumer must not enter")
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id) VALUES
 ('2026-11-09 00:30Z',1,1),('2026-11-09 00:30Z',2,2)`)
	require.NoError(t, err)
	require.NoError(t, lock.Commit())
	require.NoError(t, <-done)
	var pending int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT count(*) FROM usage_key_rollup_events").Scan(&pending))
	require.Equal(t, 2, pending)
	repo := newUsageLogRepositoryWithSQL(nil, conn)
	repo.rollupReads = true
	for _, key := range []int64{1, 2} {
		_, err = repo.GetAPIKeyTotalStats(ctx, key)
		require.ErrorIs(t, err, errUsageRollupNotReady)
	}
	assertUsageRollupsMatchSource(t, conn)
}
