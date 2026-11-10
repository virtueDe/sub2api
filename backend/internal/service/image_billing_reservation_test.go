//go:build unit

package service

import (
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"math"
	"net/http"
	"os"
	"testing"
	"time"
)

type imageReservationFake struct {
	UsageBillingRepository
	reserved, released, dispatched int
	amount                         float64
	sub                            *int64
	reserveErr, dispatchErr        error
}

func (r *imageReservationFake) ReserveImageSettlement(_ context.Context, _ UsageSettlementRef, _ int64, sub *int64, a float64) error {
	r.reserved++
	r.amount = a
	r.sub = sub
	return r.reserveErr
}
func (r *imageReservationFake) ReleaseImageSettlement(context.Context, UsageSettlementRef) error {
	r.released++
	return nil
}
func (r *imageReservationFake) MarkImageSettlementDispatched(context.Context, UsageSettlementRef) error {
	r.dispatched++
	return r.dispatchErr
}

func TestImageReservationOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		statuses    []int
		dispatchErr bool
		release     int
	}{
		{"before_upstream", nil, false, 1}, {"rejected", []int{429, 400}, false, 1},
		{"accepted", []int{200}, false, 0}, {"server_error", []int{500}, false, 0},
		{"timeout", []int{0}, false, 0}, {"ambiguous_then_rejected", []int{500, 429}, false, 0},
		{"dispatch_commit_unknown", []int{200}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &imageReservationFake{}
			if tc.dispatchErr {
				r.dispatchErr = context.DeadlineExceeded
			}
			cfg := &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}
			key := &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}
			ctx, done, err := reserveImageBilling(context.Background(), cfg, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1.25, true }, key, nil, InflightEstimateRequest{})
			require.NoError(t, err)
			require.NotEmpty(t, UsageSettlementFromContext(ctx))
			for _, status := range tc.statuses {
				err = BeginImageBillingDispatch(ctx)
				if tc.dispatchErr {
					require.Error(t, err)
					break
				}
				require.NoError(t, err)
				observe := ObserveImageBillingAttempt(ctx)
				if status == 0 {
					observe(nil, context.DeadlineExceeded)
				} else {
					observe(&http.Response{StatusCode: status}, nil)
				}
			}
			done()
			done()
			require.Equal(t, tc.release, r.released)
			if len(tc.statuses) > 0 {
				require.Equal(t, 1, r.dispatched)
			}
		})
	}
}
func TestImageReservationAdmission(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}
	key := &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}
	for _, amount := range []float64{-1, math.NaN(), math.Inf(1)} {
		r := &imageReservationFake{}
		_, _, err := reserveImageBilling(context.Background(), cfg, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return amount, true }, key, nil, InflightEstimateRequest{})
		require.Error(t, err)
		require.Zero(t, r.reserved)
	}
	r := &imageReservationFake{}
	price := func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 0, true }
	ctx, done, err := reserveImageBilling(context.Background(), cfg, r, nil, price, key, nil, InflightEstimateRequest{})
	require.NoError(t, err)
	require.Zero(t, r.amount)
	reused, _, err := reserveImageBilling(ctx, cfg, r, nil, price, key, nil, InflightEstimateRequest{})
	require.NoError(t, err)
	require.Equal(t, UsageSettlementFromContext(ctx), UsageSettlementFromContext(reused))
	require.Equal(t, 1, r.reserved)
	done()
	key.Group = &Group{SubscriptionType: SubscriptionTypeSubscription}
	_, _, err = reserveImageBilling(context.Background(), cfg, r, nil, price, key, nil, InflightEstimateRequest{})
	require.ErrorIs(t, err, ErrSubscriptionNotFound)
	_, done, err = reserveImageBilling(context.Background(), cfg, r, nil, price, key, &UserSubscription{ID: 9}, InflightEstimateRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(9), *r.sub)
	done()
	r.reserveErr = context.DeadlineExceeded
	_, _, err = reserveImageBilling(context.Background(), cfg, r, nil, price, key, &UserSubscription{ID: 9}, InflightEstimateRequest{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type imageJournalFake struct {
	UsageSettlementRepository
	err   error
	saved *UsageSettlementSnapshot
}

func (r *imageJournalFake) PrepareSettlement(_ context.Context, s *UsageSettlementSnapshot) error {
	r.saved = s
	return r.err
}
func TestImageSettlementJournalRecovery(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pricing.DataDir = t.TempDir()
	cmd := &UsageBillingCommand{RequestID: "settlement:journal", APIKeyID: 1, UserID: 2, BalanceCost: 1.25}
	cmd.Normalize()
	s := newUsageSettlementSnapshot(cmd, &UsageLog{RequestID: cmd.RequestID, APIKeyID: 1, UserID: 2, CreatedAt: time.Now(), APIKey: &APIKey{Key: "secret-must-not-persist"}, ImageGeneration: &ImageGenerationDetail{}}, &postUsageBillingParams{Cost: &CostBreakdown{ActualCost: 1.25}})
	path, err := saveImageSettlementFacts(cfg, s)
	require.NoError(t, err)
	again, err := saveImageSettlementFacts(cfg, s)
	require.NoError(t, err)
	require.Equal(t, path, again)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret-must-not-persist")
	altered := *s
	altered.Command.BalanceCost = 9
	_, err = saveImageSettlementFacts(cfg, &altered)
	require.ErrorIs(t, err, ErrUsageSettlementConflict)
	r := &imageJournalFake{err: context.DeadlineExceeded}
	cursor := ""
	require.Error(t, replayImageSettlementFacts(context.Background(), cfg, r, &cursor))
	_, err = os.Stat(path)
	require.NoError(t, err)
	r.err = nil
	require.NoError(t, replayImageSettlementFacts(context.Background(), cfg, r, &cursor))
	require.Equal(t, cmd.RequestFingerprint, r.saved.Command.RequestFingerprint)
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
}
