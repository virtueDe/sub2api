//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBatchAPIKeyRollupMatchesDetailWithDirtyHoursAndBoundaries(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-4 * time.Hour)
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost)
 SELECT $1::timestamptz+n*interval '10 minutes',k,k+10,(n+1)::numeric/10
 FROM generate_series(0,23) n CROSS JOIN unnest(ARRAY[7,9]) k`, base)
	require.NoError(t, err)
	drainUsageRollups(t, conn)
	r := &usageLogRepository{sql: conn, rollupReads: true}
	compare := func(start, end, today time.Time) {
		t.Helper()
		got, err := r.readBatchAPIKeyRollup(ctx, []int64{7, 9, 99}, start, end, today)
		require.NoError(t, err)
		for _, id := range []int64{7, 9, 99} {
			var total, daily float64
			err := conn.QueryRowContext(ctx, `SELECT COALESCE(SUM(actual_cost) FILTER(WHERE created_at >= $2 AND created_at < $3),0),COALESCE(SUM(actual_cost) FILTER(WHERE created_at >= $4),0) FROM usage_logs WHERE api_key_id=$1`, id, start, end, today).Scan(&total, &daily)
			require.NoError(t, err)
			require.InDelta(t, total, got[id].TotalActualCost, 1e-9)
			require.InDelta(t, daily, got[id].TodayActualCost, 1e-9)
		}
	}
	checkWindows := func() {
		compare(base.Add(15*time.Minute), base.Add(150*time.Minute), base.Add(45*time.Minute))
		compare(base.Add(15*time.Minute), base.Add(35*time.Minute), base.Add(time.Hour))
		compare(base, base.Add(2*time.Hour), base.Add(2*time.Hour))
	}
	checkWindows()
	// Repeated events, identity/time changes, and removal of entire old buckets.
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES($1,7,17,0.1234567890),($1,7,17,0.2);
 `, base.Add(70*time.Minute))
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `UPDATE usage_logs SET api_key_id=9,user_id=19,created_at=$1,actual_cost=0.9 WHERE api_key_id=7 AND created_at=$2`, base.Add(125*time.Minute), base.Add(20*time.Minute))
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `DELETE FROM usage_logs WHERE api_key_id=9 AND created_at >= $1 AND created_at < $1::timestamptz+interval '1 hour'`, base)
	require.NoError(t, err)
	checkWindows() // The old scalar rollup reader would reject these pending events.
	drainUsageRollups(t, conn)
	checkWindows()
}

func TestBatchAPIKeyRollupReadinessAndBacklogNeverReturnFalseZero(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	r := &usageLogRepository{sql: conn, rollupReads: true}
	end := time.Now()
	read := func() {
		t.Helper()
		got, err := r.readBatchAPIKeyRollup(ctx, []int64{7}, end.Add(-time.Hour), end, end.Add(-time.Hour))
		require.ErrorIs(t, err, errUsageRollupNotReady)
		require.Nil(t, got)
	}
	read()
	drainUsageRollups(t, conn)
	_, err := conn.ExecContext(ctx, `UPDATE usage_key_rollup_state SET integrity_failed=true`)
	require.NoError(t, err)
	read()
	_, err = conn.ExecContext(ctx, `UPDATE usage_key_rollup_state SET integrity_failed=false; INSERT INTO usage_key_rollup_backfill(bucket_start) VALUES('2000-01-01')`)
	require.NoError(t, err)
	read()
	_, err = conn.ExecContext(ctx, `DELETE FROM usage_key_rollup_backfill WHERE completed_at IS NULL`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_key_rollup_events(bucket_start,api_key_id,user_id) SELECT $1::timestamptz+n*interval '1 hour',7,8 FROM generate_series(0,48) n`, end.Truncate(time.Hour))
	require.NoError(t, err)
	read()
	_, err = conn.ExecContext(ctx, `DELETE FROM usage_key_rollup_events`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_key_rollup_events(bucket_start,api_key_id,user_id) SELECT $1::timestamptz,7,8 FROM generate_series(1,4097)`, end.Truncate(time.Hour))
	require.NoError(t, err)
	read() // One dirty identity with many duplicate events must also be bounded.
}

