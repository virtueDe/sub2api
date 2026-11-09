package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Embed concrete repositories to preserve optional filtering and write interfaces.
// Internal sequential calls execute within the outer admission lease.
type governedUsageLogRepository struct {
	*usageLogRepository
	budget *dbstats.Budget
}
type governedDashboardAggregationRepository struct {
	*dashboardAggregationRepository
	budget *dbstats.Budget
}
type governedImageProfitRepository struct {
	*imageProfitRepository
	budget *dbstats.Budget
}

func (r *governedUsageLogRepository) GetUserStats(ctx context.Context, userID int64, startTime, endTime time.Time) (*UserStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserStats, error) {
		return r.usageLogRepository.GetUserStats(ctx, userID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetDashboardStats(ctx context.Context) (*DashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*DashboardStats, error) { return r.usageLogRepository.GetDashboardStats(ctx) })
}

func (r *governedUsageLogRepository) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*DashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*DashboardStats, error) {
		return r.usageLogRepository.GetDashboardStatsWithRange(ctx, start, end)
	})
}

func (r *governedUsageLogRepository) GetUserDashboardStats(ctx context.Context, userID int64) (*UserDashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserDashboardStats, error) {
		return r.usageLogRepository.GetUserDashboardStats(ctx, userID)
	})
}

func (r *governedUsageLogRepository) GetAPIKeyDashboardStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserDashboardStats, error) {
		return r.usageLogRepository.GetAPIKeyDashboardStats(ctx, apiKeyID)
	})
}

func (r *governedUsageLogRepository) GetUserStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (*usagestats.UsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*usagestats.UsageStats, error) {
		return r.usageLogRepository.GetUserStatsAggregated(ctx, userID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetAPIKeyStatsAggregated(ctx context.Context, apiKeyID int64, startTime, endTime time.Time) (*usagestats.UsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*usagestats.UsageStats, error) {
		return r.usageLogRepository.GetAPIKeyStatsAggregated(ctx, apiKeyID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetAccountStatsAggregated(ctx context.Context, accountID int64, startTime, endTime time.Time) (*usagestats.UsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*usagestats.UsageStats, error) {
		return r.usageLogRepository.GetAccountStatsAggregated(ctx, accountID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetModelStatsAggregated(ctx context.Context, modelName string, startTime, endTime time.Time) (*usagestats.UsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*usagestats.UsageStats, error) {
		return r.usageLogRepository.GetModelStatsAggregated(ctx, modelName, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetDailyStatsAggregated(ctx context.Context, userID int64, startTime, endTime time.Time) (result []map[string]any, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]map[string]any, error) {
		return r.usageLogRepository.GetDailyStatsAggregated(ctx, userID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetBatchUserUsageStats(ctx context.Context, userIDs []int64, startTime, endTime time.Time) (map[int64]*BatchUserUsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (map[int64]*BatchUserUsageStats, error) {
		return r.usageLogRepository.GetBatchUserUsageStats(ctx, userIDs, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetBatchAPIKeyUsageStats(ctx context.Context, apiKeyIDs []int64, startTime, endTime time.Time) (map[int64]*BatchAPIKeyUsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (map[int64]*BatchAPIKeyUsageStats, error) {
		return r.usageLogRepository.GetBatchAPIKeyUsageStats(ctx, apiKeyIDs, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetGlobalStats(ctx context.Context, startTime, endTime time.Time) (*UsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UsageStats, error) {
		return r.usageLogRepository.GetGlobalStats(ctx, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetStatsWithFilters(ctx context.Context, filters UsageLogFilters) (*UsageStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UsageStats, error) {
		return r.usageLogRepository.GetStatsWithFilters(ctx, filters)
	})
}

func (r *governedUsageLogRepository) GetEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]EndpointStat, error) {
		return r.usageLogRepository.GetEndpointStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, model, requestType, stream, billingType)
	})
}

func (r *governedUsageLogRepository) GetUpstreamEndpointStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) ([]EndpointStat, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]EndpointStat, error) {
		return r.usageLogRepository.GetUpstreamEndpointStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, model, requestType, stream, billingType)
	})
}

func (r *governedUsageLogRepository) GetAccountUsageStats(ctx context.Context, accountID int64, startTime, endTime time.Time) (resp *AccountUsageStatsResponse, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*AccountUsageStatsResponse, error) {
		return r.usageLogRepository.GetAccountUsageStats(ctx, accountID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetAPIKeyUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) (results []APIKeyUsageTrendPoint, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]APIKeyUsageTrendPoint, error) {
		return r.usageLogRepository.GetAPIKeyUsageTrend(ctx, startTime, endTime, granularity, limit)
	})
}

func (r *governedUsageLogRepository) GetUserUsageTrend(ctx context.Context, startTime, endTime time.Time, granularity string, limit int, metric string) (results []UserUsageTrendPoint, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]UserUsageTrendPoint, error) {
		return r.usageLogRepository.GetUserUsageTrend(ctx, startTime, endTime, granularity, limit, metric)
	})
}

