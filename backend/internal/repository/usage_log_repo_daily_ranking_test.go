//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryGetDailyTokenRankingFiltersEligibleUsage(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newUsageLogRepositoryWithSQL(nil, db)
	start := time.Date(2026, 8, 26, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	end := start.AddDate(0, 0, 1)

	mock.ExpectQuery(`(?s)FROM usage_logs ul.*JOIN users u ON u\.id = ul\.user_id.*ul\.actual_cost > 0.*u\.role = 'user'.*u\.status = 'active'.*u\.deleted_at IS NULL.*ORDER BY total_tokens DESC, u\.id ASC.*LIMIT \$3`).
		WithArgs(start, end, 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "username", "total_tokens"}).
			AddRow(int64(7), "demo@example.com", "demo", int64(1200)).
			AddRow(int64(9), "second@example.com", "", int64(800)))

	rows, err := repo.GetDailyTokenRanking(context.Background(), start, end, 10)
	require.NoError(t, err)
	require.Equal(t, int64(7), rows[0].UserID)
	require.Equal(t, "demo", rows[0].Username)
	require.Equal(t, int64(1200), rows[0].TotalTokens)
	require.Equal(t, "second@example.com", rows[1].Email)
	require.NoError(t, mock.ExpectationsWereMet())
}
