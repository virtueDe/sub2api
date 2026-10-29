//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

func TestImagePayloadSettingsAndSchedule(t *testing.T) {
	repo := newMockSettingRepo()
	svc, err := NewImagePayloadCleanupService(repo, nil, &config.Config{Timezone: "Asia/Shanghai"})
	require.NoError(t, err)
	defer svc.Stop()
	ctx := context.Background()
	cfg, err := svc.GetSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, &ImagePayloadCleanupSettings{RetainDays: 3, Timezone: "Asia/Shanghai"}, cfg)
	require.NoError(t, svc.run(ctx, time.Now()), "disabled does not touch the database")
	for _, days := range []int{-1, 0, 366} {
		_, err := svc.UpdateSettings(ctx, ImagePayloadCleanupSettings{Enabled: true, RetainDays: days})
		require.Error(t, err)
	}
	saved, err := svc.UpdateSettings(ctx, ImagePayloadCleanupSettings{Enabled: true, RetainDays: 7, Timezone: "UTC"})
	require.NoError(t, err)
	cfg, err = svc.GetSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, saved, cfg)
	require.Equal(t, "Asia/Shanghai", cfg.Timezone)
	require.ErrorContains(t, svc.run(ctx, time.Now()), "database unavailable")
	schedule, err := cron.ParseStandard("CRON_TZ=Asia/Shanghai " + imagePayloadCron)
	require.NoError(t, err)
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, svc.location)
	require.Equal(t, now.Add(time.Hour), schedule.Next(now))
	repo.data[imagePayloadSettingsKey] = "invalid"
	_, err = svc.GetSettings(ctx)
	require.Error(t, err)
	repo.getValueErr = errors.New("offline")
	_, err = svc.GetSettings(ctx)
	require.ErrorContains(t, err, "offline")
}

func TestImagePayloadCutoffCalendarDays(t *testing.T) {
	for _, tc := range []struct {
		zone, now, want string
		days            int
	}{
		{"Asia/Shanghai", "2026-10-28T02:00:00+08:00", "2026-10-26T00:00:00+08:00", 3},
		{"Asia/Shanghai", "2026-10-01T02:00:00+08:00", "2026-09-29T00:00:00+08:00", 3},
		{"America/New_York", "2026-03-09T02:00:00-04:00", "2026-03-07T00:00:00-05:00", 3},
	} {
		loc, err := time.LoadLocation(tc.zone)
		require.NoError(t, err)
		now, err := time.Parse(time.RFC3339, tc.now)
		require.NoError(t, err)
		require.Equal(t, tc.want, imagePayloadCutoff(now, tc.days, loc).Format(time.RFC3339))
	}
}

