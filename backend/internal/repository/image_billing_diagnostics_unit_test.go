//go:build unit

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func imageDiagnosticCapture(t *testing.T, run func()) []dbstats.DiagnosticSample {
	t.Helper()
	dir := t.TempDir()
	recorder, err := dbstats.NewRecorder(dir, nil, nil)
	require.NoError(t, err)
	t.Cleanup(recorder.Stop)
	run()
	recorder.Stop()
	files, err := filepath.Glob(filepath.Join(dir, "incident-*.json"))
	require.NoError(t, err)
	if len(files) == 0 {
		return nil
	}
	require.Len(t, files, 1)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	var incident struct {
		Reason  string
		Samples []dbstats.DiagnosticSample
	}
	require.NoError(t, json.Unmarshal(data, &incident))
	require.Equal(t, "billing_error", incident.Reason)
	return incident.Samples
}

func TestImageReserveDiagnosticsStagesAndCause(t *testing.T) {
	for _, stage := range []string{"billing_begin", "payer_lock", "reservation_insert", "reserve_commit"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cause := &pq.Error{Code: "57014", Message: "canceling statement due to statement timeout"}
			begin := mock.ExpectBegin()
			if stage == "billing_begin" {
				begin.WillReturnError(cause)
			} else {
				payer := mock.ExpectQuery("SELECT balance FROM users")
				if stage == "payer_lock" {
					payer.WillReturnError(cause)
				} else {
					payer.WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(10))
					mock.ExpectQuery("SELECT reserved_amount").WillReturnRows(sqlmock.NewRows([]string{"reserved_amount", "reserved_subscription_id", "user_id", "state"}))
					mock.ExpectQuery("SELECT quota,quota_used").WillReturnRows(sqlmock.NewRows([]string{"quota", "quota_used"}).AddRow(0, 0))
					mock.ExpectExec("UPDATE users").WillReturnResult(sqlmock.NewResult(0, 1))
					insert := mock.ExpectExec("INSERT INTO usage_settlements")
					if stage == "reservation_insert" {
						insert.WillReturnError(cause)
					} else {
						insert.WillReturnResult(sqlmock.NewResult(1, 1))
						mock.ExpectCommit().WillReturnError(cause)
					}
				}
				if stage != "reserve_commit" {
					mock.ExpectRollback()
				}
			}
			samples := imageDiagnosticCapture(t, func() {
				err = (&usageBillingRepository{db: db}).ReserveImageSettlement(context.Background(), service.UsageSettlementRef{RequestID: "settlement:diagnostic", APIKeyID: 7}, 42, nil, 1)
			})
			require.ErrorIs(t, err, cause)
			var pg *pq.Error
			require.ErrorAs(t, err, &pg)
			require.Contains(t, err.Error(), "sql_stage="+stage)
			require.NotContains(t, err.Error(), "sql_stage=payer_admission")
			if stage == "payer_lock" || stage == "reservation_insert" {
				require.Contains(t, err.Error(), "db_pool_in_use=1", "snapshot must precede connection cleanup")
			}
			require.Zero(t, db.Stats().InUse, "the original transaction cleanup must still run")
			require.NotEmpty(t, samples)
			require.Positive(t, samples[len(samples)-1].Metrics["image_reserve"].Timeout)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestImageReleaseDiagnosticsStagesAndCause(t *testing.T) {
	for _, stage := range []string{"payer_lookup", "release_decision", "release_lock", "refund_balance", "refund_commit"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cause := errors.New("database unavailable")
			lookup := mock.ExpectQuery("SELECT user_id FROM usage_settlements")
			if stage == "payer_lookup" {
				lookup.WillReturnError(cause)
			} else {
				lookup.WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(42))
				decision := mock.ExpectExec("UPDATE usage_settlements SET state='release_pending'")
				if stage == "release_decision" {
					decision.WillReturnError(cause)
				} else {
					decision.WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectBegin()
					lock := mock.ExpectQuery("SELECT state,reserved_amount")
					if stage == "release_lock" {
						lock.WillReturnError(cause)
					} else {
						lock.WillReturnRows(sqlmock.NewRows([]string{"state", "reserved_amount", "reserved_subscription_id", "user_id"}).AddRow("release_pending", 1, nil, 42))
						refund := mock.ExpectExec("UPDATE users")
						if stage == "refund_balance" {
							refund.WillReturnError(cause)
						} else {
							refund.WillReturnResult(sqlmock.NewResult(0, 1))
							mock.ExpectExec("UPDATE usage_settlements SET state='released'").WillReturnResult(sqlmock.NewResult(0, 1))
							mock.ExpectCommit().WillReturnError(cause)
						}
					}
					if stage != "refund_commit" {
						mock.ExpectRollback()
					}
				}
			}
			samples := imageDiagnosticCapture(t, func() {
				err = (&usageBillingRepository{db: db}).ReleaseImageSettlement(context.Background(), service.UsageSettlementRef{RequestID: "settlement:diagnostic", APIKeyID: 7})
			})
			require.ErrorIs(t, err, cause)
			require.Contains(t, err.Error(), "sql_stage="+stage)
			require.NotEmpty(t, samples)
			require.Positive(t, samples[len(samples)-1].Metrics["image_release"].Failed)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestImageBillingDiagnosticsExcludeBusinessRejectionsAndCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, cause := range []error{nil, sql.ErrNoRows, context.Canceled, sql.ErrTxDone, service.ErrUsageSettlementConflict, service.ErrAPIKeyQuotaExhausted, service.ErrInsufficientBalance, service.ErrSubscriptionNotFound, service.ErrDailyLimitExceeded, service.ErrWeeklyLimitExceeded, service.ErrMonthlyLimitExceeded, &pq.Error{Code: "57014", Message: "canceling statement due to user request"}} {
		err := cause
		samples := imageDiagnosticCapture(t, func() {
			started := time.Now()
			stage := "payer_lock"
			// nil DB is deliberate: ignored outcomes must not access the pool.
			(&usageBillingRepository{}).recordImageBillingFailure(canceled, service.UsageSettlementRef{}, "image_reserve", started, &stage, &started, &err)
		})
		require.Equal(t, cause, err)
		require.Empty(t, samples)
	}
	require.False(t, imageBillingBusinessRejection(context.Background(), sql.ErrTxDone), "an unexpectedly completed transaction must still be diagnosed")
	require.False(t, imageBillingBusinessRejection(canceled, errors.New("connection lost")), "cancellation must not suppress unrelated database failures")
}

func TestImageBillingDiagnosticsPreserveCancellationCauseOnDeadline(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cause := &pq.Error{Code: "57014", Message: "canceling statement due to user request"}
	err = cause
	samples := imageDiagnosticCapture(t, func() {
		started := time.Now()
		stage := "payer_lock"
		(&usageBillingRepository{db: db}).recordImageBillingFailure(ctx, service.UsageSettlementRef{}, "image_reserve", started, &stage, &started, &err)
	})
	require.ErrorIs(t, err, cause)
	require.False(t, errors.Is(err, context.DeadlineExceeded), "diagnostics must preserve the original error chain")
	require.NotEmpty(t, samples)
	require.Positive(t, samples[len(samples)-1].Metrics["image_reserve"].Timeout)
	require.Equal(t, 1, strings.Count(err.Error(), "sql_stage="))
}
