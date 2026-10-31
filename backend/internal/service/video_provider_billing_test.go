//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProviderVideoPerRequestCostIgnoresDurationAndTokens(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		for _, duration := range []int{0, 5, 30} {
			t.Run(fmt.Sprintf("%s/%ds", platform, duration), func(t *testing.T) {
				price := 0.5
				group := &Group{ID: 1, Platform: platform, ModelPricing: []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}}
				key := &APIKey{Group: group, GroupID: &group.ID}
				billing := &BillingService{}
				s := &OpenAIGatewayService{resolver: NewModelPricingResolver(nil, billing), billingService: billing}
				result := &OpenAIForwardResult{ResponseID: platform + ":task", Model: "video", VideoCount: 1, VideoDurationSeconds: duration}
				require.True(t, s.VideoProviderPricingConfigured(context.Background(), key, "video"))
				require.True(t, s.ProviderVideoBillingReady(context.Background(), key, result))
				cost, err := s.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"video"}, 9, 8, 0.4, 7, UsageTokens{OutputTokens: 500000}, "", nil, false, time.Now())
				require.NoError(t, err)
				require.InDelta(t, 0.5, cost.TotalCost, 1e-12)
				require.InDelta(t, 0.2, cost.ActualCost, 1e-12)
				require.Equal(t, string(BillingModePerRequest), cost.BillingMode)
			})
		}
	}
}

func TestSeedanceVideoCountPreservesTokenTariff(t *testing.T) {
	price := 0.000002
	group := &Group{ID: 1, Platform: PlatformSeedance, ModelPricing: []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModeToken, OutputPrice: &price}}}
	billing := &BillingService{}
	s := &OpenAIGatewayService{resolver: NewModelPricingResolver(nil, billing), billingService: billing}
	result := &OpenAIForwardResult{ResponseID: "seedance:task", Model: "video", VideoCount: 1, Usage: OpenAIUsage{OutputTokens: 1000}}
	key := &APIKey{Group: group, GroupID: &group.ID}
	require.True(t, s.ProviderVideoBillingReady(context.Background(), key, result))
	cost, err := s.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"video"}, 2, 8, 9, 1, UsageTokens{OutputTokens: 1000}, "", nil, false, time.Now())
	require.NoError(t, err)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
	require.InDelta(t, 0.002, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.004, cost.ActualCost, 1e-12)
}

func TestProviderVideoPerRequestReservationUsesVideoMultiplier(t *testing.T) {
	for _, kind := range []InflightEstimateKind{InflightEstimateVideo, InflightEstimateTokenVideo} {
		for _, textRate := range []float64{0, 9} {
			price, legacySecondPrice := 0.5, 100.0
			group := &Group{ID: 1, Platform: PlatformWan, RateMultiplier: textRate, ImageRateIndependent: true, ImageRateMultiplier: textRate,
				VideoRateIndependent: true, VideoRateMultiplier: 0.4, VideoPrice720P: &legacySecondPrice,
				ModelPricing: []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}}
			key := &APIKey{User: &User{ID: 1}, Group: group, GroupID: &group.ID}
			billing := &BillingService{}
			deps := inflightEstimateDeps{billing: billing, resolver: NewModelPricingResolver(nil, billing)}
			estimate, priced := deps.estimate(context.Background(), key, InflightEstimateRequest{Model: "video", Kind: kind, Units: 1, VideoDurationSeconds: 30, VideoResolution: "720p"})
			require.True(t, priced)
			require.InDelta(t, 0.2, estimate, 1e-12, "fixed per-video price must not be inflated by seconds, image/text rates or legacy tariffs")
		}
	}
}

func TestSeedanceTokenVideoReservationRetainsTokenEstimate(t *testing.T) {
	price := 0.000002
	group := &Group{ID: 1, Platform: PlatformSeedance, RateMultiplier: 2,
		ModelPricing: []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModeToken, OutputPrice: &price}}}
	key := &APIKey{User: &User{ID: 1}, Group: group, GroupID: &group.ID}
	billing := &BillingService{}
	deps := inflightEstimateDeps{billing: billing, resolver: NewModelPricingResolver(nil, billing)}
	estimate, priced := deps.estimate(context.Background(), key, InflightEstimateRequest{Model: "video", Kind: InflightEstimateTokenVideo, MaxTokens: 1000})
	require.True(t, priced)
	require.InDelta(t, 0.004, estimate, 1e-12)
}

