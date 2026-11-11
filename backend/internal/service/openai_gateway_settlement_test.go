package service

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type openAIJournalSettlementRepoStub struct {
	UsageBillingRepository
	UsageSettlementRepository
	prepareErr error
	prepares   int
	prepared   *UsageSettlementSnapshot
}

func (r *openAIJournalSettlementRepoStub) PrepareSettlement(_ context.Context, snapshot *UsageSettlementSnapshot) error {
	r.prepares++
	r.prepared = snapshot
	return r.prepareErr
}

func TestOpenAIGatewayServiceRecordUsage_ImageFactsSurviveDatabaseTimeout(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	repo := &openAIJournalSettlementRepoStub{prepareErr: context.DeadlineExceeded}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	svc.cfg.Database.ImageBillingReservationEnabled = true
	svc.cfg.Pricing.DataDir = t.TempDir()
	groupID := int64(14)
	svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, "gpt-5.1")
	ctx := WithUsageSettlement(context.Background(), "settlement:openai-journal-timeout")

	err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID:       "upstream-image-response",
			UpstreamHeaders: http.Header{"X-Request-Id": {"upstream-image-response"}},
			Model:           "gpt-5.1", ImageCount: 1,
			Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 600, ImageOutputTokens: 100},
		},
		APIKey: &APIKey{ID: 1004, UserID: 2004, Key: "credential-must-not-persist", GroupID: &groupID,
			Group: &Group{ID: groupID, RateMultiplier: 1, Platform: PlatformOpenAI}},
		User: &User{ID: 2004}, Account: &Account{ID: 3004},
		UserAgent: "image-client/1.0", IPAddress: "203.0.113.10", SessionID: "image-session",
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 3, repo.prepares)
	require.Zero(t, usageRepo.calls, "failed settlement must not create a zero-cost bill")
	require.Zero(t, userRepo.deductCalls, "settlement must not fall back to a separate balance write")

	files, err := filepath.Glob(filepath.Join(svc.cfg.Pricing.DataDir, "image-settlement-facts", "*.pending"))
	require.NoError(t, err)
	require.Len(t, files, 1, "final financial facts must survive all database retries")
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.NotContains(t, string(data), "credential-must-not-persist")
	var snapshot UsageSettlementSnapshot
	require.NoError(t, json.Unmarshal(data, &snapshot))
	require.Equal(t, UsageSettlementFromContext(ctx), snapshot.Command.RequestID)
	require.Equal(t, int64(1004), snapshot.Command.APIKeyID)
	require.Equal(t, int64(2004), snapshot.Command.UserID)
	require.Positive(t, snapshot.Command.BalanceCost)
	require.Equal(t, snapshot.Command.BalanceCost, snapshot.Usage.ActualCost)
	require.Equal(t, 1, snapshot.Usage.ImageCount)
	require.Equal(t, 100, snapshot.Usage.ImageOutputTokens)
	require.Equal(t, optionalTrimmedStringPtr("image-client/1.0"), snapshot.Usage.UserAgent)
	require.Equal(t, optionalTrimmedStringPtr("203.0.113.10"), snapshot.Usage.IPAddress)
	require.Equal(t, optionalTrimmedStringPtr("image-session"), snapshot.Usage.SessionID)
	require.Equal(t, optionalTrimmedStringPtr("upstream-image-response"), snapshot.Usage.UpstreamRequestID)
	require.Equal(t, UsageSettlementFromContext(ctx), snapshot.Usage.RequestID, "upstream ID must not replace the billing identity")
	require.Equal(t, repo.prepared.Command.RequestFingerprint, snapshot.Command.RequestFingerprint)

	cursor := ""
	require.ErrorIs(t, replayImageSettlementFacts(context.Background(), svc.cfg, repo, &cursor), context.DeadlineExceeded)
	require.FileExists(t, files[0])
	repo.prepareErr = nil
	require.NoError(t, replayImageSettlementFacts(context.Background(), svc.cfg, repo, &cursor))
	require.Equal(t, snapshot, *repo.prepared, "recovery must replay the original price and billing identity")
	require.NoFileExists(t, files[0])
	require.NoError(t, replayImageSettlementFacts(context.Background(), svc.cfg, repo, &cursor))
	require.Equal(t, 5, repo.prepares, "recovered facts must not be replayed again")
}

func TestOpenAIGatewayServiceRecordUsage_DurablePlatformQuota(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "legacy"
		if enabled {
			name = "durable"
		}
		t.Run(name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			repo := &openAIRecordUsageBillingRepoStub{}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.cfg.Database.ImageBillingReservationEnabled = enabled
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "ordinary-openai-request", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 500}},
				APIKey: &APIKey{ID: 1}, User: &User{ID: 2}, Account: &Account{ID: 3}, QuotaPlatform: PlatformOpenAI,
			})
			require.NoError(t, err)
			require.Equal(t, 1, repo.calls)
			if !enabled {
				require.Nil(t, repo.lastCmd.PlatformQuota)
				return
			}
			require.NotNil(t, repo.lastCmd.PlatformQuota, "durable mode must include platform quota in the balance transaction")
			require.Equal(t, PlatformOpenAI, repo.lastCmd.PlatformQuota.Platform)
			require.Positive(t, repo.lastCmd.PlatformQuota.Cost)
			require.Equal(t, repo.lastCmd.BalanceCost, repo.lastCmd.PlatformQuota.Cost)
			require.Equal(t, usageRepo.lastLog.CreatedAt, repo.lastCmd.PlatformQuota.OccurredAt)
		})
	}
}
