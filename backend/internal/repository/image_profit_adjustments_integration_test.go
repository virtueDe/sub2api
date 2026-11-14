//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestImageProfitMigrationBoundaryFollowsTriggerBarrier(t *testing.T) {
	ctx := context.Background()
	schema := fmt.Sprintf("profit_migration_%d", time.Now().UnixNano())
	_, err := integrationDB.Exec("CREATE SCHEMA " + schema)
	require.NoError(t, err)
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	writer, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, "RESET search_path")
		_, _ = writer.ExecContext(ctx, "RESET search_path")
		_ = conn.Close()
		_ = writer.Close()
		_, cleanupErr := integrationDB.Exec("DROP SCHEMA " + schema + " CASCADE")
		require.NoError(t, cleanupErr)
	})
	_, err = conn.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = writer.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `CREATE TABLE users(id bigint PRIMARY KEY,role text); INSERT INTO users VALUES(1,'admin');
CREATE TABLE usage_logs(id bigserial,created_at timestamptz DEFAULT clock_timestamp(),user_id bigint DEFAULT 1,billing_type integer DEFAULT 0,video_count integer DEFAULT 0,image_count integer DEFAULT 1,image_output_tokens integer DEFAULT 0,billing_mode text DEFAULT 'image') PARTITION BY RANGE(created_at);
CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT;`)
	require.NoError(t, err)
	raw, err := migrations.FS.ReadFile("256_image_profit_adjustments.sql")
	require.NoError(t, err)
	const marker = "CREATE TRIGGER usage_logs_image_profit_admin_exclusion"
	prefix, suffix, ok := strings.Cut(string(raw), marker)
	require.True(t, ok)
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, prefix)
	require.NoError(t, err)
	// A writer may commit after the policy table is created but before the
	// trigger locks exist. Its timestamp must remain BEFORE the final boundary.
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_logs DEFAULT VALUES`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, marker+suffix)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var beforeBoundary bool
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT created_at < (SELECT exclude_admin_from FROM image_profit_accounting_policy) FROM usage_logs WHERE id=1`).Scan(&beforeBoundary))
	require.True(t, beforeBoundary)
	_, err = writer.ExecContext(ctx, `INSERT INTO usage_logs_default DEFAULT VALUES`)
	require.NoError(t, err)
	var excludedID int64
	require.NoError(t, writer.QueryRowContext(ctx, `SELECT usage_log_id FROM image_profit_excluded_usage`).Scan(&excludedID))
	require.EqualValues(t, 2, excludedID)
}

