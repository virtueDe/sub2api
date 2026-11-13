//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestGroupUsageRollupTriggerInvalidatesCascadedHistoricalDelete(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		name := "ordinary"
		if partitioned {
			name = "partitioned"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			schema := createGroupUsageRollupTriggerTestSchema(t, ctx, partitioned)
			tx := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
			defer func() { _ = tx.Rollback() }()

			_, err := tx.ExecContext(ctx, `
				INSERT INTO groups (id) VALUES (10);
				INSERT INTO users (id) VALUES (1);
				INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at)
				VALUES (1, 1, 10, 1.25, TIMESTAMPTZ '2020-01-02 08:00:00+08');
				UPDATE usage_group_rollup_state
				SET closed_before = DATE '2026-08-14'
				WHERE id = 1;
				DELETE FROM users WHERE id = 1;
			`)
			require.NoError(t, err)

			var closedBefore string
			err = tx.QueryRowContext(ctx, `
				SELECT closed_before::text
				FROM usage_group_rollup_state
				WHERE id = 1
			`).Scan(&closedBefore)
			require.NoError(t, err)
			require.Equal(t, "2026-08-14", closedBefore)
			var events int
			require.NoError(t, tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_group_rollup_events").Scan(&events))
			require.GreaterOrEqual(t, events, 2)
		})
	}
}

// Both historical and current-day writers must commit while the consumer owns
// its advisory lock and the state row. This reproduces the production lock chain.
func TestGroupUsageRollupWritersDoNotWaitForConsumerState(t *testing.T) {
	for _, createdAt := range []string{"2020-01-02 09:00:00+08", "2026-08-14 00:00:01+08"} {
		t.Run(createdAt, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
			seed := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
			_, err := seed.ExecContext(ctx, "INSERT INTO groups VALUES(10); INSERT INTO users VALUES(1)")
			require.NoError(t, err)
			require.NoError(t, seed.Commit())
			consumer := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
			defer func() { _ = consumer.Rollback() }()
			_, err = consumer.ExecContext(ctx, "SELECT pg_advisory_xact_lock(742981313); SELECT id FROM usage_group_rollup_state FOR UPDATE")
			require.NoError(t, err)
			writer := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
			defer func() { _ = writer.Rollback() }()
			_, err = writer.ExecContext(ctx, "SET LOCAL statement_timeout='1s'")
			require.NoError(t, err)
			_, err = writer.ExecContext(ctx, "INSERT INTO usage_logs VALUES(1,1,10,1.25,$1)", createdAt)
			require.NoError(t, err)
			_, err = writer.ExecContext(ctx, "UPDATE usage_logs SET actual_cost=2 WHERE id=1; DELETE FROM usage_logs WHERE id=1")
			require.NoError(t, err)
			require.NoError(t, writer.Commit())
			var events int
			require.NoError(t, consumer.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_group_rollup_events").Scan(&events))
			require.Equal(t, 3, events)
		})
	}
}

func TestGroupUsageRollupTriggerKeepsWatermarkForTodayInsert(t *testing.T) {
	ctx := context.Background()
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)

	tx := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, setGroupUsageRollupTriggerTimeZone(ctx, tx, "Asia/Shanghai"))
	_, err := tx.ExecContext(ctx, `
		INSERT INTO groups (id) VALUES (10);
		INSERT INTO users (id) VALUES (1);
		UPDATE usage_group_rollup_state
		SET closed_before = (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date
		WHERE id = 1;
		INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at)
		VALUES (1, 1, 10, 1.25, CURRENT_TIMESTAMP);
	`)
	require.NoError(t, err)

	var unchanged bool
	err = tx.QueryRowContext(ctx, `
		SELECT closed_before = (CURRENT_TIMESTAMP AT TIME ZONE 'Asia/Shanghai')::date
		FROM usage_group_rollup_state
		WHERE id = 1
	`).Scan(&unchanged)
	require.NoError(t, err)
	require.True(t, unchanged)
}

func TestGroupUsageRollupTriggerUsesSessionTimezoneAcrossDST(t *testing.T) {
	ctx := context.Background()
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)

	tx := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = tx.Rollback() }()
	_, err := tx.ExecContext(ctx, `
		SET LOCAL TIME ZONE 'America/New_York';
		INSERT INTO groups (id) VALUES (10);
		INSERT INTO users (id) VALUES (1);
		UPDATE usage_group_rollup_state
		SET closed_before = DATE '2026-03-09',
			timezone_name = 'America/New_York'
		WHERE id = 1;
		INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at)
		VALUES (1, 1, 10, 1.25, TIMESTAMPTZ '2026-03-08 04:30:00+00');
	`)
	require.NoError(t, err)

	var closedBefore string
	err = tx.QueryRowContext(ctx, `
		SELECT closed_before::text
		FROM usage_group_rollup_state
		WHERE id = 1
	`).Scan(&closedBefore)
	require.NoError(t, err)
	require.Equal(t, "2026-03-09", closedBefore)
	var bucket string
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT (bucket_start AT TIME ZONE 'UTC')::date::text FROM usage_group_rollup_events").Scan(&bucket))
	require.Equal(t, "2026-03-08", bucket)
}

