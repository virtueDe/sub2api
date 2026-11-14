package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImageProfitSettlementIdempotentAfterDateLock(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, service.ImageProfitLocation)
	old, _ := json.Marshal(service.ImageProfitReport{Date: "2026-11-07", Revision: 1, Status: "complete"})
	m.ExpectBegin()
	m.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("2026-11-07").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT id,result FROM image_profit_reports").WithArgs("2026-11-07").WillReturnRows(sqlmock.NewRows([]string{"id", "result"}).AddRow(9, old))
	m.ExpectCommit()
	got, err := r.Settle(context.Background(), day, "scheduled", 0, false)
	require.NoError(t, err)
	require.EqualValues(t, 9, got.ID)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitSettlementRollsBackWhenSourceFails(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, service.ImageProfitLocation)
	m.ExpectBegin()
	m.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT id,result FROM image_profit_reports").WillReturnError(sql.ErrNoRows)
	m.ExpectQuery("SELECT p.id").WillReturnRows(sqlmock.NewRows([]string{"id", "scope_type", "scope_id", "model", "resolution", "price", "effective", "note", "actor", "created"}))
	m.ExpectQuery("SELECT u.id,u.account_id").WillReturnError(context.DeadlineExceeded)
	m.ExpectRollback()
	_, err := r.Settle(context.Background(), day, "scheduled", 0, false)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitSettlementPreservesPriorReportWhenUsageWasCleaned(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, service.ImageProfitLocation)
	old, _ := json.Marshal(service.ImageProfitReport{Date: "2026-11-07", Revision: 1, Status: "complete", RequestCount: 1})
	m.ExpectBegin()
	m.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT id,result FROM image_profit_reports").WillReturnRows(sqlmock.NewRows([]string{"id", "result"}).AddRow(9, old))
	m.ExpectQuery("SELECT p.id").WillReturnRows(sqlmock.NewRows([]string{"id", "scope_type", "scope_id", "model", "resolution", "price", "effective", "note", "actor", "created"}))
	m.ExpectQuery("SELECT u.id,u.account_id").WillReturnRows(sqlmock.NewRows([]string{"account", "channel", "account_name", "channel_name", "model", "size", "count", "breakdown", "revenue", "created"}))
	m.ExpectRollback()
	_, err := r.Settle(context.Background(), day, "correct cost", 1, true)
	require.ErrorContains(t, err, "previous report was preserved")
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitChannelPriceUsesExistingChannelSchema(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	p := &service.ImageProcurementPrice{ScopeType: "channel", ScopeID: 2}
	m.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM channels WHERE id=\$1\)`).WithArgs(int64(2)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	require.ErrorContains(t, r.CreatePrice(context.Background(), p), "does not exist")
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitSettlementWritesIncompleteSnapshotWithoutPrices(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, service.ImageProfitLocation)
	m.ExpectBegin()
	m.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT id,result FROM image_profit_reports").WillReturnError(sql.ErrNoRows)
	m.ExpectQuery("SELECT p.id").WillReturnRows(sqlmock.NewRows([]string{"id", "scope_type", "scope_id", "model", "resolution", "price", "effective", "note", "actor", "created"}))
	m.ExpectQuery("SELECT u.id,u.account_id").WithArgs(day, day.AddDate(0, 0, 1)).WillReturnRows(sqlmock.NewRows([]string{"id", "account", "channel", "account_name", "channel_name", "model", "size", "count", "breakdown", "revenue", "created", "loss", "corrected", "excluded"}).AddRow(91, 1, 2, "account", "channel", "gemini-nano-banana-2.1", "1K", 2, []byte(`{"1K":2}`), "0.8", day.Add(time.Hour), false, false, false))
	m.ExpectQuery("SELECT MIN\\(created_at\\) FROM usage_logs").WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(day.AddDate(0, 0, -1)))
	m.ExpectQuery("INSERT INTO image_profit_reports").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
	m.ExpectCommit()
	report, err := r.Settle(context.Background(), day, "scheduled", 0, false)
	require.NoError(t, err)
	require.Equal(t, "incomplete", report.Status)
	require.Nil(t, report.Profit)
	require.Equal(t, []int64{91}, report.SourceIDs)
	require.Equal(t, "0.8", report.Revenue.String())
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitPendingDatesDoNotRequireConfiguredPrices(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, service.ImageProfitLocation)
	m.ExpectQuery("WITH first_day AS").WithArgs("2026-11-07", 14).WillReturnRows(sqlmock.NewRows([]string{"date"}).AddRow("2026-11-07"))
	dates, err := r.PendingDates(context.Background(), day, 14)
	require.NoError(t, err)
	require.Equal(t, []time.Time{day}, dates)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitSettlementRejectsReplacedSourceIDs(t *testing.T) {
	db, m := newSQLMock(t)
	r := &imageProfitRepository{db: db}
	day := time.Date(2026, 11, 7, 0, 0, 0, 0, service.ImageProfitLocation)
	old, _ := json.Marshal(service.ImageProfitReport{Date: "2026-11-07", Revision: 1, RequestCount: 1, SourceIDs: []int64{90}})
	m.ExpectBegin()
	m.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectQuery("SELECT id,result FROM image_profit_reports").WillReturnRows(sqlmock.NewRows([]string{"id", "result"}).AddRow(1, old))
	m.ExpectQuery("SELECT p.id").WillReturnRows(sqlmock.NewRows([]string{"id", "scope_type", "scope_id", "model", "resolution", "price", "effective", "note", "actor", "created"}))
	m.ExpectQuery("SELECT u.id,u.account_id").WillReturnRows(sqlmock.NewRows([]string{"id", "account", "channel", "account_name", "channel_name", "model", "size", "count", "breakdown", "revenue", "created", "loss", "corrected", "excluded"}).AddRow(91, 1, 2, "account", "channel", "gemini-nano-banana-2.1", "1K", 2, []byte(`{"1K":2}`), "0.8", day.Add(time.Hour), false, false, false))
	m.ExpectRollback()
	_, err := r.Settle(context.Background(), day, "correct cost", 1, true)
	require.ErrorContains(t, err, "previous report was preserved")
	require.NoError(t, m.ExpectationsWereMet())
}

func TestImageProfitHistoricalReportDoesNotInventZeroLoss(t *testing.T) {
	report, err := decodeProfitReport(1, []byte(`{"revenue_usd":"1","known_cost_usd":"0.2","lines":[{"revenue_usd":"1"}]}`))
	require.NoError(t, err)
	require.Nil(t, report.AbnormalLoss)
	require.Nil(t, report.Lines[0].AbnormalLoss)
}
