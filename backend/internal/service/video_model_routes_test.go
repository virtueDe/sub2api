//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func resolutionVideoAccount(platform string) *Account {
	return &Account{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "test-key", VideoModelRoutesCredential: map[string]VideoModelRoute{
			"sd-video": {DefaultResolution: "720p", Resolutions: map[string]string{"480p": "sd-480p", "720p": "sd-720p", "1080p": "sd-1080p"}},
		}}}
}

func TestVideoModelRoutesCatalogAndAdmission(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		t.Run(platform, func(t *testing.T) {
			a := resolutionVideoAccount(platform)
			a.Credentials["model_mapping"] = map[string]any{"sd-480p": "sd-480p", "sd-720p": "sd-720p", "other": "other-upstream"}
			require.NoError(t, ValidateVideoAccount(platform, a.Type, a.Credentials))
			require.Equal(t, map[string]string{"sd-video": "sd-video", "other": "other-upstream"}, a.GetModelMapping())
			require.True(t, a.IsModelSupported("sd-video"))
			require.False(t, a.IsModelSupported("sd-480p"))
			require.False(t, a.IsModelSupported("unknown"))
			require.True(t, explicitModelMappingClaims(*a, "sd-video"))
			groupID := int64(1)
			gateway := &GatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{*a}}}
			require.Equal(t, []string{"other", "sd-video"}, gateway.GetAvailableModels(context.Background(), &groupID, platform))
			for _, resolution := range []string{"", "480p", "720p", "1080p", "2160p"} {
				ctx := WithVideoRequestedResolution(context.Background(), resolution)
				want := resolution != "2160p"
				scheduler := &defaultOpenAIAccountScheduler{}
				ok, _ := scheduler.isAccountRequestCompatibleReason(ctx, a, OpenAIAccountScheduleRequest{RequestedModel: "sd-video"})
				require.Equal(t, want, ok)
				require.Equal(t, want, isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, a, platform, "sd-video", false, OpenAIEndpointCapability(platform)))
			}
			// Lookup admission has no resolution context and does not require the old alias.
			require.True(t, videoResolutionCompatible(context.Background(), a, ""))
			a.Credentials["model_mapping"] = map[string]any{"*": "wildcard-target"}
			require.False(t, a.IsModelSupported("sd-720p"))
		})
	}
}

func TestVideoModelRoutesValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Account)
	}{
		{"bad schema", func(a *Account) { a.Credentials[VideoModelRoutesCredential] = "not an object" }},
		{"missing default", func(a *Account) {
			a.Credentials[VideoModelRoutesCredential] = map[string]VideoModelRoute{"sd": {Resolutions: map[string]string{"720p": "target"}}}
		}},
		{"invalid resolution", func(a *Account) {
			a.Credentials[VideoModelRoutesCredential] = map[string]VideoModelRoute{"sd": {DefaultResolution: "bad", Resolutions: map[string]string{"bad": "target"}}}
		}},
		{"conflicting mapping", func(a *Account) { a.Credentials["model_mapping"] = map[string]any{"sd-video": "other"} }},
		{"wrong platform", func(a *Account) { a.Platform = PlatformOpenAI }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := resolutionVideoAccount(PlatformKling)
			a.Credentials["kling_access_key"], a.Credentials["kling_secret_key"] = "ak", "sk"
			tc.mutate(a)
			require.Error(t, ValidateVideoAccount(a.Platform, a.Type, a.Credentials))
		})
	}
}

