//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type durableBillingProviderStub struct {
	UsageBillingBatchRepository
	UsageSettlementRepository
	ImageBillingReservationRepository
	began, reserved, retried bool
}

func (r *durableBillingProviderStub) BeginSettlement(context.Context, string, int64, int64) error {
	r.began = true
	return nil
}

func (r *durableBillingProviderStub) ReserveImageSettlement(context.Context, UsageSettlementRef, int64, *int64, float64) error {
	r.reserved = true
	return nil
}

func (r *durableBillingProviderStub) RetryImageSettlementRelease(context.Context, func(context.Context, int64) error) error {
	r.retried = true
	return nil
}

func TestUsageBillingProviderPreservesDurableCapabilities(t *testing.T) {
	raw := &durableBillingProviderStub{UsageBillingBatchRepository: newLaneTestRepo()}
	lanes := NewUsageBillingLaneCombiner(raw)
	defer lanes.Stop()
	repo, err := ProvideUsageBillingRepository(lanes, raw)
	require.NoError(t, err)
	durable, ok := repo.(durableUsageBillingRepository)
	require.True(t, ok)
	ctx := context.Background()
	require.NoError(t, durable.BeginSettlement(ctx, "settlement:test", 1, 2))
	require.NoError(t, durable.ReserveImageSettlement(ctx, UsageSettlementRef{}, 2, nil, 1))
	require.NoError(t, durable.RetryImageSettlementRelease(ctx, nil))
	require.True(t, raw.began && raw.reserved && raw.retried)
	// Ordinary billing still passes through the lane combiner.
	_, err = repo.Apply(ctx, &UsageBillingCommand{RequestID: "provider:test", UserID: 2})
	require.NoError(t, err)
	require.Equal(t, uint64(1), lanes.Stats().SingleApplies)
}

func TestUsageBillingProviderRejectsMissingDurableCapabilities(t *testing.T) {
	raw := newLaneTestRepo()
	lanes := NewUsageBillingLaneCombiner(raw)
	defer lanes.Stop()
	repo, err := ProvideUsageBillingRepository(lanes, raw)
	require.ErrorContains(t, err, "durable image settlement")
	require.Nil(t, repo)
}