func TestProviderVideoFreePerRequestReservationIsPriced(t *testing.T) {
	for _, kind := range []InflightEstimateKind{InflightEstimateVideo, InflightEstimateTokenVideo} {
		for _, freePrice := range []bool{false, true} {
			price, rate := 0.5, 0.0
			if freePrice {
				price, rate = 0, 1
			}
			group := &Group{ID: 1, Platform: PlatformWan, RateMultiplier: 9, VideoRateIndependent: true, VideoRateMultiplier: rate,
				ModelPricing: []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}}
			key := &APIKey{User: &User{ID: 1}, Group: group, GroupID: &group.ID}
			billing := &BillingService{}
			deps := inflightEstimateDeps{billing: billing, resolver: NewModelPricingResolver(nil, billing), accountMappedModels: func(context.Context, *APIKey, string) []string {
				t.Fatal("an explicitly free price must not fall back to another model")
				return nil
			}}
			estimate, priced := deps.estimate(context.Background(), key, InflightEstimateRequest{Model: "video", Kind: kind, VideoDurationSeconds: 30})
			require.True(t, priced)
			require.Zero(t, estimate)
		}
	}
}

func TestProviderVideoPerRequestChannelPriceAndGroupOverride(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		t.Run(platform, func(t *testing.T) {
			price := 0.8
			group := &Group{ID: 1, Platform: platform, RateMultiplier: 1}
			key := &APIKey{User: &User{ID: 1}, Group: group, GroupID: &group.ID}
			cache := newEmptyChannelCache()
			cache.loadedAt = time.Now()
			cache.groupPlatform[group.ID] = platform
			cache.channelByGroupID[group.ID] = &Channel{ID: 1, Status: StatusActive}
			cache.pricingByGroupModel[channelModelKey{groupID: group.ID, platform: platform, model: "video"}] = &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &price}
			channels := &ChannelService{}
			channels.cache.Store(cache)
			billing := &BillingService{}
			s := &OpenAIGatewayService{resolver: NewModelPricingResolver(channels, billing), billingService: billing}
			result := &OpenAIForwardResult{ResponseID: platform + ":task", Model: "video", VideoCount: 1}
			for _, expected := range []float64{0.8, 0.3} {
				if expected == 0.3 {
					group.ModelPricing = []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModePerRequest, PerRequestPrice: &expected}}
				}
				require.True(t, s.VideoProviderPricingConfigured(context.Background(), key, "video"))
				cost, err := s.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"video"}, 1, 1, 1, 1, UsageTokens{}, "", nil, false, time.Now())
				require.NoError(t, err)
				require.InDelta(t, expected, cost.ActualCost, 1e-12)
				deps := inflightEstimateDeps{billing: billing, resolver: s.resolver}
				estimate, priced := deps.estimate(context.Background(), key, InflightEstimateRequest{Model: "video", Kind: InflightEstimateVideo, VideoDurationSeconds: 30})
				require.True(t, priced)
				require.InDelta(t, expected, estimate, 1e-12)
			}
		})
	}
}

type providerVideoBillingRepo struct {
	UsageBillingRepository
	commands map[string]*UsageBillingCommand
	charged  float64
}

func (r *providerVideoBillingRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	key := fmt.Sprintf("%d:%s", cmd.APIKeyID, cmd.RequestID)
	if previous := r.commands[key]; previous != nil {
		if previous.RequestFingerprint != cmd.RequestFingerprint {
			return nil, ErrUsageBillingRequestConflict
		}
		return &UsageBillingApplyResult{Applied: false}, nil
	}
	r.commands[key] = cmd
	r.charged += cmd.BalanceCost
	return &UsageBillingApplyResult{Applied: true}, nil
}

