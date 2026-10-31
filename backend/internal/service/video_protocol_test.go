//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUnifiedVideoProtocolMappings(t *testing.T) {
	for _, tc := range []struct {
		platform string
		endpoint GrokMediaEndpoint
		field    string
	}{
		{PlatformSeedance, SeedanceEndpointCreate, "content.0.image_url.url"},
		{PlatformWan, "wan_create", "input.img_url"},
		{PlatformKling, "kling_image_create", "image"},
		{PlatformSora, "sora_create", "input_reference"},
	} {
		endpoint, body, _, err := ConvertUnifiedVideoRequest(tc.platform, "application/json", []byte(`{"model":"public-video","prompt":"waves","seconds":8,"size":"1280x720","input_reference":"https://example.com/first.png","metadata":{"model":"cannot-override","model_name":"cannot-override"}}`))
		require.NoError(t, err)
		require.Equal(t, tc.endpoint, endpoint)
		require.Equal(t, "https://example.com/first.png", gjson.GetBytes(body, tc.field).String())
		field := "model"
		if tc.platform == PlatformKling {
			field = "model_name"
		}
		require.Equal(t, "public-video", gjson.GetBytes(body, field).String())
	}
	for _, bad := range []string{"video_", PublicVideoTaskID("wan:../secret"), PublicVideoTaskID("unknown:id"), "arbitrary"} {
		_, _, err := ParsePublicVideoTaskID(bad)
		require.Error(t, err)
	}
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling} {
		_, _, _, err := ConvertUnifiedVideoRequest(platform, "application/json", []byte(`{"model":"video","seconds":1.5}`))
		require.Error(t, err)
	}
}

func TestIndependentVideoAccountCapabilitiesAndSecrets(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		a := &Account{Platform: platform, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
		require.True(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapability(platform)))
		require.False(t, a.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityChatCompletions))
		require.NoError(t, ValidateVideoAccount(platform, a.Type, a.Credentials))
		require.Error(t, ValidateVideoAccount(platform, AccountTypeOAuth, a.Credentials))
	}
	a := &Account{Credentials: map[string]any{"kling_access_key": "ak", "kling_secret_key": "sk"}}
	require.NoError(t, ValidateVideoAccount(PlatformKling, AccountTypeAPIKey, a.Credentials))
	_, err := videoProviderToken(a, PlatformKling, time.Now())
	require.NoError(t, err)
	require.True(t, IsSensitiveCredentialKey("kling_secret_key"))
	require.True(t, IsSensitiveCredentialKey("kling_access_key"))
	merged := MergePreservingSensitiveCreds(a.Credentials, map[string]any{"base_url": "https://example.com"})
	require.Equal(t, "sk", merged["kling_secret_key"])
}

func TestProviderVideoBillingPreservesLongDuration(t *testing.T) {
	price := 0.1
	group := &Group{ID: 1, Platform: PlatformWan, ModelPricing: []ChannelModelPricing{{Platform: PlatformWan, Models: []string{"wan-video"}, BillingMode: BillingModeVideo, PerRequestPrice: &price}}}
	billing := &BillingService{}
	s := &OpenAIGatewayService{resolver: NewModelPricingResolver(nil, billing), billingService: billing}
	result := &OpenAIForwardResult{ResponseID: "wan:task", Model: "wan-video", VideoCount: 1, VideoDurationSeconds: 30, VideoResolution: "1080p"}
	cost, err := s.calculateOpenAIRecordUsageCost(context.Background(), result, &APIKey{Group: group, GroupID: &group.ID}, []string{"wan-video"}, 1, 1, 1, 1, UsageTokens{}, "", nil, false, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 3.0, cost.TotalCost, 1e-9)
	require.Equal(t, 30, videoUsageDuration(result))
	require.Equal(t, "1024p", videoTaskResolution("sora:task", "1024p"))
	require.Equal(t, "480p", videoTaskResolution("grok-task", "1024p"))
	deps := inflightEstimateDeps{billing: billing, resolver: s.resolver}
	estimate, priced := deps.estimateOne(context.Background(), &APIKey{Group: group, GroupID: &group.ID}, "wan-video", InflightEstimateRequest{Kind: InflightEstimateVideo, VideoDurationSeconds: 30, VideoResolution: "1080p"}, 1, 1, 1)
	require.True(t, priced)
	require.InDelta(t, 3.0, estimate, 1e-9)
	group.ModelPricing = nil
	_, err = s.calculateOpenAIRecordUsageCost(context.Background(), result, &APIKey{Group: group, GroupID: &group.ID}, []string{"wan-video"}, 1, 1, 1, 1, UsageTokens{}, "", nil, false, time.Now())
	require.Error(t, err)
}
