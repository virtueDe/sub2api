package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagepayload"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const imagePayloadBatchSize = 20

type imagePayloadProgress struct {
	AfterID int64 `json:"after_id"`
	UpperID int64 `json:"upper_id"`
}

func (s *ImagePayloadCleanupService) run(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 31*time.Minute)
	defer cancel()
	cfg, err := s.GetSettings(ctx)
	if err != nil || !cfg.Enabled {
		return err
	}
	if s.db == nil {
		return errors.New("image payload cleanup database unavailable")
	}

	var failures []error
	for _, table := range []string{"usage_logs", "ops_error_logs"} {
		tableCtx, stop := context.WithTimeout(ctx, 15*time.Minute)
		err := s.runTable(tableCtx, table, now)
		stop()
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", table, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(failures...)
}

func (s *ImagePayloadCleanupService) runTable(ctx context.Context, table string, now time.Time) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	lockID := hashAdvisoryLockID("image_payload_cleanup")
	var locked bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", lockID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", lockID); err != nil {
			// Discard sessions whose advisory lock could not be released.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	// Work stays on the lock-owning session. Each table gets a fresh session,
	// so a timed-out query cannot poison the other table's execution budget.
	return s.cleanTable(ctx, conn, table, now)
}

func (s *ImagePayloadCleanupService) cleanTable(ctx context.Context, conn *sql.Conn, table string, now time.Time) error {
	// Only these identifiers can enter SQL; never accept a request-provided name.
	if table != "usage_logs" && table != "ops_error_logs" {
		return errors.New("invalid image payload table")
	}
	key := imagePayloadSettingsKey + "_progress_" + table
	var progress imagePayloadProgress
	raw, err := imagePayloadSetting(ctx, conn, key)
	if err != nil {
		return err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &progress); err != nil {
			return fmt.Errorf("decode cleanup progress: %w", err)
		}
	}
	if progress.AfterID < 0 || progress.UpperID < progress.AfterID {
		return errors.New("invalid image payload cleanup progress")
	}
	if progress.UpperID == 0 {
		if err := conn.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM "+table).Scan(&progress.UpperID); err != nil {
			return err
		}
	}
	var scanned, updated, removed, failed int64
	var firstError error
	defer func() {
		logger.LegacyPrintf("service.image_payload_cleanup", "table=%s scanned=%d updated=%d failed=%d content_bytes_removed=%d cursor=%d upper=%d", table, scanned, updated, failed, removed, progress.AfterID, progress.UpperID)
	}()
	for {
		settings, err := imagePayloadSetting(ctx, conn, imagePayloadSettingsKey)
		if err != nil {
			return err
		}
		cfg, err := parseImagePayloadSettings(settings, s.location)
		if err != nil || !cfg.Enabled {
			return err
		}
		cutoff := imagePayloadCutoff(now, cfg.RetainDays, s.location)
		batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		ids, err := imagePayloadIDs(batchCtx, conn, table, progress)
		cancel()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			// A new complete sweep on the next day catches newly expired outputs and
			// rows skipped because a live request held their lock.
			if err := saveImagePayloadProgress(ctx, conn, key, `{"after_id":0,"upper_id":0}`); err != nil {
				return err
			}
			return firstError
		}
		workCtx, stopBatch := context.WithTimeout(ctx, 30*time.Second)
		var batchErr error
		for _, id := range ids {
			changed, bytesRemoved, err := cleanImagePayloadRow(workCtx, conn, table, id, cutoff)
			if err != nil {
				failed++
				if firstError == nil {
					firstError = fmt.Errorf("row %d: %w", id, err)
				}
				logger.LegacyPrintf("service.image_payload_cleanup", "table=%s row=%d cleanup failed: %v", table, id, err)
				// Retry failed rows on the next complete sweep, without letting
				// one poison row permanently starve all subsequent IDs.
				progress.AfterID = id
				batchErr = err
				break
			}
			scanned++
			if changed {
				updated++
				removed += bytesRemoved
			}
			progress.AfterID = id
		}
		stopBatch()
		state, err := json.Marshal(progress)
		if err != nil {
			return err
		}
		if err := saveImagePayloadProgress(ctx, conn, key, string(state)); err != nil {
			return err
		}
		if batchErr != nil {
			return firstError
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func imagePayloadIDs(ctx context.Context, conn *sql.Conn, table string, progress imagePayloadProgress) ([]int64, error) {
	rows, err := conn.QueryContext(ctx, "SELECT id FROM "+table+" WHERE id > $1 AND id <= $2 AND image_generation IS NOT NULL ORDER BY id LIMIT $3", progress.AfterID, progress.UpperID, imagePayloadBatchSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0, imagePayloadBatchSize)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func cleanImagePayloadRow(ctx context.Context, conn *sql.Conn, table string, id int64, cutoff time.Time) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '1s'; SET LOCAL statement_timeout = '10s'"); err != nil {
		return false, 0, err
	}
	var raw []byte
	var created time.Time
	err = tx.QueryRowContext(ctx, "SELECT created_at, image_generation FROM "+table+" WHERE id = $1 AND image_generation IS NOT NULL FOR UPDATE SKIP LOCKED", id).Scan(&created, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	clean, removed, changed, err := imagepayload.CleanDetail(raw, created.Before(cutoff))
	if err != nil {
		return false, 0, fmt.Errorf("clean image payload row %d: %w", id, err)
	}
	if changed {
		if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET image_generation = $2::jsonb WHERE id = $1", id, string(clean)); err != nil {
			return false, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, 0, err
	}
	return changed, removed, nil
}

// Use the lock-owning session for all worker queries, including settings. Taking
// another pooled connection while holding this one can deadlock with other jobs.
func imagePayloadSetting(ctx context.Context, conn *sql.Conn, key string) (string, error) {
	var value string
	err := conn.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = $1", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func saveImagePayloadProgress(ctx context.Context, conn *sql.Conn, key, value string) error {
	_, err := conn.ExecContext(ctx, `INSERT INTO settings (key,value,updated_at) VALUES ($1,$2,NOW()) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`, key, value)
	return err
}