func TestVideoModelRoutesUnifiedForwardAndDefault(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		for _, resolution := range []string{"", "480p", "720p", "1080p"} {
			t.Run(platform+"/"+resolution, func(t *testing.T) {
				a := resolutionVideoAccount(platform)
				response := `{"id":"task","model":"sd-720p"}`
				if platform == PlatformWan {
					response = `{"output":{"task_id":"task"}}`
				}
				if platform == PlatformKling {
					response = `{"code":0,"data":{"task_id":"task"}}`
				}
				upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(response)}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				c, w := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
				c.Set(VideoUnifiedContextKey, true)
				body := `{"model":"sd-video","prompt":"waves","seconds":5`
				if resolution != "" {
					body += `,"resolution":"` + resolution + `"`
				}
				body += `}`
				endpoint, native, contentType, err := PrepareUnifiedVideoRequest(c, platform, []byte(body))
				require.NoError(t, err)
				var result *OpenAIForwardResult
				if platform == PlatformSeedance {
					result, err = svc.ForwardSeedance(context.Background(), c, a, endpoint, "", native)
				} else {
					result, err = svc.ForwardVideoProvider(context.Background(), c, a, endpoint, "", native, contentType)
				}
				require.NoError(t, err)
				if resolution == "" {
					resolution = "720p"
				}
				require.Equal(t, "sd-video", result.Model)
				require.Equal(t, "sd-video", result.BillingModel)
				require.Equal(t, "sd-"+resolution, result.UpstreamModel)
				require.Equal(t, resolution, result.VideoResolution)
				require.Equal(t, "sd-video", gjson.Get(w.Body.String(), "model").String())
				forwarded, err := io.ReadAll(upstream.request.Body)
				require.NoError(t, err)
				field := "model"
				if platform == PlatformKling {
					field = "model_name"
				}
				require.Equal(t, "sd-"+resolution, gjson.GetBytes(forwarded, field).String())
				if platform == PlatformSora {
					require.Equal(t, defaultVideoSize(resolution), gjson.GetBytes(forwarded, "size").String())
					require.False(t, gjson.GetBytes(forwarded, "resolution").Exists())
				}
				if platform == PlatformSeedance {
					require.Equal(t, "16:9", gjson.GetBytes(forwarded, "ratio").String())
				}
				if platform == PlatformKling {
					require.Equal(t, "16:9", gjson.GetBytes(forwarded, "aspect_ratio").String())
				}
			})
		}
	}
}

func TestVideoModelRoutesNativeAndMultipart(t *testing.T) {
	for _, tc := range []struct {
		platform string
		endpoint GrokMediaEndpoint
		body     string
	}{
		{PlatformSeedance, SeedanceEndpointCreate, `{"model":"sd-video","resolution":"1080p","content":[{"type":"text","text":"waves"}]}`},
		{PlatformWan, "wan_create", `{"model":"sd-video","input":{"prompt":"waves"},"parameters":{"resolution":"1080P"}}`},
		{PlatformKling, "kling_text_create", `{"model_name":"sd-video","resolution":"1080p","prompt":"waves"}`},
		{PlatformSora, "sora_create", `{"model":"sd-video","size":"1080x1920","prompt":"waves"}`},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			c, _ := grokMediaContentTestContext(http.MethodPost, "/native", nil)
			info := GrokMediaRequestInfo{Model: "sd-video"}
			_, body, _, model, err := prepareVideoAccountModel(c, resolutionVideoAccount(tc.platform), tc.endpoint, &info, []byte(tc.body), "application/json")
			require.NoError(t, err)
			require.Equal(t, "sd-1080p", model)
			require.Equal(t, "1080p", info.Resolution)
			if tc.platform == PlatformSora {
				require.Equal(t, "1080x1920", gjson.GetBytes(body, "size").String())
			}
		})
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	require.NoError(t, w.WriteField("model", "sd-video"))
	require.NoError(t, w.WriteField("resolution", "1080p"))
	part, err := w.CreateFormFile("input_reference", "frame.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("image-content"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	endpoint, body, contentType, err := PrepareUnifiedVideoRequest(c, PlatformSora, buf.Bytes())
	require.NoError(t, err)
	info, err := ParseVideoProviderRoutingRequest(endpoint, contentType, body)
	require.NoError(t, err)
	_, body, contentType, model, err := prepareVideoAccountModel(c, resolutionVideoAccount(PlatformSora), endpoint, &info, body, contentType)
	require.NoError(t, err)
	require.Equal(t, "sd-1080p", model)
	parsed, err := ParseVideoProviderRequest(endpoint, contentType, body)
	require.NoError(t, err)
	require.Equal(t, "sd-1080p", parsed.Model)
	require.Equal(t, "1080p", parsed.Resolution)
	require.Equal(t, []byte("image-content"), parsed.Uploads[0].Data)
	fields, err := videoRequestFields(body, contentType)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(fields, "resolution").Exists())
}

func TestVideoModelRoutesPreserveMetadataSize(t *testing.T) {
	a := resolutionVideoAccount(PlatformWan)
	c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	endpoint, body, contentType, err := PrepareUnifiedVideoRequest(c, PlatformWan, []byte(`{"model":"sd-video","prompt":"waves","metadata":{"parameters":{"size":"1080*1920"}}}`))
	require.NoError(t, err)
	info, err := ParseVideoProviderRoutingRequest(endpoint, contentType, body)
	require.NoError(t, err)
	_, body, _, upstream, err := prepareVideoAccountModel(c, a, endpoint, &info, body, contentType)
	require.NoError(t, err)
	require.Equal(t, "sd-1080p", upstream)
	require.Equal(t, "1080*1920", gjson.GetBytes(body, "parameters.size").String())
}