func TestGroupUsageSummaryIncludesYesterdayAcrossWatermark(t *testing.T) {
	ctx := context.Background()
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
	todayStart := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)

	tests := []struct {
		name             string
		closedBefore     string
		includeYesterday bool
	}{
		{name: "closed_rollup", closedBefore: "2026-08-14", includeYesterday: true},
		{name: "raw_tail", closedBefore: "2026-08-13", includeYesterday: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
			tx := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
			defer func() { _ = tx.Rollback() }()

			_, err := tx.ExecContext(ctx, `
				INSERT INTO groups (id) VALUES (10);
				INSERT INTO users (id) VALUES (1);
				INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at) VALUES
					(1, 1, 10, 2, TIMESTAMPTZ '2026-08-12 12:00:00+08'),
					(2, 1, 10, 3, TIMESTAMPTZ '2026-08-13 12:00:00+08'),
					(3, 1, 10, 4, TIMESTAMPTZ '2026-08-14 12:00:00+08');
				INSERT INTO usage_group_daily_rollups (bucket_date, group_id, actual_cost, computed_at)
				VALUES (DATE '2026-08-12', 10, 2, NOW());
			`)
			require.NoError(t, err)
			if tt.includeYesterday {
				_, err = tx.ExecContext(ctx, `
					INSERT INTO usage_group_daily_rollups (bucket_date, group_id, actual_cost, computed_at)
					VALUES (DATE '2026-08-13', 10, 3, NOW())
				`)
				require.NoError(t, err)
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE usage_group_rollup_state
				SET closed_before = $1::date,
					retained_from = TIMESTAMPTZ '2026-08-12 00:00:00+08'
				WHERE id = 1
			`, tt.closedBefore)
			require.NoError(t, err)

			repo := newUsageLogRepositoryWithSQL(nil, tx)
			result, err := repo.GetAllGroupUsageSummary(ctx, todayStart)
			require.NoError(t, err)
			require.Len(t, result, 1)
			require.InDelta(t, 9, result[0].TotalCost, 0.0000001)
			require.InDelta(t, 4, result[0].TodayCost, 0.0000001)
			require.InDelta(t, 3, result[0].YesterdayCost, 0.0000001)
		})
	}
}

func TestGroupUsageRollupSyncRebuildsAfterTimezoneChange(t *testing.T) {
	ctx := context.Background()
	useGroupUsageRepositoryTestTimezone(t, "America/New_York")
	todayStart := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
	tx := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = tx.Rollback() }()

	_, err := tx.ExecContext(ctx, `
		SET LOCAL TIME ZONE 'UTC';
		INSERT INTO groups (id) VALUES (10);
		INSERT INTO users (id) VALUES (1);
		INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at) VALUES
			(1, 1, 10, 3, TIMESTAMPTZ '2026-03-08 05:30:00+00'),
			(2, 1, 10, 5, TIMESTAMPTZ '2026-03-09 04:30:00+00');
		INSERT INTO usage_group_daily_rollups (bucket_date, group_id, actual_cost, computed_at)
		VALUES (DATE '2026-03-08', 10, 99, NOW());
		UPDATE usage_group_rollup_state
		SET closed_before = DATE '2026-03-09',
			retained_from = TIMESTAMPTZ '2026-03-08 05:30:00+00',
			timezone_name = 'Asia/Shanghai'
		WHERE id = 1;
	`)
	require.NoError(t, err)

	repo := newDashboardAggregationRepositoryWithSQL(tx)
	require.NoError(t, repo.SyncGroupUsageRollups(ctx, todayStart))

	var stateTimezone string
	var closedBefore string
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT timezone_name, closed_before::text
		FROM usage_group_rollup_state
		WHERE id = 1
	`).Scan(&stateTimezone, &closedBefore))
	require.Equal(t, "America/New_York", stateTimezone)
	require.Equal(t, "2026-03-09", closedBefore)

	var rollupCost float64
	require.NoError(t, tx.QueryRowContext(ctx, `
		SELECT actual_cost
		FROM usage_group_daily_rollups
		WHERE bucket_date = DATE '2026-03-08' AND group_id = 10
	`).Scan(&rollupCost))
	require.InDelta(t, 3, rollupCost, 0.0000001)

	usageRepo := newUsageLogRepositoryWithSQL(nil, tx)
	result, err := usageRepo.GetAllGroupUsageSummary(ctx, todayStart)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.InDelta(t, 8, result[0].TotalCost, 0.0000001)
	require.InDelta(t, 5, result[0].TodayCost, 0.0000001)
	require.InDelta(t, 3, result[0].YesterdayCost, 0.0000001)
}

