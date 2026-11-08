package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	calls int
}

func (r *unavailableUsageStatsRepo) GetAPIKeyDashboardStats(context.Context, int64) (*usagestats.UserDashboardStats, error) {
	r.calls++
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
		require.Equal(t, 1, statsRepo.calls, "polling must not repeat a failed scan")
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