func TestVideoModelRoutesRejectUnsupportedAndConflictingResolution(t *testing.T) {
	c, _ := grokMediaContentTestContext(http.MethodPost, "/v1/videos", nil)
	for _, body := range []string{`{"model":"sd-video","resolution":"1080p","size":"1280x720"}`, `{"model":"sd-video","resolution":"bogus"}`, `{"model":"sd-video","size":"invalid"}`} {
		_, _, _, err := PrepareUnifiedVideoRequest(c, PlatformWan, []byte(body))
		require.Error(t, err)
	}
	a := resolutionVideoAccount(PlatformWan)
	_, _, _, err := a.ResolveVideoModel("sd-video", "2160p")
	require.Error(t, err)
	_, _, _, err = a.ResolveVideoModel("sd-720p", "720p")
	require.Error(t, err)
	groupID := int64(1)
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{*a}}}
	require.Error(t, svc.VideoResolutionRoutingError(WithVideoRequestedResolution(context.Background(), "2160p"), &groupID, PlatformWan, "sd-video"))
	require.NoError(t, svc.VideoResolutionRoutingError(WithVideoRequestedResolution(context.Background(), "720p"), &groupID, PlatformWan, "sd-video"))
}

func TestVideoModelRoutesPendingSurvivesMappingChanges(t *testing.T) {
	a := resolutionVideoAccount(PlatformSeedance)
	a.Credentials[VideoModelRoutesCredential] = map[string]VideoModelRoute{}
	upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task","model":"provider-renamed-model","status":"succeeded","usage":{"completion_tokens":99}}`)}
	c, w := grokMediaContentTestContext(http.MethodGet, "/tasks/task", nil)
	c.Set(VideoPendingContextKey, &GrokVideoPendingBilling{Model: "sd-video", BillingModel: "sd-video", OriginalModel: "public-video", UpstreamModel: "sd-1080p", VideoResolution: "1080p"})
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	result, err := svc.ForwardSeedance(context.Background(), c, a, SeedanceEndpointStatus, "seedance:task", nil)
	require.NoError(t, err)
	require.Equal(t, "sd-1080p", result.UpstreamModel)
	require.Equal(t, "sd-video", result.BillingModel)
	require.Equal(t, "1080p", result.VideoResolution)
	require.Equal(t, "public-video", gjson.Get(w.Body.String(), "model").String())
}

func TestVideoModelRoutesPerRequestResolutionPricing(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		defaultPrice, low, medium, high := 9.0, 0.0, 0.5, 1.0
		group := &Group{ID: 1, Platform: platform, ModelPricing: []ChannelModelPricing{{Models: []string{"sd-video"}, BillingMode: BillingModePerRequest, PerRequestPrice: &defaultPrice,
			Intervals: []PricingInterval{{TierLabel: "480p", PerRequestPrice: &low}, {TierLabel: "720P", PerRequestPrice: &medium}, {TierLabel: "1080p", PerRequestPrice: &high}}}}}
		billing := &BillingService{}
		svc := &OpenAIGatewayService{resolver: NewModelPricingResolver(nil, billing), billingService: billing}
		for resolution, price := range map[string]float64{"480p": low, "720p": medium, "1080p": high, "2160p": defaultPrice} {
			result := &OpenAIForwardResult{ResponseID: platform + ":task", Model: "sd-video", BillingModel: "sd-video", UpstreamModel: "private-" + resolution, VideoResolution: resolution, VideoCount: 1, VideoDurationSeconds: 30}
			cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), result, &APIKey{Group: group, GroupID: &group.ID}, []string{"sd-video"}, 9, 8, 1, 7, UsageTokens{OutputTokens: 500000}, "", nil, false, time.Now())
			require.NoError(t, err)
			require.InDelta(t, price, cost.ActualCost, 1e-12, platform+"/"+resolution)
		}
	}
}