func TestImageProfitAdjustmentsRecalculateWithoutChangingCharges(t *testing.T) {
	ctx := context.Background()
	_, snapshot := settlementFixture(t)
	u := snapshot.Usage
	day := time.Date(2037, 11, 9, 0, 0, 0, 0, service.ImageProfitLocation)
	reason := "profit-adjustments-" + uuid.NewString()
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM image_profit_reports WHERE reason=$1`,
			`DELETE FROM image_profit_usage_adjustments WHERE batch_key=$1`,
			`DELETE FROM image_procurement_prices WHERE note=$1`,
		} {
			_, err := integrationDB.Exec(q, reason)
			require.NoError(t, err)
		}
	})
	insert := func(count int, amount string, at time.Time) int64 {
		t.Helper()
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,image_count,image_size,billing_mode,actual_cost,total_cost,created_at)
VALUES($1,$2,$3,$4,'profit-test-model',$5,CASE WHEN $5>0 THEN '1K' ELSE NULL END,'image',$6,$6,$7) RETURNING id`, u.UserID, u.APIKeyID, u.AccountID, uuid.NewString(), count, amount, at).Scan(&id))
		return id
	}
	// An earlier source establishes retention coverage for the test day.
	insert(1, "1", day.Add(-time.Hour))
	insert(2, "1", day.Add(time.Hour))
	lossID := insert(2, "0", day.Add(2*time.Hour))
	countID := insert(0, "0.0504", day.Add(3*time.Hour))
	for _, size := range []string{"1K", "2K", "4K"} {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO image_procurement_prices(scope_type,scope_id,model,resolution,unit_price_usd,effective_at,note,created_by) VALUES('account',$1,'profit-test-model',$2,0.04,$3,$4,$5)`, u.AccountID, size, day, reason, u.UserID)
		require.NoError(t, err)
	}
	r := &imageProfitRepository{db: integrationDB}
	before, err := r.Settle(ctx, day, reason, u.UserID, true)
	require.NoError(t, err)
	require.Equal(t, 2, before.MissingCount)
	for i := 0; i < 2; i++ {
		// Replaying the same batch cannot apply either decision twice.
		_, err = integrationDB.ExecContext(ctx, `INSERT INTO image_profit_usage_adjustments(usage_log_id,confirmed_loss,image_count_override,original_snapshot,reason,batch_key,created_by)
SELECT id,id=$1,CASE WHEN id=$2 THEN 1 ELSE NULL END,jsonb_build_object('image_count',image_count,'actual_cost',actual_cost),$3,$3,$4 FROM usage_logs WHERE id IN ($1,$2) ON CONFLICT (usage_log_id) DO NOTHING`, lossID, countID, reason, u.UserID)
		require.NoError(t, err)
	}
	after, err := r.Settle(ctx, day, reason, u.UserID, true)
	require.NoError(t, err)
	require.Equal(t, "complete", after.Status)
	require.Equal(t, before.Revision+1, after.Revision)
	require.Equal(t, before.SourceIDs, after.SourceIDs)
	require.Equal(t, 3, after.RequestCount)
	require.Equal(t, 5, after.ImageCount)
	require.Equal(t, 1, after.CountCorrections)
	require.Equal(t, 1, after.LossRequests)
	require.Equal(t, "0.08", after.AbnormalLoss.String())
	require.Equal(t, "0.2", after.KnownCost.String())
	require.Equal(t, "1.0504", after.Revenue.String())
	require.Equal(t, "0.8504", after.Profit.String())
	old, err := r.GetReport(ctx, before.ID)
	require.NoError(t, err)
	require.Equal(t, 2, old.MissingCount)
	require.Nil(t, old.Profit)
	var rawCount int
	var charge string
	require.NoError(t, integrationDB.QueryRow(`SELECT image_count,actual_cost::text FROM usage_logs WHERE id=$1`, countID).Scan(&rawCount, &charge))
	require.Zero(t, rawCount)
	require.Equal(t, "0.0504000000", charge)
	checkImageWallet(t, u.UserID, 100, 0)
	_, err = integrationDB.Exec(`DELETE FROM usage_logs WHERE id=$1`, lossID)
	require.NoError(t, err)
	_, err = r.Settle(ctx, day, reason, u.UserID, true)
	require.ErrorContains(t, err, "previous report was preserved")
	var decisions int
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM image_profit_usage_adjustments WHERE batch_key=$1`, reason).Scan(&decisions))
	require.Equal(t, 2, decisions, "audit records survive source retention")
}

func TestImageProfitAdminExclusionSnapshotsFutureRole(t *testing.T) {
	ctx := context.Background()
	_, snapshot := settlementFixture(t)
	u := snapshot.Usage
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var cutoff time.Time
	require.NoError(t, tx.QueryRow(`SELECT exclude_admin_from FROM image_profit_accounting_policy WHERE id`).Scan(&cutoff))
	_, err = tx.Exec(`UPDATE users SET role='admin' WHERE id=$1`, u.UserID)
	require.NoError(t, err)
	// Single INSERT with multiple rows covers bulk writes and late old imports.
	_, err = tx.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,image_count,image_size,billing_mode,created_at)