func (r *governedUsageLogRepository) GetUserSpendingRanking(ctx context.Context, startTime, endTime time.Time, limit int) (result *UserSpendingRankingResponse, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserSpendingRankingResponse, error) {
		return r.usageLogRepository.GetUserSpendingRanking(ctx, startTime, endTime, limit)
	})
}

func (r *governedUsageLogRepository) GetUserUsageTrendByUserID(ctx context.Context, userID int64, startTime, endTime time.Time, granularity string) (results []TrendDataPoint, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]TrendDataPoint, error) {
		return r.usageLogRepository.GetUserUsageTrendByUserID(ctx, userID, startTime, endTime, granularity)
	})
}

func (r *governedUsageLogRepository) GetUserModelStats(ctx context.Context, userID int64, startTime, endTime time.Time) (results []ModelStat, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]ModelStat, error) {
		return r.usageLogRepository.GetUserModelStats(ctx, userID, startTime, endTime)
	})
}

func (r *governedUsageLogRepository) GetUsageTrendWithFilters(ctx context.Context, startTime, endTime time.Time, granularity string, userID, apiKeyID, accountID, groupID int64, model string, requestType *int16, stream *bool, billingType *int8) (results []TrendDataPoint, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]TrendDataPoint, error) {
		return r.usageLogRepository.GetUsageTrendWithFilters(ctx, startTime, endTime, granularity, userID, apiKeyID, accountID, groupID, model, requestType, stream, billingType)
	})
}

func (r *governedUsageLogRepository) GetUsageTrendWithUsageFilters(ctx context.Context, startTime, endTime time.Time, granularity string, filters UsageLogFilters) (results []TrendDataPoint, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]TrendDataPoint, error) {
		return r.usageLogRepository.GetUsageTrendWithUsageFilters(ctx, startTime, endTime, granularity, filters)
	})
}

func (r *governedUsageLogRepository) GetModelStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8) (results []ModelStat, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]ModelStat, error) {
		return r.usageLogRepository.GetModelStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, requestType, stream, billingType)
	})
}

func (r *governedUsageLogRepository) GetModelStatsWithFiltersBySource(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8, source string) (results []ModelStat, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]ModelStat, error) {
		return r.usageLogRepository.GetModelStatsWithFiltersBySource(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, requestType, stream, billingType, source)
	})
}

func (r *governedUsageLogRepository) GetModelStatsWithUsageFiltersBySource(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters, source string) (results []ModelStat, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]ModelStat, error) {
		return r.usageLogRepository.GetModelStatsWithUsageFiltersBySource(ctx, startTime, endTime, filters, source)
	})
}

func (r *governedUsageLogRepository) GetGroupStatsWithFilters(ctx context.Context, startTime, endTime time.Time, userID, apiKeyID, accountID, groupID int64, requestType *int16, stream *bool, billingType *int8) (results []usagestats.GroupStat, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]usagestats.GroupStat, error) {
		return r.usageLogRepository.GetGroupStatsWithFilters(ctx, startTime, endTime, userID, apiKeyID, accountID, groupID, requestType, stream, billingType)
	})
}

func (r *governedUsageLogRepository) GetGroupStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters UsageLogFilters) (results []usagestats.GroupStat, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]usagestats.GroupStat, error) {
		return r.usageLogRepository.GetGroupStatsWithUsageFilters(ctx, startTime, endTime, filters)
	})
}

