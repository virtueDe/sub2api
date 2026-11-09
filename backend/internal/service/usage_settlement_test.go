//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestUsageSettlementIdentityAndSafeFacts(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "same-parent")
	a := WithUsageSettlement(parent, "settlement:child-a")
	b := WithUsageSettlement(parent, "settlement:child-b")
	require.Equal(t, "settlement:child-a", resolveUsageBillingRequestID(a, "upstream-a"))
	require.Equal(t, "settlement:child-b", resolveUsageBillingRequestID(b, "upstream-b"))
	require.Equal(t, "settlement:child-a", resolveUsageBillingRequestID(a, "different-failover"))
	for _, id := range []string{"web_search:charge", "grok-video:charge", GeminiCachedContentUsageRequestIDPrefix + "charge"} {
		require.Equal(t, id, resolveUsageBillingRequestID(a, id))
	}
	cmd := &UsageBillingCommand{RequestID: "settlement:child-a", BalanceCost: 0.000078125}
	cmd.Normalize()
	s := newUsageSettlementSnapshot(cmd, &UsageLog{CreatedAt: time.Now(), ActualCost: 0.000078125, APIKey: &APIKey{Key: "credential-must-not-persist"}, User: &User{PasswordHash: "password-must-not-persist"}}, &postUsageBillingParams{Cost: &CostBreakdown{ActualCost: 0.000078125}})
	data, err := json.Marshal(s)
	require.NoError(t, err)
	require.NotContains(t, string(data), "credential-must-not-persist")
	require.NotContains(t, string(data), "password-must-not-persist")
	var decoded UsageSettlementSnapshot
	require.NoError(t, json.Unmarshal(data, &decoded))
	decoded.Command.Normalize()
	require.Equal(t, cmd.RequestFingerprint, decoded.Command.RequestFingerprint)
	require.Equal(t, 0.00007813, decoded.Command.BalanceCost)
}

type settlementTestRepository struct {
	UsageBillingRepository
	UsageSettlementRepository
	ids      []string
	beginErr error
	prepares int
	prepared *UsageSettlementSnapshot
}

func (r *settlementTestRepository) BeginSettlement(_ context.Context, id string, _ int64, _ int64) error {
	r.ids = append(r.ids, id)
	return r.beginErr
}
func (r *settlementTestRepository) PrepareSettlement(_ context.Context, s *UsageSettlementSnapshot) error {
	r.prepares++
	r.prepared = s
	if r.prepares == 1 {
		return context.DeadlineExceeded
	}
	return nil
}

func TestUsageSettlementBeginAndPrepareRetry(t *testing.T) {
	r := &settlementTestRepository{}
	s := &GatewayService{cfg: &config.Config{Database: config.DatabaseConfig{UsageSettlementEnabled: true}}, usageBillingRepo: r}
	key := &APIKey{ID: 7, UserID: 8}
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "shared-parent")
	a, err := s.BeginGeminiImageSettlement(parent, key)
	require.NoError(t, err)
	b, err := s.BeginGeminiImageSettlement(parent, key)
	require.NoError(t, err)
	require.NotEqual(t, UsageSettlementFromContext(a), UsageSettlementFromContext(b))
	r.beginErr = errors.New("database unavailable")
	_, err = s.BeginGeminiImageSettlement(parent, key)
	require.ErrorIs(t, err, r.beginErr)
	snapshot := &UsageSettlementSnapshot{Version: 1, Command: UsageBillingCommand{RequestFingerprint: "immutable", BalanceCost: 1.25}}
	require.NoError(t, prepareUsageSettlementWithRetry(context.Background(), r, snapshot))
	require.Equal(t, 2, r.prepares)
	require.Same(t, snapshot, r.prepared)
}