func TestGroupUsageSummaryUsesConfiguredDSTBoundaries(t *testing.T) {
	ctx := context.Background()
	useGroupUsageRepositoryTestTimezone(t, "America/New_York")
	todayStart := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	schema := createGroupUsageRollupTriggerTestSchema(t, ctx, false)
	tx := beginGroupUsageRollupTriggerTestTx(t, ctx, schema)
	defer func() { _ = tx.Rollback() }()

	_, err := tx.ExecContext(ctx, `
		SET LOCAL TIME ZONE 'UTC';
		INSERT INTO groups (id) VALUES (10);
		INSERT INTO users (id) VALUES (1);
		INSERT INTO usage_logs (id, user_id, group_id, actual_cost, created_at) VALUES
			(1, 1, 10, 100, TIMESTAMPTZ '2026-03-08 04:30:00+00'),
			(2, 1, 10, 3, TIMESTAMPTZ '2026-03-08 05:30:00+00'),
			(3, 1, 10, 4, TIMESTAMPTZ '2026-03-09 03:30:00+00'),
			(4, 1, 10, 5, TIMESTAMPTZ '2026-03-09 04:30:00+00');
		UPDATE usage_group_rollup_state
		SET closed_before = DATE '1970-01-01',
			retained_from = TIMESTAMPTZ '1970-01-01 00:00:00+00',
			timezone_name = 'America/New_York'
		WHERE id = 1;
	`)
	require.NoError(t, err)

	repo := newUsageLogRepositoryWithSQL(nil, tx)
	result, err := repo.GetAllGroupUsageSummary(ctx, todayStart)
	require.NoError(t, err)
	require.Len(t, result, 1)
	require.InDelta(t, 112, result[0].TotalCost, 0.0000001)
	require.InDelta(t, 5, result[0].TodayCost, 0.0000001)
	require.InDelta(t, 7, result[0].YesterdayCost, 0.0000001)
}

func createGroupUsageRollupTriggerTestSchema(t *testing.T, ctx context.Context, partitioned bool) string {
	t.Helper()

	schema := fmt.Sprintf("group_usage_rollup_trigger_%d", time.Now().UnixNano())
	quotedSchema := pq.QuoteIdentifier(schema)
	_, err := integrationDB.ExecContext(ctx, "CREATE SCHEMA "+quotedSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE")
	})

	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	require.NoError(t, setGroupUsageRollupTriggerSearchPath(ctx, tx, quotedSchema))

	usageLogsDDL := `
		CREATE TABLE usage_logs (
			id BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
			actual_cost NUMERIC(20, 10) NOT NULL,
			created_at TIMESTAMPTZ NOT NULL
		);
	`
	if partitioned {
		usageLogsDDL = `
			CREATE TABLE usage_logs (
				id BIGINT NOT NULL,
				user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
				group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
				actual_cost NUMERIC(20, 10) NOT NULL,
				created_at TIMESTAMPTZ NOT NULL
			) PARTITION BY RANGE (created_at);
			CREATE TABLE usage_logs_default PARTITION OF usage_logs DEFAULT;
		`
	}

	_, err = tx.ExecContext(ctx, `
		CREATE TABLE users (id BIGINT PRIMARY KEY);
		CREATE TABLE groups (id BIGINT PRIMARY KEY);
	`+usageLogsDDL)
	require.NoError(t, err)

	for _, migrationName := range []string{
		"222_group_usage_daily_rollups.sql",
		"223_group_usage_rollup_timezone.sql",
	} {
		migrationSQL, readErr := migrations.FS.ReadFile(migrationName)
		require.NoError(t, readErr)
		for range 2 {
			_, err = tx.ExecContext(ctx, string(migrationSQL))
			require.NoError(t, err)
		}
	}
	migrationSQL, readErr := migrations.FS.ReadFile("255_group_usage_rollup_dirty_events.sql")
	require.NoError(t, readErr)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, "CREATE INDEX ON usage_logs(created_at)")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	return schema
}

func beginGroupUsageRollupTriggerTestTx(t *testing.T, ctx context.Context, schema string) *sql.Tx {
	t.Helper()

	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, setGroupUsageRollupTriggerSearchPath(ctx, tx, pq.QuoteIdentifier(schema)))
	return tx
}

func setGroupUsageRollupTriggerSearchPath(ctx context.Context, tx *sql.Tx, quotedSchema string) error {
	_, err := tx.ExecContext(ctx, "SET LOCAL search_path TO "+quotedSchema)
	return err
}

func setGroupUsageRollupTriggerTimeZone(ctx context.Context, tx *sql.Tx, name string) error {
	_, err := tx.ExecContext(ctx, "SET LOCAL TIME ZONE "+pq.QuoteLiteral(name))
	return err
}