SELECT $1,$2,$3,$4 || n,'profit-admin-test',1,'1K','image',$5::timestamptz + n * interval '1 second' FROM generate_series(-1,1) n`, u.UserID, u.APIKeyID, u.AccountID, uuid.NewString(), cutoff)
	require.NoError(t, err)
	var excluded int
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM image_profit_excluded_usage WHERE user_id=$1`, u.UserID).Scan(&excluded))
	require.Equal(t, 2, excluded, "cutoff is inclusive; old import remains included")
	_, err = tx.Exec(`UPDATE users SET role='user' WHERE id=$1`, u.UserID)
	require.NoError(t, err)
	var regularID int64
	require.NoError(t, tx.QueryRow(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,image_count,image_size,billing_mode,created_at)
VALUES($1,$2,$3,$4,'profit-admin-test',1,'1K','image',$5) RETURNING id`, u.UserID, u.APIKeyID, u.AccountID, uuid.NewString(), cutoff.Add(time.Minute)).Scan(&regularID))
	_, err = tx.Exec(`UPDATE users SET role='admin' WHERE id=$1`, u.UserID)
	require.NoError(t, err)
	var hasExclusion bool
	require.NoError(t, tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM image_profit_excluded_usage WHERE usage_log_id=$1)`, regularID).Scan(&hasExclusion))
	require.False(t, hasExclusion, "promotion cannot exclude earlier regular-user calls")
	require.NoError(t, tx.QueryRow(`SELECT count(*) FROM image_profit_excluded_usage WHERE user_id=$1`, u.UserID).Scan(&excluded))
	require.Equal(t, 2, excluded, "role changes cannot erase existing exclusion snapshots")

	// Verify trigger inheritance on an existing leaf and one created later,
	// without altering the suite's real usage table or its partition layout.
	_, err = tx.Exec(`CREATE TEMP TABLE profit_partition_test (LIKE usage_logs INCLUDING DEFAULTS INCLUDING CONSTRAINTS) PARTITION BY RANGE(created_at);
CREATE TEMP TABLE profit_partition_existing PARTITION OF profit_partition_test FOR VALUES FROM ('2038-01-01') TO ('2038-02-01');
CREATE TRIGGER profit_test_capture AFTER INSERT ON profit_partition_test FOR EACH ROW EXECUTE FUNCTION capture_image_profit_admin_exclusion();
CREATE TEMP TABLE profit_partition_new PARTITION OF profit_partition_test FOR VALUES FROM ('2038-02-01') TO ('2038-03-01');`)
	require.NoError(t, err)
	for index, leaf := range []string{"profit_partition_existing", "profit_partition_new"} {
		var id int64
		q := fmt.Sprintf(`INSERT INTO %s(user_id,api_key_id,account_id,request_id,model,image_count,image_size,billing_mode,created_at) VALUES($1,$2,$3,$4,'profit-admin-test',1,'1K','image',$5) RETURNING id`, leaf)
		require.NoError(t, tx.QueryRow(q, u.UserID, u.APIKeyID, u.AccountID, uuid.NewString(), time.Date(2038, time.Month(index+1), 2, 0, 0, 0, 0, time.UTC)).Scan(&id))
		require.NoError(t, tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM image_profit_excluded_usage WHERE usage_log_id=$1)`, id).Scan(&hasExclusion))
		require.True(t, hasExclusion, leaf)
	}
}

func TestImageProfitExcludedAdminStillProtectsSourceCompleteness(t *testing.T) {
	ctx := context.Background()
	_, snapshot := settlementFixture(t)
	u := snapshot.Usage
	day := time.Date(2037, 11, 10, 0, 0, 0, 0, service.ImageProfitLocation)
	reason := "profit-admin-report-" + uuid.NewString()
	t.Cleanup(func() {
		_, err := integrationDB.Exec(`DELETE FROM image_profit_reports WHERE reason=$1`, reason)
		require.NoError(t, err)
		_, err = integrationDB.Exec(`DELETE FROM image_profit_excluded_usage WHERE user_id=$1`, u.UserID)
		require.NoError(t, err)
	})
	_, err := integrationDB.Exec(`UPDATE users SET role='admin' WHERE id=$1`, u.UserID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`INSERT INTO usage_logs(user_id,api_key_id,account_id,request_id,model,image_count,image_size,billing_mode,actual_cost,created_at)
SELECT $1,$2,$3,$4||n,'profit-admin-report',1,'1K','image',0.5,$5::timestamptz + n * interval '1 hour' FROM generate_series(-1,1) n`, u.UserID, u.APIKeyID, u.AccountID, uuid.NewString(), day)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE users SET role='user' WHERE id=$1`, u.UserID)
	require.NoError(t, err)
	r := &imageProfitRepository{db: integrationDB}
	report, err := r.Settle(ctx, day, reason, u.UserID, true)
	require.NoError(t, err)
	require.Equal(t, "complete", report.Status)
	require.Equal(t, 2, report.SourceCount)
	require.Len(t, report.SourceIDs, 2)
	require.Equal(t, 2, report.ExcludedAdminRequests)
	require.Zero(t, report.RequestCount)
	require.True(t, report.Profit.IsZero())
	report2, err := r.Settle(ctx, day, reason, u.UserID, true)
	require.NoError(t, err)
	require.Equal(t, report.SourceIDs, report2.SourceIDs)
	_, err = integrationDB.Exec(`DELETE FROM usage_logs WHERE id=$1`, report.SourceIDs[0])
	require.NoError(t, err)
	_, err = r.Settle(ctx, day, reason, u.UserID, true)
	require.ErrorContains(t, err, "previous report was preserved")
}