func TestVideoModelRoutesWanSizeConsistency(t *testing.T) {
	for _, model := range []string{"wan2.6-t2v", "wan2.6-i2v"} {
		a := resolutionVideoAccount(PlatformWan)
		a.Credentials[VideoModelRoutesCredential] = map[string]VideoModelRoute{"sd-video": {DefaultResolution: "1080p", Resolutions: map[string]string{"1080p": model}}}
		for _, size := range []string{"", "1080*1920"} {
			c, _ := grokMediaContentTestContext(http.MethodPost, "/native", nil)
			info := GrokMediaRequestInfo{Model: "sd-video"}
			body := `{"model":"sd-video","parameters":{}}`
			if size != "" {
				body = `{"model":"sd-video","parameters":{"size":"` + size + `"}}`
			}
			_, native, _, _, err := prepareVideoAccountModel(c, a, "wan_create", &info, []byte(body), "application/json")
			require.NoError(t, err)
			if model == "wan2.6-t2v" {
				want := "1920*1080"
				if size != "" {
					want = size
				}
				require.Equal(t, want, gjson.GetBytes(native, "parameters.size").String())
				require.False(t, gjson.GetBytes(native, "parameters.resolution").Exists())
			} else {
				require.Equal(t, "1080P", gjson.GetBytes(native, "parameters.resolution").String())
				require.False(t, gjson.GetBytes(native, "parameters.size").Exists())
			}
		}
		// Common fields override native extensions without leaving conflicting fields.
		_, native, _, err := ConvertUnifiedVideoRequest(PlatformWan, "application/json", []byte(`{"model":"`+model+`","resolution":"1080p","metadata":{"parameters":{"size":"1280*720","resolution":"720P"}}}`))
		require.NoError(t, err)
		info, err := ParseVideoProviderRequest("wan_create", "application/json", native)
		require.NoError(t, err)
		require.Equal(t, "1080p", info.Resolution)
		require.NotEqual(t, gjson.GetBytes(native, "parameters.size").Exists(), gjson.GetBytes(native, "parameters.resolution").Exists())
	}
}

func TestVideoModelRoutesChannelRestrictionUsesTarget(t *testing.T) {
	channel := newTestChannelService(makeStandardRepo(Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{10}, RestrictModels: true, BillingModelSource: BillingModelSourceUpstream,
		ModelPricing: []ChannelModelPricing{{Platform: PlatformWan, Models: []string{"sd-1080p"}}}}, map[int64]string{10: PlatformWan}))
	svc := &OpenAIGatewayService{channelService: channel}
	a := resolutionVideoAccount(PlatformWan)
	require.False(t, svc.isUpstreamModelRestrictedByChannel(WithVideoRequestedResolution(context.Background(), "1080p"), 10, a, "sd-video", false))
	require.True(t, svc.isUpstreamModelRestrictedByChannel(WithVideoRequestedResolution(context.Background(), "720p"), 10, a, "sd-video", false))
	require.True(t, svc.isUpstreamModelRestrictedByChannel(WithVideoRequestedResolution(context.Background(), ""), 10, a, "sd-video", false))
}

func TestVideoModelRoutesBulkMappingConflictRejectedBeforeWrite(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{1: resolutionVideoAccount(PlatformWan)}}
	_, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1}, Credentials: map[string]any{"model_mapping": map[string]any{"sd-video": "conflicting-target"}}})
	require.ErrorContains(t, err, "conflicts")
	require.Empty(t, repo.bulkUpdates)
}

func TestVideoModelRoutesAccountTestUsesPublicModelDefault(t *testing.T) {
	for _, platform := range []string{PlatformSeedance, PlatformWan, PlatformKling, PlatformSora} {
		a := resolutionVideoAccount(platform)
		a.Credentials[VideoModelRoutesCredential] = map[string]VideoModelRoute{"sd-video": {DefaultResolution: "1080p", Resolutions: map[string]string{"1080p": "private-1080p"}}}
		// Stop after submission: an empty upstream reply avoids polling or a real job.
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{}`)}
		svc := &AccountTestService{httpUpstream: upstream}
		c, _ := grokMediaContentTestContext(http.MethodPost, "/test", nil)
		require.Error(t, svc.testVideoProviderAccount(c, a, "", "waves"))
		require.NotNil(t, upstream.request)
		body, err := io.ReadAll(upstream.request.Body)
		require.NoError(t, err)
		field := "model"
		if platform == PlatformKling {
			field = "model_name"
		}
		require.Equal(t, "private-1080p", gjson.GetBytes(body, field).String())
		if platform == PlatformSora {
			require.Equal(t, "1920x1080", gjson.GetBytes(body, "size").String())
		}
	}
}

func TestVideoRequestTierPricePrefersExactLabel(t *testing.T) {
	alias, exact := 0.1, 0.5
	pricing := &ResolvedPricing{RequestTiers: []PricingInterval{{TierLabel: "HD", PerRequestPrice: &alias}, {TierLabel: "720p", PerRequestPrice: &exact}}}
	price, found := lookupRequestTierPrice(pricing, "720p")
	require.True(t, found)
	require.Equal(t, exact, price)
}
