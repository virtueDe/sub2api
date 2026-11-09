package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unavailableUsageStatsRepo struct {
	service.UsageLogRepository
	calls atomic.Int32
}

func (r *unavailableUsageStatsRepo) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	r.calls.Add(1)
	return nil, context.DeadlineExceeded
}

func (r *unavailableUsageStatsRepo) GetAPIKeyTotalStats(ctx context.Context, id int64) (*usagestats.UserDashboardStats, error) {
	return r.GetAPIKeyDashboardStats(ctx, id)
}
func (r *unavailableUsageStatsRepo) GetAPIKeyTodayStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return nil, context.DeadlineExceeded
}
func (r *unavailableUsageStatsRepo) GetAPIKeyPerformanceStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	return nil, context.DeadlineExceeded
}
func (r *unavailableUsageStatsRepo) GetUsageTrendWithFilters(context.Context, time.Time, time.Time, string, int64, int64, int64, int64, string, *int16, *bool, *int8) ([]usagestats.TrendDataPoint, error) {
	return nil, context.DeadlineExceeded
}
func (r *unavailableUsageStatsRepo) GetModelStatsWithFilters(context.Context, time.Time, time.Time, int64, int64, int64, int64, *int16, *bool, *int8) ([]usagestats.ModelStat, error) {
	return nil, context.DeadlineExceeded
}

type liveUsageBalanceRepo struct {
	service.UserRepository
	balance float64
	calls   int
}

func (r *liveUsageBalanceRepo) GetByID(context.Context, int64) (*service.User, error) {
	r.calls++
	return &service.User{ID: 42, Balance: r.balance}, nil
}

func (*liveUsageBalanceRepo) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func TestUsageStatisticsBackoffStillReturnsLiveBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	statsRepo := &unavailableUsageStatsRepo{}
	users := &liveUsageBalanceRepo{}
	h := &GatewayHandler{
		usageService: service.NewUsageService(statsRepo, nil, nil, nil),
		userService:  service.NewUserService(users, nil, nil, nil),
	}
	for i, balance := range []float64{12, 9.5} {
		users.balance = balance
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 42, Status: service.StatusAPIKeyActive})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		h.Usage(c)
		require.Equal(t, http.StatusOK, recorder.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
		require.Equal(t, balance, body["balance"])
		require.Equal(t, balance, body["remaining"])
		require.Equal(t, "unrestricted", body["mode"])
		for _, key := range []string{"usage", "daily_usage", "model_stats"} {
			require.NotContains(t, body, key, "failed statistics must not masquerade as zero usage")
		}
		require.Equal(t, i+1, users.calls)
		require.Eventually(t, func() bool { return statsRepo.calls.Load() == 1 }, time.Second, time.Millisecond)
		require.Contains(t, body, "statistics")
	}
}

func TestUsageUnrestrictedIncludesWeeklyWindowStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)

	weeklyWindowStart := time.Date(2026, time.July, 13, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	c.Set(string(middleware.ContextKeySubscription), &service.UserSubscription{
		WeeklyWindowStart: &weeklyWindowStart,
	})

	handler := &GatewayHandler{}
	handler.usageUnrestricted(
		c,
		context.Background(),
		&service.APIKey{Group: &service.Group{
			Name:             "Weekly plan",
			SubscriptionType: service.SubscriptionTypeSubscription,
		}},
		middleware.AuthSubject{},
		nil,
		nil,
		nil,
	)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Subscription struct {
			WeeklyWindowStart *time.Time `json:"weekly_window_start"`
		} `json:"subscription"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.NotNil(t, response.Subscription.WeeklyWindowStart)
	require.True(t, weeklyWindowStart.Equal(*response.Subscription.WeeklyWindowStart))
}

type blockingUsageStatsRepo struct {
	unavailableUsageStatsRepo
	release chan struct{}
}

func (r *blockingUsageStatsRepo) GetAPIKeyTotalStats(ctx context.Context, _ int64) (*usagestats.UserDashboardStats, error) {
	select {
	case <-r.release:
		return &usagestats.UserDashboardStats{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestUsageColdStatisticsDoNotDelayBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &blockingUsageStatsRepo{release: make(chan struct{})}
	defer close(repo.release)
	users := &liveUsageBalanceRepo{balance: 23}
	h := &GatewayHandler{usageService: service.NewUsageService(repo, nil, nil, nil), userService: service.NewUserService(users, nil, nil, nil)}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: 42, Status: service.StatusAPIKeyActive})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	done := make(chan struct{})
	go func() { h.Usage(c); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("balance response waited for statistics")
	}
	require.Equal(t, http.StatusOK, recorder.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, float64(23), body["balance"])
	require.NotContains(t, body, "usage")
	statistics, ok := body["statistics"].(map[string]any)
	require.True(t, ok)
	total, ok := statistics["total"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "pending", total["state"])
}
