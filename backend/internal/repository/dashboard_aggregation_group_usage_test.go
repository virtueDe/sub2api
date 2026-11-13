//go:build unit

package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestDashboardAggregationRepositorySyncGroupUsageRollupsBoundedProgress(t *testing.T) {
	setGroupUsageRollupTestTimezone(t)
	for _, steps := range []int{0, 3, 32} {
		t.Run(time.Duration(steps).String(), func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := newDashboardAggregationRepositoryWithSQL(db)
			today := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
			for range steps {
				mock.ExpectQuery(`SELECT process_group_usage_rollups`).WithArgs(today, "Asia/Shanghai").WillReturnRows(sqlmock.NewRows([]string{"processed"}).AddRow(1))
			}
			if steps < 32 {
				mock.ExpectQuery(`SELECT process_group_usage_rollups`).WithArgs(today, "Asia/Shanghai").WillReturnRows(sqlmock.NewRows([]string{"processed"}).AddRow(0))
			}
			require.NoError(t, repo.SyncGroupUsageRollups(context.Background(), today))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDashboardAggregationRepositorySyncGroupUsageRollupsPropagatesFailure(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectQuery(`SELECT process_group_usage_rollups`).WillReturnError(sql.ErrConnDone)
	require.ErrorIs(t, newDashboardAggregationRepositoryWithSQL(db).SyncGroupUsageRollups(context.Background(), time.Now()), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryRecomputeRangeQueuesAffectedDaysAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			db, mock := newSQLMock(t)
			start := time.Date(2026, 8, 1, 3, 0, 0, 0, time.UTC)
			end := start.Add(time.Hour)
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO usage_group_rollup_events`).WithArgs(start, end).WillReturnResult(sqlmock.NewResult(0, 1))
			if fail {
				mock.ExpectExec(`DELETE FROM usage_dashboard_hourly`).WillReturnError(sql.ErrConnDone)
				mock.ExpectRollback()
			} else {
				for _, query := range []string{`DELETE FROM usage_dashboard_hourly WHERE`, `DELETE FROM usage_dashboard_hourly_users WHERE`, `DELETE FROM usage_dashboard_daily WHERE`, `DELETE FROM usage_dashboard_daily_users WHERE`, `INSERT INTO usage_dashboard_hourly_users`, `INSERT INTO usage_dashboard_daily_users`, `INSERT INTO usage_dashboard_hourly`, `INSERT INTO usage_dashboard_daily`} {
					mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 1))
				}
				mock.ExpectCommit()
			}
			err := newDashboardAggregationRepositoryWithSQL(db).RecomputeRange(context.Background(), start, end)
			if fail {
				require.ErrorIs(t, err, sql.ErrConnDone)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestDashboardAggregationRepositoryCleanupUsageLogsCommitsWithoutSynchronousRebuild(t *testing.T) {
	db, mock := newSQLMock(t)
	cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT tableoid, ctid.*DELETE FROM usage_logs.*RETURNING created_at`).WithArgs(cutoff, usageLogsCleanupBatchSize).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(cutoff.Add(-time.Hour)))
	mock.ExpectCommit()
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsPartitionedSortsAndQueuesEachDrop(t *testing.T) {
	db, mock := newSQLMock(t)
	cutoff := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("usage_logs_202606").AddRow("usage_logs_invalid").AddRow("usage_logs_202604").AddRow("usage_logs_202607"))
	for _, name := range []string{"usage_logs_202604", "usage_logs_202606"} {
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`LOCK TABLE`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`INSERT INTO usage_key_rollup_events`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`INSERT INTO usage_group_rollup_events`).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(`DROP TABLE IF EXISTS "` + name + `"`).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectCommit()
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT tableoid, ctid.*WHERE created_at < \$1.*WHERE \(tableoid, ctid\) IN.*RETURNING created_at`).WithArgs(cutoff, usageLogsCleanupBatchSize).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(cutoff.Add(-time.Hour)))
	mock.ExpectCommit()
	require.NoError(t, newDashboardAggregationRepositoryWithSQL(db).CleanupUsageLogs(context.Background(), cutoff))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsFailureRollsBack(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)DELETE FROM usage_logs.*RETURNING created_at`).WillReturnRows(sqlmock.NewRows([]string{"created_at"}).AddRow(time.Now()).RowError(0, sql.ErrConnDone))
	mock.ExpectRollback()
	require.ErrorIs(t, newDashboardAggregationRepositoryWithSQL(db).CleanupUsageLogs(context.Background(), time.Now()), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDashboardAggregationRepositoryCleanupUsageLogsPartitionFailureRollsBackAndStops(t *testing.T) {
	db, mock := newSQLMock(t)
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT c.relname`).WillReturnRows(sqlmock.NewRows([]string{"relname"}).AddRow("usage_logs_202606").AddRow("usage_logs_202604"))
	mock.ExpectBegin()
	for _, query := range []string{`SELECT pg_advisory_xact_lock`, `SELECT pg_advisory_xact_lock`, `LOCK TABLE`, `INSERT INTO usage_key_rollup_events`, `INSERT INTO usage_group_rollup_events`} {
		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(`DROP TABLE IF EXISTS "usage_logs_202604"`).WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	require.ErrorIs(t, newDashboardAggregationRepositoryWithSQL(db).CleanupUsageLogs(context.Background(), time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)), sql.ErrConnDone)
	require.NoError(t, mock.ExpectationsWereMet())
}

func setGroupUsageRollupTestTimezone(t *testing.T) {
	t.Helper()
	useGroupUsageRepositoryTestTimezone(t, "Asia/Shanghai")
}
