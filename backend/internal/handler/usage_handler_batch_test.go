package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type batchUsageHandlerRepo struct {
	service.UsageLogRepository
	fail bool
}

func (r *batchUsageHandlerRepo) GetBatchAPIKeyUsageStats(ctx context.Context, ids []int64, start, end time.Time) (map[int64]*usagestats.BatchAPIKeyUsageStats, error) {
	if r.fail {
		return nil, context.DeadlineExceeded
	}
	return map[int64]*usagestats.BatchAPIKeyUsageStats{7: {APIKeyID: 7, TotalActualCost: 12}}, nil
}

type batchUsageOwnershipRepo struct {
	service.APIKeyRepository
	allowed atomic.Bool
	calls   atomic.Int32
}

func (r *batchUsageOwnershipRepo) VerifyOwnership(ctx context.Context, userID int64, ids []int64) ([]int64, error) {
	r.calls.Add(1)
	if r.allowed.Load() && userID == 42 {
		return []int64{7}, nil
	}
	return nil, nil
}

func TestDashboardBatchUsageVerifiesOwnershipBeforeEveryCacheRead(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "timeout"}[fail], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			usage := service.NewUsageService(&batchUsageHandlerRepo{fail: fail}, nil, nil, nil)
			t.Cleanup(usage.Stop)
			ownership := &batchUsageOwnershipRepo{}
			ownership.allowed.Store(true)
			keys := service.NewAPIKeyService(ownership, nil, nil, nil, nil, nil, nil)
			h := NewUsageHandler(usage, keys, nil, nil)
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})
				c.Next()
			})
			router.POST("/batch", h.DashboardAPIKeysUsage)
			request := func() service.BatchAPIKeyUsageSnapshot {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/batch", strings.NewReader(`{"api_key_ids":[7,99]}`))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				require.Equal(t, http.StatusOK, w.Code)
				var body struct {
					Data service.BatchAPIKeyUsageSnapshot `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				return body.Data
			}
			first := request()
			require.Equal(t, "pending", first.Statistics.State)
			require.Empty(t, first.Stats)
			require.Eventually(t, func() bool { return request().Statistics.State != "pending" }, time.Second, time.Millisecond)
			result := request()
			if fail {
				require.Empty(t, result.Stats)
				require.Equal(t, "unavailable", result.Statistics.State)
			} else {
				require.Equal(t, 12.0, result.Stats[7].TotalActualCost)
			}
			require.NotContains(t, result.Stats, int64(99))
			ownership.allowed.Store(false)
			before := ownership.calls.Load()
			require.Empty(t, request().Stats, "deletion/permission change must hide cached amounts")
			require.Equal(t, before+1, ownership.calls.Load())
		})
	}
}
