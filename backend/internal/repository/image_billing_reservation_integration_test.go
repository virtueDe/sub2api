//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func imageReservationFixture(t *testing.T) (*usageBillingRepository, *service.UsageSettlementSnapshot, service.UsageSettlementRef) {
	r, s := settlementFixture(t)
	s.Command.RequestID = "settlement:" + uuid.NewString()
	s.Usage.RequestID = s.Command.RequestID
	s.Command.RequestFingerprint = ""
	s.Command.Normalize()
	return r, s, service.UsageSettlementRef{RequestID: s.Command.RequestID, APIKeyID: s.Command.APIKeyID}
}
func checkImageWallet(t *testing.T, userID int64, balance, held float64) {
	t.Helper()
	var b, h float64
	require.NoError(t, integrationDB.QueryRow(`SELECT balance,frozen_balance FROM users WHERE id=$1`, userID).Scan(&b, &h))
	require.Equal(t, balance, b)
	require.Equal(t, held, h)
}
func TestImageReservationAtomicCaptureAndReplay(t *testing.T) {
	for _, actual := range []float64{0, 1.25, 3} {
		t.Run(fmt.Sprint(actual), func(t *testing.T) {
			ctx := context.Background()
			r, s, ref := imageReservationFixture(t)
			require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 2))
			require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 2))
			checkImageWallet(t, s.Command.UserID, 98, 2)
			require.ErrorIs(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 3), service.ErrUsageSettlementConflict)
			require.NoError(t, r.MarkImageSettlementDispatched(ctx, ref))
			s.Command.BalanceCost = actual
			s.Command.APIKeyQuotaCost = actual
			s.Command.RequestFingerprint = ""
			s.Command.Normalize()
			s.Usage.ActualCost = actual
			s.Usage.TotalCost = actual
			require.NoError(t, r.PrepareSettlement(ctx, s))
			_, result, err := r.ApplySettlement(ctx, ref, &service.ImageGenerationDetail{})
			require.NoError(t, err)
			require.True(t, result.Applied)
			checkImageWallet(t, s.Command.UserID, 100-actual, 0)
			_, result, err = r.ApplySettlement(ctx, ref, nil)
			require.NoError(t, err)
			require.False(t, result.Applied)
			checkImageWallet(t, s.Command.UserID, 100-actual, 0)
			var count int
			var cost float64
			var noDetail bool
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*),max(actual_cost),bool_and(image_generation IS NULL) FROM usage_logs WHERE request_id=$1`, ref.RequestID).Scan(&count, &cost, &noDetail))
			require.Equal(t, 1, count)
			require.Equal(t, actual, cost)
			require.True(t, noDetail)
			require.ErrorIs(t, r.ReleaseImageSettlement(ctx, ref), service.ErrUsageSettlementConflict)
		})
	}
}
func TestImageReservationRollbackRetainsHold(t *testing.T) {
	ctx := context.Background()
	r, s, ref := imageReservationFixture(t)
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 2))
	s.Command.AccountID = -123
	s.Usage.AccountID = -123
	require.NoError(t, r.PrepareSettlement(ctx, s))
	_, _, err := r.ApplySettlement(ctx, ref, nil)
	require.Error(t, err)
	checkImageWallet(t, s.Command.UserID, 98, 2)
	var n int
	var state string
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, ref.RequestID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM usage_settlements WHERE request_id=$1`, ref.RequestID).Scan(&state))
	require.Equal(t, "ready", state)
}
func TestImageReservationConcurrentAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, s, _ := imageReservationFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- r.ReserveImageSettlement(ctx, service.UsageSettlementRef{RequestID: "settlement:" + uuid.NewString(), APIKeyID: s.Command.APIKeyID}, s.Command.UserID, nil, 8)
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else {
			require.ErrorIs(t, err, service.ErrInsufficientBalance)
		}
	}
	require.Equal(t, 12, ok)
	checkImageWallet(t, s.Command.UserID, 4, 96)
	// The same payer lock also prevents concurrent requests exceeding a key quota.
	r, s, ref := imageReservationFixture(t)
	_, err := integrationDB.Exec(`UPDATE api_keys SET quota=10 WHERE id=$1`, ref.APIKeyID)
	require.NoError(t, err)
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 6))
	ref.RequestID = "settlement:" + uuid.NewString()
	require.ErrorIs(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 6), service.ErrAPIKeyQuotaExhausted)
	checkImageWallet(t, s.Command.UserID, 94, 6)
}
func TestImageReservationReleaseRecoveryAndDispatchFence(t *testing.T) {
	ctx := context.Background()
	r, s, ref := imageReservationFixture(t)
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 2))
	require.NoError(t, r.ReleaseImageSettlement(ctx, ref))
	require.NoError(t, r.ReleaseImageSettlement(ctx, ref))
	checkImageWallet(t, s.Command.UserID, 100, 0)
	require.ErrorIs(t, r.MarkImageSettlementDispatched(ctx, ref), service.ErrUsageSettlementConflict)
	// Simulate process exit before upstream: recovery may refund only before dispatch.
	_, stale, staleRef := imageReservationFixture(t)
	_, unknown, unknownRef := imageReservationFixture(t)
	require.NoError(t, r.ReserveImageSettlement(ctx, staleRef, stale.Command.UserID, nil, 2))
	require.NoError(t, r.ReserveImageSettlement(ctx, unknownRef, unknown.Command.UserID, nil, 2))
	require.NoError(t, r.MarkImageSettlementDispatched(ctx, unknownRef))
	_, err := integrationDB.Exec(`UPDATE usage_settlements SET created_at=now()-interval '3 hours' WHERE request_id IN ($1,$2)`, staleRef.RequestID, unknownRef.RequestID)
	require.NoError(t, err)
	calls := 0
	for i := 0; i < 10; i++ {
		require.NoError(t, r.RetryImageSettlementRelease(ctx, func(context.Context, int64) error { calls++; return nil }))
	}
	require.GreaterOrEqual(t, calls, 2)
	checkImageWallet(t, stale.Command.UserID, 100, 0)
	checkImageWallet(t, unknown.Command.UserID, 98, 2)
	_, err = r.ReviewExpiredSettlements(ctx)
	require.NoError(t, err)
	var state string
	require.NoError(t, integrationDB.QueryRow(`SELECT state FROM usage_settlements WHERE request_id=$1`, unknownRef.RequestID).Scan(&state))
	require.Equal(t, "needs_review", state)
	// Final financial facts can recover a review hold after a late journal replay.
	require.NoError(t, r.PrepareSettlement(ctx, unknown))
	_, _, err = r.ApplySettlement(ctx, unknownRef, nil)
	require.NoError(t, err)
	checkImageWallet(t, unknown.Command.UserID, 98.75, 0)
}
func TestImageReservationSubscriptionAdmission(t *testing.T) {
	ctx := context.Background()
	r, s, ref := imageReservationFixture(t)
	client := testEntClient(t)
	limit := 10.0
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformGemini, SubscriptionType: service.SubscriptionTypeSubscription, DailyLimitUSD: &limit})
	sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: s.Command.UserID, GroupID: group.ID})
	_, err := integrationDB.Exec(`UPDATE api_keys SET group_id=$2 WHERE id=$1`, ref.APIKeyID, group.ID)
	require.NoError(t, err)
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, &sub.ID, 6))
	second := ref
	second.RequestID = "settlement:" + uuid.NewString()
	require.ErrorIs(t, r.ReserveImageSettlement(ctx, second, s.Command.UserID, &sub.ID, 6), service.ErrDailyLimitExceeded)
	checkImageWallet(t, s.Command.UserID, 100, 0)
	s.Command.SubscriptionID = &sub.ID
	s.Command.SubscriptionCost = 1.25
	s.Command.BalanceCost = 0
	s.Command.RequestFingerprint = ""
	s.Command.Normalize()
	require.NoError(t, r.PrepareSettlement(ctx, s))
	_, _, err = r.ApplySettlement(ctx, ref, nil)
	require.NoError(t, err)
	checkImageWallet(t, s.Command.UserID, 100, 0)
	var used float64
	require.NoError(t, integrationDB.QueryRow(`SELECT daily_usage_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&used))
	require.Equal(t, 1.25, used)
	require.NoError(t, r.ReserveImageSettlement(ctx, second, s.Command.UserID, &sub.ID, 6))
}

func TestImageReservationCacheFailureDoesNotBlockOtherRefunds(t *testing.T) {
	ctx := context.Background()
	r, a, ar := imageReservationFixture(t)
	_, b, br := imageReservationFixture(t)
	require.NoError(t, r.ReserveImageSettlement(ctx, ar, a.Command.UserID, nil, 2))
	require.NoError(t, r.ReserveImageSettlement(ctx, br, b.Command.UserID, nil, 2))
	require.NoError(t, r.ReleaseImageSettlement(ctx, ar))
	_, err := integrationDB.Exec(`UPDATE usage_settlements SET updated_at='2020-01-01' WHERE request_id=$1`, ar.RequestID)
	require.NoError(t, err)
	_, err = integrationDB.Exec(`UPDATE usage_settlements SET state='release_pending',updated_at='2020-01-02' WHERE request_id=$1`, br.RequestID)
	require.NoError(t, err)
	invalidate := func(context.Context, int64) error { return context.DeadlineExceeded }
	require.Error(t, r.RetryImageSettlementRelease(ctx, invalidate))
	require.Error(t, r.RetryImageSettlementRelease(ctx, invalidate))
	checkImageWallet(t, b.Command.UserID, 100, 0)
}
