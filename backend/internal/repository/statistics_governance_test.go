package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestStatisticsRepositoriesShareAdmissionBeforeAnySQL(t *testing.T) {
	budget := dbstats.NewBudget(1)
	_, err := dbstats.Run(context.Background(), budget, time.Second, func(ctx context.Context) (int, error) {
		// Nil SQL handles deliberately fail if any denied operation reaches SQL.
		usage := &governedUsageLogRepository{usageLogRepository: &usageLogRepository{}, budget: budget}
		aggregation := &governedDashboardAggregationRepository{dashboardAggregationRepository: &dashboardAggregationRepository{}, budget: budget}
		profit := &governedImageProfitRepository{imageProfitRepository: &imageProfitRepository{}, budget: budget}
		_, e := usage.GetUserDashboardStats(ctx, 1)
		require.ErrorIs(t, e, dbstats.ErrBusy)
		_, e = usage.GetStatsWithFilters(ctx, UsageLogFilters{})
		require.ErrorIs(t, e, dbstats.ErrBusy)
		_, e = usage.GetAPIKeyTotalStats(ctx, 1)
		require.ErrorIs(t, e, dbstats.ErrBusy)
		require.ErrorIs(t, aggregation.AggregateRange(ctx, time.Now().Add(-time.Hour), time.Now()), dbstats.ErrBusy)
		_, e = profit.Settle(ctx, time.Now(), "scheduled", 0, false)
		require.ErrorIs(t, e, dbstats.ErrBusy)
		return 0, nil
	})
	require.NoError(t, err)
}

func TestStatisticsReportingPoolKeepsQuotaReadsOnCore(t *testing.T) {
	core, coreMock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = core.Close() }()
	read, readMock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = read.Close() }()
	r := &governedUsageLogRepository{usageLogRepository: &usageLogRepository{sql: core}, reporting: &usageLogRepository{sql: read}, budget: dbstats.NewBudget(1)}
	readMock.ExpectQuery(`FROM usage_logs WHERE api_key_id = \$1$`).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count", "input", "output", "creation", "read", "cost", "actual", "duration"}).AddRow(10, 20, 30, 40, 50, 1.2, 0.8, 150))
	_, err = r.GetAPIKeyTotalStats(context.Background(), 7)
	require.NoError(t, err)
	coreMock.ExpectQuery(`FROM usage_logs WHERE account_id = \$1`).WithArgs(int64(3), sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens", "cost", "standard", "user"}).AddRow(1, 5, 0.1, 0.2, 0.3))
	_, err = r.GetAccountTodayStats(context.Background(), 3)
	require.NoError(t, err)
	require.NoError(t, coreMock.ExpectationsWereMet())
	require.NoError(t, readMock.ExpectationsWereMet())
}

func TestStatisticsBudgetDoesNotBlockSchedulingQuotaReads(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	budget := dbstats.NewBudget(1)
	repo := &governedUsageLogRepository{usageLogRepository: &usageLogRepository{sql: db}, budget: budget}
	mock.ExpectQuery(`FROM usage_logs WHERE account_id = \$1`).WithArgs(int64(3), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"requests", "tokens", "cost", "standard", "user"}).AddRow(1, 5, 0.1, 0.2, 0.3))
	_, err = dbstats.Run(context.Background(), budget, time.Second, func(ctx context.Context) (int, error) {
		stats, e := repo.GetAccountTodayStats(ctx, 3)
		require.NoError(t, e)
		require.Equal(t, int64(1), stats.Requests)
		return 0, nil
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStatisticsCombinedDashboardHoldsOneLeaseWithoutNestedRejection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &governedUsageLogRepository{usageLogRepository: &usageLogRepository{sql: db}, budget: dbstats.NewBudget(1)}
	mock.ExpectQuery(`FROM usage_logs WHERE api_key_id = \$1$`).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"count", "input", "output", "creation", "read", "cost", "actual", "duration"}).AddRow(10, 20, 30, 40, 50, 1.2, 0.8, 150))
	mock.ExpectQuery(`FROM usage_logs WHERE api_key_id = \$1 AND created_at >= \$2`).WithArgs(int64(7), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count", "input", "output", "creation", "read", "cost", "actual"}).AddRow(1, 2, 3, 4, 5, 0.2, 0.1))
	mock.ExpectQuery(`FROM usage_logs`).WithArgs(sqlmock.AnyArg(), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"rpm", "tpm"}).AddRow(10, 35))
	stats, err := repo.GetAPIKeyDashboardStats(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(140), stats.TotalTokens)
	require.Equal(t, int64(14), stats.TodayTokens)
	require.Equal(t, float64(150), stats.AverageDurationMs)
	require.Equal(t, int64(2), stats.Rpm)
	require.Equal(t, int64(7), stats.Tpm)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStatisticsAccountReportPropagatesComponentTimeout(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &governedUsageLogRepository{usageLogRepository: &usageLogRepository{sql: db}, budget: dbstats.NewBudget(1)}
	mock.ExpectQuery(`TO_CHAR`).WillReturnRows(sqlmock.NewRows([]string{"date", "requests", "tokens", "cost", "actual", "user_cost"}))
	mock.ExpectQuery(`AVG\(duration_ms\)`).WillReturnRows(sqlmock.NewRows([]string{"avg"}).AddRow(0))
	mock.ExpectQuery(`FROM usage_logs`).WillReturnError(context.DeadlineExceeded)
	result, err := repo.GetAccountUsageStats(context.Background(), 1, time.Now().Add(-time.Hour), time.Now())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, result)
	_, err = dbstats.Run(context.Background(), repo.budget, time.Second, func(context.Context) (int, error) { return 1, nil })
	require.NoError(t, err, "deadline failure must release admission")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUsageRepositoryConstructorPreservesOptionalInterfaces(t *testing.T) {
	repo := NewUsageLogRepository(nil, nil)
	_, ok := repo.(interface {
		CreateBestEffort(context.Context, *service.UsageLog) error
	})
	require.True(t, ok)
	_, ok = repo.(interface {
		GetAPIKeyTodayStats(context.Context, int64) (*UserDashboardStats, error)
	})
	require.True(t, ok)
	_, ok = repo.(*governedUsageLogRepository)
	require.True(t, ok)
	_, ok = repo.(interface {
		GetAccountWindowStatsBatch(context.Context, []int64, time.Time) (map[int64]*usagestats.AccountStats, error)
	})
	require.True(t, ok)
	_, ok = repo.(interface {
		GetGeminiUsageTotalsBatch(context.Context, []int64, time.Time, time.Time) (map[int64]service.GeminiUsageTotals, error)
	})
	require.True(t, ok)
}
