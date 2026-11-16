package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardFailureProbe struct {
	service.UsageLogRepository
	err            error
	rankingCalls   atomic.Int32
	rankingStarted chan struct{}
	rankingRelease chan struct{}
}

func (r *dashboardFailureProbe) GetDashboardStats(context.Context) (*usagestats.DashboardStats, error) {
	return &usagestats.DashboardStats{}, r.err
}

func (r *dashboardFailureProbe) GetUsageTrendWithUsageFilters(context.Context, time.Time, time.Time, string, usagestats.UsageLogFilters) ([]usagestats.TrendDataPoint, error) {
	return []usagestats.TrendDataPoint{}, r.err
}

func (r *dashboardFailureProbe) GetModelStatsWithUsageFiltersBySource(context.Context, time.Time, time.Time, usagestats.UsageLogFilters, string) ([]usagestats.ModelStat, error) {
	return []usagestats.ModelStat{}, r.err
}

func (r *dashboardFailureProbe) GetGroupStatsWithUsageFilters(context.Context, time.Time, time.Time, usagestats.UsageLogFilters) ([]usagestats.GroupStat, error) {
	return []usagestats.GroupStat{}, r.err
}

func (r *dashboardFailureProbe) GetUserUsageTrend(context.Context, time.Time, time.Time, string, int, string) ([]usagestats.UserUsageTrendPoint, error) {
	return []usagestats.UserUsageTrendPoint{}, r.err
}

func (r *dashboardFailureProbe) GetUserSpendingRanking(context.Context, time.Time, time.Time, int) (*usagestats.UserSpendingRankingResponse, error) {
	r.rankingCalls.Add(1)
	if r.rankingStarted != nil {
		r.rankingStarted <- struct{}{}
		<-r.rankingRelease
	}
	return &usagestats.UserSpendingRankingResponse{}, r.err
}

func TestDashboardStatisticsErrorsAreRetryableAndNotCached(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"stats", "trend", "models", "groups", "users-trend", "users-ranking", "snapshot-stats", "snapshot-trend", "snapshot-models", "snapshot-groups", "snapshot-users"} {
		t.Run(endpoint, func(t *testing.T) {
			resetDashboardReadCachesForTest()
			t.Cleanup(resetDashboardReadCachesForTest)
			repo := &dashboardFailureProbe{err: fmt.Errorf("repository: %w", dbstats.ErrBusy)}
			h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
			handlers := map[string]gin.HandlerFunc{
				"stats": h.GetStats, "trend": h.GetUsageTrend, "models": h.GetModelStats,
				"groups": h.GetGroupStats, "users-trend": h.GetUserUsageTrend, "users-ranking": h.GetUserSpendingRanking,
			}
			query := "?start_date=2026-11-01&end_date=2026-11-02"
			handler, ok := handlers[endpoint]
			if !ok {
				handler = h.GetSnapshotV2
				for part, flag := range map[string]string{"snapshot-stats": "include_stats", "snapshot-trend": "include_trend", "snapshot-models": "include_model_stats", "snapshot-groups": "include_group_stats", "snapshot-users": "include_users_trend"} {
					query += fmt.Sprintf("&%s=%t", flag, endpoint == part)
				}
			}
			router := gin.New()
			router.GET("/statistics", handler)
			call := func() *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/statistics"+query, nil))
				return rec
			}
			rec := call()
			require.Equal(t, http.StatusServiceUnavailable, rec.Code)
			require.Equal(t, "1", rec.Header().Get("Retry-After"))
			require.Contains(t, rec.Body.String(), `"reason":"STATISTICS_BUSY"`)
			require.NotContains(t, rec.Body.String(), `"data":`)
			repo.err = errors.New("private SQL connection details")
			rec = call()
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			require.Empty(t, rec.Header().Get("Retry-After"))
			require.NotContains(t, rec.Body.String(), "private SQL")
			repo.err = nil
			require.Equal(t, http.StatusOK, call().Code, "failure must not be cached")
		})
	}
}

func TestDashboardRankingCoalescesColdRequestsAndSeparatesRanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetDashboardReadCachesForTest()
	t.Cleanup(resetDashboardReadCachesForTest)
	repo := &dashboardFailureProbe{rankingStarted: make(chan struct{}, 8), rankingRelease: make(chan struct{})}
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	var arrived sync.WaitGroup
	arrived.Add(8)
	start := make(chan struct{})
	router.Use(func(c *gin.Context) {
		if c.Query("start_date") == "2026-11-01" {
			arrived.Done()
			<-start
		}
		c.Next()
	})
	router.GET("/ranking", h.GetUserSpendingRanking)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ranking?start_date=2026-11-01&end_date=2026-11-02", nil))
			codes <- rec.Code
		}()
	}
	arrived.Wait()
	close(start)
	select {
	case <-repo.rankingStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("ranking did not start")
	}
	// Keep the first load blocked while all same-key callers enter. A second
	// repository call here exposes the old Get/load/Set cache stampede.
	duplicate := false
	select {
	case <-repo.rankingStarted:
		duplicate = true
	case <-time.After(50 * time.Millisecond):
	}
	close(repo.rankingRelease)
	wg.Wait()
	require.False(t, duplicate, "same-key cold requests must share the in-flight load")
	close(codes)
	for code := range codes {
		require.Equal(t, http.StatusOK, code)
	}
	require.Equal(t, int32(1), repo.rankingCalls.Load())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ranking?start_date=2026-11-03&end_date=2026-11-04", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, int32(2), repo.rankingCalls.Load())
}