func (r *governedUsageLogRepository) GetUserBreakdownStats(ctx context.Context, startTime, endTime time.Time, dim usagestats.UserBreakdownDimension, limit int) (results []usagestats.UserBreakdownItem, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]usagestats.UserBreakdownItem, error) {
		return r.usageLogRepository.GetUserBreakdownStats(ctx, startTime, endTime, dim, limit)
	})
}

func (r *governedUsageLogRepository) GetAllGroupUsageSummary(ctx context.Context, todayStart time.Time) ([]usagestats.GroupUsageSummary, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]usagestats.GroupUsageSummary, error) {
		return r.usageLogRepository.GetAllGroupUsageSummary(ctx, todayStart)
	})
}

func (r *governedUsageLogRepository) GetDailyTokenRanking(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
) (results []usagestats.DailyTokenRankingSource, err error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]usagestats.DailyTokenRankingSource, error) {
		return r.usageLogRepository.GetDailyTokenRanking(ctx, startTime, endTime, limit)
	})
}

func (r *governedUsageLogRepository) GetDailyTokenRankingForSettlement(
	ctx context.Context,
	startTime, endTime time.Time,
	limit int,
) ([]usagestats.DailyTokenRankingSource, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]usagestats.DailyTokenRankingSource, error) {
		return r.usageLogRepository.GetDailyTokenRankingForSettlement(ctx, startTime, endTime, limit)
	})
}

func (r *governedDashboardAggregationRepository) AggregateRange(ctx context.Context, start, end time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error {
		return r.dashboardAggregationRepository.AggregateRange(ctx, start, end)
	})
}

func (r *governedDashboardAggregationRepository) RecomputeRange(ctx context.Context, start, end time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error {
		return r.dashboardAggregationRepository.RecomputeRange(ctx, start, end)
	})
}

func (r *governedDashboardAggregationRepository) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error {
		return r.dashboardAggregationRepository.CleanupAggregates(ctx, hourlyCutoff, dailyCutoff)
	})
}

func (r *governedDashboardAggregationRepository) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error { return r.dashboardAggregationRepository.CleanupUsageLogs(ctx, cutoff) })
}

func (r *governedDashboardAggregationRepository) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error {
		return r.dashboardAggregationRepository.CleanupUsageBillingDedup(ctx, cutoff)
	})
}

func (r *governedDashboardAggregationRepository) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error {
		return r.dashboardAggregationRepository.EnsureUsageLogsPartitions(ctx, now)
	})
}

func (r *governedDashboardAggregationRepository) SyncGroupUsageRollups(ctx context.Context, todayStart time.Time) error {
	return dbstats.RunMaintenance(ctx, r.budget, func(ctx context.Context) error {
		return r.dashboardAggregationRepository.SyncGroupUsageRollups(ctx, todayStart)
	})
}

func (r *governedImageProfitRepository) PendingDates(ctx context.Context, through time.Time, limit int) ([]time.Time, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) ([]time.Time, error) {
		return r.imageProfitRepository.PendingDates(ctx, through, limit)
	})
}

func (r *governedImageProfitRepository) Settle(ctx context.Context, day time.Time, reason string, actor int64, force bool) (*service.ImageProfitReport, error) {
	return dbstats.Run(ctx, r.budget, dbstats.MaintenanceTimeout, func(ctx context.Context) (*service.ImageProfitReport, error) {
		return r.imageProfitRepository.Settle(ctx, day, reason, actor, force)
	})
}

func (r *governedUsageLogRepository) GetAPIKeyTotalStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserDashboardStats, error) {
		return r.usageLogRepository.GetAPIKeyTotalStats(ctx, apiKeyID)
	})
}

func (r *governedUsageLogRepository) GetAPIKeyTodayStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserDashboardStats, error) {
		return r.usageLogRepository.GetAPIKeyTodayStats(ctx, apiKeyID)
	})
}

func (r *governedUsageLogRepository) GetAPIKeyPerformanceStats(ctx context.Context, apiKeyID int64) (*UserDashboardStats, error) {
	return dbstats.Run(ctx, r.budget, dbstats.ReadTimeout, func(ctx context.Context) (*UserDashboardStats, error) {
		return r.usageLogRepository.GetAPIKeyPerformanceStats(ctx, apiKeyID)
	})
}
