//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// All data and schema mutations in this suite are confined to its disposable
// container. No developer or production database connection is accepted.
func TestImagePayloadPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	image := os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")
	if image == "" {
		image = "postgres:18.1-alpine3.23"
	}
	pg, err := tcpostgres.Run(ctx, image, tcpostgres.WithDatabase("image_payload_test"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("test-only"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(context.Background())) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	base, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, base.Close()) })

	setup := func(t *testing.T) (*sql.DB, *ImagePayloadCleanupService, time.Time) {
		t.Helper()
		schema := fmt.Sprintf("payload_%d", time.Now().UnixNano())
		_, err := base.ExecContext(ctx, "CREATE SCHEMA "+schema)
		require.NoError(t, err)
		scopedDSN, err := pg.ConnectionString(ctx, "sslmode=disable", "search_path="+schema)
		require.NoError(t, err)
		db, err := sql.Open("postgres", scopedDSN)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, db.Close())
			_, err := base.Exec("DROP SCHEMA " + schema + " CASCADE")
			require.NoError(t, err)
		})
		_, err = db.ExecContext(ctx, `CREATE TABLE settings (id BIGSERIAL PRIMARY KEY,key VARCHAR(100) UNIQUE NOT NULL,value TEXT NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
   CREATE TABLE usage_logs (id BIGSERIAL PRIMARY KEY,created_at TIMESTAMPTZ NOT NULL,user_id BIGINT NOT NULL DEFAULT 42,actual_cost NUMERIC NOT NULL DEFAULT 1.23456789,image_generation JSONB);
   CREATE TABLE ops_error_logs (LIKE usage_logs INCLUDING ALL);`)
		require.NoError(t, err)
		svc, err := NewImagePayloadCleanupService(&imagePayloadSQLSettings{db: db}, db, &config.Config{Timezone: "Asia/Shanghai"})
		require.NoError(t, err)
		t.Cleanup(svc.Stop)
		_, err = svc.UpdateSettings(ctx, ImagePayloadCleanupSettings{Enabled: true, RetainDays: 3})
		require.NoError(t, err)
		return db, svc, time.Date(2026, 10, 28, 2, 0, 0, 0, svc.location)
	}
	fixture := func(t *testing.T, db *sql.DB, table string, id int, created time.Time, output string) {
		t.Helper()
		detail := `{"prompt":"cat","request_params":{"image":["data:image/png;base64,aGVsbG8="],"prompt":"cat","seed":9007199254740993,"n":2},"images":[{"b64_json":` + fmt.Sprintf("%q", output) + `,"url":"https://example.test/image?signature=keep","mime_type":"image/png","revised_prompt":"a cat","partial":true,"future":7}],"future":{"number":9007199254740993}}`
		_, err := db.ExecContext(ctx, "INSERT INTO "+table+" (id,created_at,image_generation) VALUES ($1,$2,$3::jsonb)", id, created, detail)
		require.NoError(t, err)
	}
	hasOutput := func(t *testing.T, db *sql.DB, table string, id int) bool {
		t.Helper()
		var exists bool
		require.NoError(t, db.QueryRowContext(ctx, "SELECT image_generation->'images'->0 ? 'b64_json' FROM "+table+" WHERE id=$1", id).Scan(&exists))
		return exists
	}

	t.Run("retention_multiple_batches_and_idempotency", func(t *testing.T) {
		db, svc, now := setup(t)
		cutoff := imagePayloadCutoff(now, 3, svc.location)
		for _, table := range []string{"usage_logs", "ops_error_logs"} {
			for id := 1; id <= 45; id++ {
				created := cutoff.Add(-time.Second)
				if id == 44 {
					created = cutoff
				}
				if id == 45 {
					created = cutoff.Add(time.Hour)
				}
				output := "output"
				if id == 1 {
					output = strings.Repeat("YQ==", 262144)
				}
				fixture(t, db, table, id, created, output)
			}
		}
		require.NoError(t, svc.run(ctx, now))
		for _, table := range []string{"usage_logs", "ops_error_logs"} {
			var preserved, inputs, expired, recent int
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+` WHERE user_id=42 AND actual_cost=1.23456789 AND image_generation->>'prompt'='cat' AND image_generation->'request_params'->>'seed'='9007199254740993' AND image_generation->'request_params'->>'n'='2' AND image_generation->'future'->>'number'='9007199254740993' AND image_generation->'images'->0->>'url'='https://example.test/image?signature=keep' AND image_generation->'images'->0->>'revised_prompt'='a cat' AND image_generation->'images'->0->>'partial'='true' AND image_generation->'images'->0->>'future'='7'`).Scan(&preserved))
			require.Equal(t, 45, preserved)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+` WHERE image_generation->'request_params'->'image'->>0='[IMAGE_DATA_OMITTED]'`).Scan(&inputs))
			require.Equal(t, 45, inputs)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+` WHERE created_at<$1 AND NOT (image_generation->'images'->0 ? 'b64_json')`, cutoff).Scan(&expired))
			require.Equal(t, 43, expired)
			require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM "+table+` WHERE created_at>=$1 AND image_generation->'images'->0->>'b64_json'='output'`, cutoff).Scan(&recent))
			require.Equal(t, 2, recent)
		}
		var xmin string
		require.NoError(t, db.QueryRowContext(ctx, "SELECT xmin::text FROM usage_logs WHERE id=1").Scan(&xmin))
		require.NoError(t, svc.run(ctx, now))
		var unchanged string
		require.NoError(t, db.QueryRowContext(ctx, "SELECT xmin::text FROM usage_logs WHERE id=1").Scan(&unchanged))
		require.Equal(t, xmin, unchanged)
	})

	t.Run("row_lock_skips_and_rescans_without_overwrite", func(t *testing.T) {
		db, svc, now := setup(t)
		old := now.AddDate(0, 0, -5)
		fixture(t, db, "usage_logs", 1, old, "output")
		fixture(t, db, "usage_logs", 2, old, "output")
		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.ExecContext(ctx, `UPDATE usage_logs SET actual_cost=9.87,image_generation=jsonb_set(image_generation,'{prompt}','"edited"') WHERE id=1`)
		require.NoError(t, err)
		require.NoError(t, svc.run(ctx, now))
		require.True(t, hasOutput(t, db, "usage_logs", 1))
		require.False(t, hasOutput(t, db, "usage_logs", 2))
		require.NoError(t, tx.Commit())
		require.NoError(t, svc.run(ctx, now))
		require.False(t, hasOutput(t, db, "usage_logs", 1))
		var prompt, cost string
		require.NoError(t, db.QueryRowContext(ctx, `SELECT image_generation->>'prompt',actual_cost::text FROM usage_logs WHERE id=1`).Scan(&prompt, &cost))
		require.Equal(t, "edited", prompt)
		require.Equal(t, "9.87", cost)
	})

	t.Run("one_available_connection_and_singleton", func(t *testing.T) {
		db, svc, now := setup(t)
		fixture(t, db, "usage_logs", 1, now.AddDate(0, 0, -5), "output")
		held, err := db.Conn(ctx)
		require.NoError(t, err)
		defer func() { _ = held.Close() }()
		// Simulate another job holding a session for its own advisory lock.
		_, err = held.ExecContext(ctx, "SELECT pg_advisory_lock(12345)")
		require.NoError(t, err)
		db.SetMaxOpenConns(2)
		deadline, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		require.NoError(t, svc.run(deadline, now))
		require.False(t, hasOutput(t, db, "usage_logs", 1))
		_, err = held.ExecContext(ctx, "SELECT pg_advisory_unlock(12345)")
		require.NoError(t, err)
		_, err = held.ExecContext(ctx, "SELECT pg_advisory_lock($1)", hashAdvisoryLockID("image_payload_cleanup"))
		require.NoError(t, err)
		fixture(t, db, "usage_logs", 2, now.AddDate(0, 0, -5), "output")
		require.NoError(t, svc.run(deadline, now))
		require.True(t, hasOutput(t, db, "usage_logs", 2))
		_, err = held.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", hashAdvisoryLockID("image_payload_cleanup"))
		require.NoError(t, err)
		require.NoError(t, held.Close())
		db.SetMaxOpenConns(1)
		require.NoError(t, svc.run(deadline, now))
		require.False(t, hasOutput(t, db, "usage_logs", 2))
	})

	t.Run("failed_row_rolls_back_and_checkpoint_advances", func(t *testing.T) {
		db, svc, now := setup(t)
		for _, table := range []string{"usage_logs", "ops_error_logs"} {
			for id := 1; id <= 2; id++ {
				fixture(t, db, table, id, now.AddDate(0, 0, -5), "output")
			}
		}
		_, err := db.ExecContext(ctx, `CREATE FUNCTION reject_image_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id=1 THEN RAISE EXCEPTION 'test cleanup failure'; END IF; RETURN NEW; END $$;
   CREATE TRIGGER reject_cleanup BEFORE UPDATE ON usage_logs FOR EACH ROW EXECUTE FUNCTION reject_image_cleanup();`)
		require.NoError(t, err)
		require.ErrorContains(t, svc.run(ctx, now), "test cleanup failure")
		require.True(t, hasOutput(t, db, "usage_logs", 1))
		require.False(t, hasOutput(t, db, "ops_error_logs", 1))
		var state string
		require.NoError(t, db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", imagePayloadSettingsKey+"_progress_usage_logs").Scan(&state))
		require.JSONEq(t, `{"after_id":1,"upper_id":2}`, state)
		require.NoError(t, svc.run(ctx, now))
		require.False(t, hasOutput(t, db, "usage_logs", 2))
		_, err = db.ExecContext(ctx, "DROP TRIGGER reject_cleanup ON usage_logs")
		require.NoError(t, err)
		require.NoError(t, svc.run(ctx, now))
		require.False(t, hasOutput(t, db, "usage_logs", 1))
	})

	t.Run("disable_at_batch_boundary", func(t *testing.T) {
		db, svc, now := setup(t)
		for id := 1; id <= imagePayloadBatchSize+1; id++ {
			fixture(t, db, "usage_logs", id, now.AddDate(0, 0, -5), "output")
		}
		_, err := db.ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION disable_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.id=%d THEN UPDATE settings SET value='{"enabled":false,"retain_days":3}' WHERE key='image_payload_cleanup'; END IF; RETURN NEW; END $$;
   CREATE TRIGGER disable_cleanup BEFORE UPDATE ON usage_logs FOR EACH ROW EXECUTE FUNCTION disable_cleanup();`, imagePayloadBatchSize))
		require.NoError(t, err)
		require.NoError(t, svc.run(ctx, now))
		require.False(t, hasOutput(t, db, "usage_logs", imagePayloadBatchSize))
		require.True(t, hasOutput(t, db, "usage_logs", imagePayloadBatchSize+1))
	})

	t.Run("cancellation_rolls_back_and_releases_lock", func(t *testing.T) {
		db, svc, now := setup(t)
		fixture(t, db, "usage_logs", 1, now.AddDate(0, 0, -5), "output")
		_, err := db.ExecContext(ctx, `CREATE FUNCTION pause_cleanup() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(10); RETURN NEW; END $$;
   CREATE TRIGGER pause_cleanup BEFORE UPDATE ON usage_logs FOR EACH ROW EXECUTE FUNCTION pause_cleanup();`)
		require.NoError(t, err)
		runCtx, stop := context.WithCancel(ctx)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- svc.run(runCtx, now) }()
		require.Eventually(t, func() bool {
			var waiting bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='PgSleep' AND query LIKE 'UPDATE usage_logs SET image_generation%')`).Scan(&waiting)
			return err == nil && waiting
		}, 5*time.Second, 20*time.Millisecond)
		stop()
		select {
		case err := <-done:
			require.Error(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup did not cancel")
		}
		require.True(t, hasOutput(t, db, "usage_logs", 1))
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()
		var locked bool
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", hashAdvisoryLockID("image_payload_cleanup")).Scan(&locked))
		require.True(t, locked)
		_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", hashAdvisoryLockID("image_payload_cleanup"))
		require.NoError(t, err)
	})
}

type imagePayloadSQLSettings struct {
	SettingRepository
	db *sql.DB
}

func (s *imagePayloadSQLSettings) GetValue(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=$1", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", ErrSettingNotFound
	}
	return value, err
}
func (s *imagePayloadSQLSettings) Set(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=NOW()`, key, value)
	return err
}
