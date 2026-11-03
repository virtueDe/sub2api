package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBillingBalanceReadDoesNotLoadUserGroups(t *testing.T) {
	repo, mock := newRedeemAdjustmentRepoMock(t)
	mock.ExpectQuery(`SELECT balance FROM users WHERE id = \$1 AND deleted_at IS NULL`).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(-0.25))
	balance, err := repo.GetBalance(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, -0.25, balance)
	require.NoError(t, mock.ExpectationsWereMet())
	mock.ExpectQuery(`SELECT balance FROM users WHERE id = \$1 AND deleted_at IS NULL`).WithArgs(int64(43)).WillReturnRows(sqlmock.NewRows([]string{"balance"}))
	_, err = repo.GetBalance(context.Background(), 43)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