func TestProviderVideoPerRequestRecordUsageDeduplicatesPollAndDownload(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		t.Run(platform, func(t *testing.T) {
			logs := &openAIRecordUsageLogRepoStub{inserted: true}
			ledger := &providerVideoBillingRepo{commands: make(map[string]*UsageBillingCommand)}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, ledger, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.resolver = NewModelPricingResolver(nil, svc.billingService)
			price := 0.5
			group := &Group{ID: 1, Platform: platform, RateMultiplier: 9, VideoRateIndependent: true, VideoRateMultiplier: 0.4,
				ModelPricing: []ChannelModelPricing{{Models: []string{"video"}, BillingMode: BillingModePerRequest, PerRequestPrice: &price}}}
			user := &User{ID: 1}
			key := &APIKey{ID: 2, User: user, GroupID: &group.ID, Group: group}
			taskID := platform + ":task"
			for i, endpoint := range []string{"/native/tasks/task", "/v1/videos/id", "/v1/videos/id/content"} {
				// Simulate a lost Redis claim: every endpoint reaches RecordUsage.
				// All still submit the same durable task key and fingerprint.
				result := &OpenAIForwardResult{ResponseID: taskID, Model: "video", BillingModel: "video", VideoCount: 1, VideoDurationSeconds: 30}
				if platform == PlatformSeedance && i > 0 {
					result.Usage.OutputTokens = 12345
				}
				err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: key, User: user,
					Account: &Account{ID: 3, Platform: platform, Type: AccountTypeAPIKey}, InboundEndpoint: endpoint,
					RequestPayloadHash: HashUsageRequestPayload([]byte(taskID))})
				require.NoError(t, err)
				require.Equal(t, StableGrokVideoBillingRequestID(taskID), logs.lastLog.RequestID)
				require.InDelta(t, 0.5, logs.lastLog.TotalCost, 1e-12)
				require.InDelta(t, 0.2, logs.lastLog.ActualCost, 1e-12)
				require.InDelta(t, 0.4, logs.lastLog.RateMultiplier, 1e-12)
				require.Equal(t, string(BillingModePerRequest), *logs.lastLog.BillingMode)
				require.Equal(t, result.Usage.OutputTokens, logs.lastLog.OutputTokens)
			}
			require.Len(t, ledger.commands, 1)
			require.InDelta(t, 0.2, ledger.charged, 1e-12)
		})
	}
}

func TestProviderVideoReservationPreservesGrokLegacyTariffs(t *testing.T) {
	for _, rate := range []float64{0, 1} {
		secondPrice, requestPrice := 1.0, 0.1
		group := &Group{ID: 1, Platform: PlatformGrok, RateMultiplier: 1,
			VideoRateIndependent: true, VideoRateMultiplier: rate, VideoPrice720P: &secondPrice}
		key := &APIKey{User: &User{ID: 1}, Group: group, GroupID: &group.ID}
		cache := newEmptyChannelCache()
		cache.loadedAt = time.Now()
		cache.groupPlatform[group.ID] = PlatformGrok
		cache.channelByGroupID[group.ID] = &Channel{ID: 1, Status: StatusActive}
		channels := &ChannelService{}
		billing := &BillingService{}
		for _, channelPricing := range []bool{false, true} {
			if channelPricing {
				cache.pricingByGroupModel[channelModelKey{groupID: group.ID, platform: PlatformGrok, model: "grok-imagine-video"}] = &ChannelModelPricing{BillingMode: BillingModePerRequest, PerRequestPrice: &requestPrice}
			}
			channels.cache.Store(cache)
			deps := inflightEstimateDeps{billing: billing, resolver: NewModelPricingResolver(channels, billing)}
			estimate, priced := deps.estimate(context.Background(), key, InflightEstimateRequest{Model: "grok-imagine-video", Kind: InflightEstimateVideo, VideoDurationSeconds: 15, VideoResolution: "720p"})
			require.True(t, priced)
			require.InDelta(t, 15*rate, estimate, 1e-12)
		}
	}
}