func TestImagePayloadRowRetentionBoundaryAndRollback(t *testing.T) {
	cutoff := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		created      time.Time
		update, fail bool
	}{
		{"old", cutoff.Add(-time.Nanosecond), true, false}, {"boundary", cutoff, false, false},
		{"recent", cutoff.Add(time.Hour), false, false}, {"write failure", cutoff.Add(-time.Hour), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			conn, err := db.Conn(context.Background())
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()
			mock.ExpectBegin()
			mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("SELECT created_at, image_generation FROM usage_logs").WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"created_at", "image_generation"}).AddRow(tc.created, `{"images":[{"b64_json":"output","url":"https://example.test"}],"prompt":"cat"}`))
			if tc.update {
				query := mock.ExpectExec("UPDATE usage_logs SET image_generation").WithArgs(int64(5), `{"images":[{"url":"https://example.test"}],"prompt":"cat"}`)
				if tc.fail {
					query.WillReturnError(errors.New("write failed"))
					mock.ExpectRollback()
				} else {
					query.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			if !tc.fail {
				mock.ExpectCommit()
			}
			changed, _, err := cleanImagePayloadRow(context.Background(), conn, "usage_logs", 5, cutoff)
			if tc.fail {
				require.ErrorContains(t, err, "write failed")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.update, changed)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestImagePayloadLockContentionAndSingleConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := newMockSettingRepo()
	svc, err := NewImagePayloadCleanupService(repo, db, &config.Config{Timezone: "UTC"})
	require.NoError(t, err)
	defer svc.Stop()
	_, err = svc.UpdateSettings(context.Background(), ImagePayloadCleanupSettings{Enabled: true, RetainDays: 3})
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	// Even a one-connection pool can attempt the singleton lock safely.
	for range 2 {
		mock.ExpectQuery("SELECT pg_try_advisory_lock").WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	}
	require.NoError(t, svc.run(context.Background(), time.Now()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImagePayloadFailureCheckpointAndResume(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := newMockSettingRepo()
	repo.data[imagePayloadSettingsKey] = `{"enabled":true,"retain_days":3}`
	svc, err := NewImagePayloadCleanupService(repo, db, &config.Config{Timezone: "UTC"})
	require.NoError(t, err)
	defer svc.Stop()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	expectImagePayloadSetting(mock, imagePayloadSettingsKey+"_progress_usage_logs", "")
	mock.ExpectQuery("SELECT COALESCE").WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(9))
	expectImagePayloadSetting(mock, imagePayloadSettingsKey, `{"enabled":true,"retain_days":3}`)
	mock.ExpectQuery("SELECT id FROM usage_logs").WithArgs(int64(0), int64(9), imagePayloadBatchSize).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5).AddRow(9))
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT created_at").WithArgs(int64(5)).WillReturnError(errors.New("statement timeout"))
	mock.ExpectRollback()
	mock.ExpectExec("INSERT INTO settings").WithArgs(imagePayloadSettingsKey+"_progress_usage_logs", `{"after_id":5,"upper_id":9}`).WillReturnResult(sqlmock.NewResult(0, 1))
	require.ErrorContains(t, svc.cleanTable(context.Background(), conn, "usage_logs", time.Now()), "statement timeout")
	expectImagePayloadSetting(mock, imagePayloadSettingsKey+"_progress_usage_logs", `{"after_id":5,"upper_id":9}`)
	expectImagePayloadSetting(mock, imagePayloadSettingsKey, `{"enabled":true,"retain_days":3}`)
	mock.ExpectQuery("SELECT id FROM usage_logs").WithArgs(int64(5), int64(9), imagePayloadBatchSize).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL").WillReturnResult(sqlmock.NewResult(0, 0))
	// A concurrently deleted or locked row is skipped without writing stale data.
	mock.ExpectQuery("SELECT created_at").WithArgs(int64(9)).WillReturnRows(sqlmock.NewRows([]string{"created_at", "image_generation"}))
	mock.ExpectRollback()
	mock.ExpectExec("INSERT INTO settings").WithArgs(imagePayloadSettingsKey+"_progress_usage_logs", `{"after_id":9,"upper_id":9}`).WillReturnResult(sqlmock.NewResult(0, 1))
	expectImagePayloadSetting(mock, imagePayloadSettingsKey, `{"enabled":true,"retain_days":3}`)
	mock.ExpectQuery("SELECT id FROM usage_logs").WithArgs(int64(9), int64(9), imagePayloadBatchSize).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("INSERT INTO settings").WithArgs(imagePayloadSettingsKey+"_progress_usage_logs", `{"after_id":0,"upper_id":0}`).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, svc.cleanTable(context.Background(), conn, "usage_logs", time.Now()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestImagePayloadDisabledStopsResumedWork(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := newMockSettingRepo()
	repo.data[imagePayloadSettingsKey+"_progress_ops_error_logs"] = `{"after_id":5,"upper_id":9}`
	svc, err := NewImagePayloadCleanupService(repo, db, &config.Config{})
	require.NoError(t, err)
	defer svc.Stop()
	conn, err := db.Conn(context.Background())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	expectImagePayloadSetting(mock, imagePayloadSettingsKey+"_progress_ops_error_logs", `{"after_id":5,"upper_id":9}`)
	expectImagePayloadSetting(mock, imagePayloadSettingsKey, `{"enabled":false,"retain_days":3}`)
	require.NoError(t, svc.cleanTable(context.Background(), conn, "ops_error_logs", time.Now()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func expectImagePayloadSetting(mock sqlmock.Sqlmock, key, value string) {
	mock.ExpectQuery("SELECT value FROM settings").WithArgs(key).WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(value))
}
