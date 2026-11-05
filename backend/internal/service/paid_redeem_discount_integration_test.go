//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Always uses a disposable local container, never a supplied database DSN.
func TestPaidRedeemDiscountPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	pg, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("discount_test"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("test-only"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	base, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = base.Close() })
	setup := func(t *testing.T) (*sql.DB, *PaidRedeemDiscountService, *PaidRedeemDiscountPreview) {
		t.Helper()
		schema := fmt.Sprintf("discount_%d", time.Now().UnixNano())
		_, err := base.ExecContext(ctx, "CREATE SCHEMA "+schema)
		require.NoError(t, err)
		scopedDSN, err := pg.ConnectionString(ctx, "sslmode=disable", "search_path="+schema)
		require.NoError(t, err)
		db, err := sql.Open("postgres", scopedDSN)
		require.NoError(t, err)
		client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.Postgres, db))))
		t.Cleanup(func() { _ = client.Close() })
		_, err = db.ExecContext(ctx, `CREATE TABLE user_group_rate_multipliers (
			user_id BIGINT REFERENCES users(id), group_id BIGINT REFERENCES groups(id),
			rate_multiplier DECIMAL(10,4), rpm_override INT, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ,
			PRIMARY KEY(user_id,group_id));
			CREATE TABLE auth_cache_invalidation_outbox (id BIGSERIAL PRIMARY KEY, cache_key TEXT NOT NULL);`)
		require.NoError(t, err)
		target := client.Group.Create().SetName("target").SetAllowImageGeneration(true).SaveX(ctx)
		source := client.Group.Create().SetName("source").SaveX(ctx)
		u := client.User.Create().SetEmail("paid@example.com").SetPasswordHash("test").SaveX(ctx)
		client.UserAllowedGroup.Create().SetUserID(u.ID).SetGroupID(source.ID).SaveX(ctx)
		client.APIKey.Create().SetUserID(u.ID).SetName("key").SetKey("test-key").SaveX(ctx)
		svc := NewPaidRedeemDiscountService(client, nil)
		_, err = svc.UpdateConfig(ctx, PaidRedeemDiscountConfig{Enabled: true, GroupID: target.ID, RateMultiplier: 0.01, SourceGroupIDs: []int64{source.ID}})
		require.NoError(t, err)
		preview, err := svc.Preview(ctx)
		require.NoError(t, err)
		return db, svc, preview
	}

	for _, mutation := range []string{
		`UPDATE users SET role='admin'`,
		`UPDATE users SET deleted_at=NOW()`,
		`DELETE FROM user_allowed_groups`,
		`UPDATE groups SET rate_multiplier=0.5 WHERE name='target'`,
		`UPDATE groups SET deleted_at=NOW() WHERE name='source'`,
		`UPDATE settings SET value=jsonb_set(value::jsonb,'{rate_multiplier}','0.02')::text WHERE key='paid_redeem_discount'`,
	} {
		t.Run(mutation, func(t *testing.T) {
			db, svc, preview := setup(t)
			// Hold the first INSERT after eligibility checking so competing writes
			// can be observed waiting on the locks held by the actual Backfill.
			_, err := db.ExecContext(ctx, `CREATE FUNCTION hold_discount_grant() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN PERFORM pg_advisory_xact_lock(321005); RETURN NEW; END $$;
				CREATE TRIGGER hold_discount BEFORE INSERT ON user_group_rate_multipliers FOR EACH ROW EXECUTE FUNCTION hold_discount_grant();`)
			require.NoError(t, err)
			gate, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = gate.Rollback() }()
			_, err = gate.ExecContext(ctx, `SELECT pg_advisory_xact_lock(321005)`)
			require.NoError(t, err)
			applied := make(chan error, 1)
			go func() { _, err := svc.Backfill(ctx, preview.PreviewToken); applied <- err }()
			require.Eventually(t, func() bool {
				var waiting bool
				err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE 'INSERT INTO user_group_rate_multipliers%')`).Scan(&waiting)
				return err == nil && waiting
			}, 5*time.Second, 10*time.Millisecond)
			writer, err := db.Conn(ctx)
			require.NoError(t, err)
			defer func() { _ = writer.Close() }()
			var pid int
			require.NoError(t, writer.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			changed := make(chan error, 1)
			go func() { _, err := writer.ExecContext(ctx, mutation); changed <- err }()
			require.Eventually(t, func() bool {
				var waiting bool
				err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, pid).Scan(&waiting)
				return err == nil && waiting
			}, 5*time.Second, 10*time.Millisecond)
			require.NoError(t, gate.Commit())
			select {
			case err := <-applied:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-changed:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var rate float64
			var events int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT rate_multiplier FROM user_group_rate_multipliers`).Scan(&rate))
			require.Equal(t, 0.01, rate)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM auth_cache_invalidation_outbox`).Scan(&events))
			require.Equal(t, 1, events)
		})
	}

	t.Run("eligibility_changed_before_lock_acquired", func(t *testing.T) {
		db, svc, preview := setup(t)
		writer, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = writer.Rollback() }()
		_, err = writer.ExecContext(ctx, `UPDATE users SET role='admin'`)
		require.NoError(t, err)
		applied := make(chan error, 1)
		go func() { _, err := svc.Backfill(ctx, preview.PreviewToken); applied <- err }()
		require.Eventually(t, func() bool {
			var waiting bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE%')`).Scan(&waiting)
			return err == nil && waiting
		}, 5*time.Second, 10*time.Millisecond)
		require.NoError(t, writer.Commit())
		select {
		case err := <-applied:
			require.ErrorContains(t, err, "refresh the preview")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		var count int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM user_group_rate_multipliers`).Scan(&count))
		require.Zero(t, count)
	})
}