func TestBatchAPIKeyRollupHonorsCanaryKeys(t *testing.T) {
	conn, _ := usageRollupFixture(t)
	ctx := context.Background()
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES(now()-interval '1 minute',9,10,1)`)
	require.NoError(t, err)
	r := &usageLogRepository{sql: conn, rollupReads: true, rollupKeys: []int64{7}}
	// Backfill is not ready, but an unlisted key still uses its existing detail path.
	stats, err := r.GetBatchAPIKeyUsageStats(ctx, []int64{9}, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1.0, stats[9].TotalActualCost)
	_, err = r.GetBatchAPIKeyUsageStats(ctx, []int64{7, 9}, time.Time{}, time.Time{})
	require.ErrorIs(t, err, errUsageRollupNotReady)
	r.batchRollupDisabled = true
	stats, err = r.GetBatchAPIKeyUsageStats(ctx, []int64{7, 9}, time.Time{}, time.Time{})
	require.NoError(t, err, "batch-only rollback must work without changing the global rollup flag")
	require.Equal(t, 1.0, stats[9].TotalActualCost)
	r.batchRollupDisabled = false
	drainUsageRollups(t, conn)
	stats, err = r.GetBatchAPIKeyUsageStats(ctx, []int64{7, 9}, time.Time{}, time.Time{})
	require.NoError(t, err)
	require.Zero(t, stats[7].TotalActualCost)
	require.Equal(t, 1.0, stats[9].TotalActualCost)
}

func TestBatchAPIKeyRollupSnapshotAndGenericPlan(t *testing.T) {
	conn, schema := usageRollupFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	_, err := conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES($1,7,8,1)`, base.Add(10*time.Minute))
	require.NoError(t, err)
	drainUsageRollups(t, conn)
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	r := &usageLogRepository{sql: tx}
	before, err := r.readBatchAPIKeyRollup(ctx, []int64{7}, base, base.Add(2*time.Hour), base)
	require.NoError(t, err)
	writer, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = writer.ExecContext(ctx, "RESET search_path"); _ = writer.Close() })
	_, err = writer.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) VALUES($1,7,8,2)`, base.Add(20*time.Minute))
	require.NoError(t, err)
	after, err := r.readBatchAPIKeyRollup(ctx, []int64{7}, base, base.Add(2*time.Hour), base)
	require.NoError(t, err)
	require.Equal(t, before, after, "event and amount sources must use the same MVCC snapshot")
	require.NoError(t, tx.Commit())
	r.sql = conn
	after, err = r.readBatchAPIKeyRollup(ctx, []int64{7}, base, base.Add(2*time.Hour), base)
	require.NoError(t, err)
	require.Equal(t, 3.0, after[7].TotalActualCost)
	// Ensure correlated detail lookup retains a Key/time index under generic plans.
	_, err = conn.ExecContext(ctx, `INSERT INTO usage_logs(created_at,api_key_id,user_id,actual_cost) SELECT $1::timestamptz+n*interval '1 second',1000+n,2000+n,1 FROM generate_series(1,2000) n`, base)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `ANALYZE usage_logs; SET plan_cache_mode=force_generic_plan; PREPARE batch_usage(bigint[],timestamptz,timestamptz,timestamptz,timestamptz) AS `+batchAPIKeyRollupQuery)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = conn.ExecContext(ctx, `DEALLOCATE batch_usage; RESET plan_cache_mode`) })
	rows, err := conn.QueryContext(ctx, fmt.Sprintf(`EXPLAIN EXECUTE batch_usage('{7}','%s','%s','%s','%s')`, base.Format(time.RFC3339), base.Add(2*time.Hour).Format(time.RFC3339), base.Format(time.RFC3339), base.Format(time.RFC3339)))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan.String(), "Index Cond:")
	require.Contains(t, plan.String(), "created_at >= b.lo")
}
