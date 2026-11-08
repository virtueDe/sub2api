//go:build unit

package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingApplyIdentifiesFailedSQLAndPreservesCause(t *testing.T) {
	cause := &pq.Error{Code: "57014", Message: "canceling statement due to user request"}
	for _, stage := range []string{
		"begin", "dedup_insert", "dedup_existing_lookup", "dedup_archive_lookup",
		"subscription", "balance", "api_key_quota", "api_key_rate_limit", "account_quota", "commit",
	} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			cmd := &service.UsageBillingCommand{RequestID: "client:diagnostic-request", APIKeyID: 7, UserID: 42, AccountID: 9}
			begin := mock.ExpectBegin()
			if stage == "begin" {
				begin.WillReturnError(cause)
			} else {
				insert := mock.ExpectQuery("INSERT INTO usage_billing_dedup").WithArgs(cmd.RequestID, cmd.APIKeyID, sqlmock.AnyArg())
				switch stage {
				case "dedup_insert":
					insert.WillReturnError(cause)
				case "dedup_existing_lookup":
					insert.WillReturnRows(sqlmock.NewRows([]string{"id"}))
					mock.ExpectQuery("SELECT request_fingerprint FROM usage_billing_dedup WHERE").WillReturnError(cause)
				default:
					insert.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1))
					archive := mock.ExpectQuery("SELECT request_fingerprint FROM usage_billing_dedup_archive")
					if stage == "dedup_archive_lookup" {
						archive.WillReturnError(cause)
					} else {
						archive.WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}))
					}
				}
				switch stage {
				case "subscription":
					id := int64(3)
					cmd.SubscriptionID, cmd.SubscriptionCost = &id, 1
					mock.ExpectExec("UPDATE user_subscriptions").WillReturnError(cause)
				case "balance":
					cmd.BalanceCost = 1
					mock.ExpectQuery(conditionalBalanceDeductSQL).WithArgs(1.0, cmd.UserID).WillReturnError(cause)
				case "api_key_quota":
					cmd.APIKeyQuotaCost = 1
					mock.ExpectQuery(apiKeyQuotaIncrementSQL).WillReturnError(cause)
				case "api_key_rate_limit":
					cmd.APIKeyRateLimitCost = 1
					mock.ExpectExec(apiKeyRateLimitIncrementSQL).WillReturnError(cause)
				case "account_quota":
					cmd.AccountQuotaCost, cmd.AccountType = 1, service.AccountTypeServiceAccount
					mock.ExpectQuery(accountQuotaIncrementSQL).WillReturnError(cause)
				}
				if stage == "commit" {
					mock.ExpectCommit().WillReturnError(cause)
				} else {
					mock.ExpectRollback()
				}
			}

			result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), cmd)
			require.Nil(t, result)
			require.ErrorIs(t, err, cause)
			var pgErr *pq.Error
			require.True(t, errors.As(err, &pgErr))
			require.Equal(t, pq.ErrorCode("57014"), pgErr.Code)
			for _, field := range []string{`request_id="client:diagnostic-request"`, "api_key_id=7", "user_id=42", "account_id=9", "sql_stage=" + stage, "sql_elapsed_ms=", "elapsed_ms=", "db_pool_in_use=", "db_pool_max_open="} {
				require.Contains(t, err.Error(), field)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUsageBillingApplyConflictStillRollsBackWithoutBalanceChange(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO usage_billing_dedup").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectQuery("SELECT request_fingerprint FROM usage_billing_dedup WHERE").WillReturnRows(sqlmock.NewRows([]string{"request_fingerprint"}).AddRow("different-fingerprint"))
	mock.ExpectRollback()
	result, err := (&usageBillingRepository{db: db}).Apply(context.Background(), &service.UsageBillingCommand{RequestID: "client:conflict", APIKeyID: 7, UserID: 42, BalanceCost: 1})
	require.Nil(t, result)
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	require.Contains(t, err.Error(), `request_id="client:conflict"`)
	require.NoError(t, mock.ExpectationsWereMet())
}
