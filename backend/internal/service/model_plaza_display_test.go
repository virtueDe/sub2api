//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type plazaDisplaySettingsRepo struct {
	SettingRepository
	value string
	err   error
}

func (r *plazaDisplaySettingsRepo) GetValue(context.Context, string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}
func (r *plazaDisplaySettingsRepo) Set(_ context.Context, _ string, value string) error {
	r.value = value
	return nil
}

func TestModelPlazaDisplaySettings(t *testing.T) {
	ctx := context.Background()
	repo := &plazaDisplaySettingsRepo{}
	svc := NewSettingService(repo, nil)
	cfg, err := svc.GetModelPlazaDisplay(ctx)
	require.NoError(t, err)
	require.Equal(t, "automatic", cfg.Mode)
	require.Empty(t, cfg.Groups)
	require.NoError(t, svc.SetModelPlazaDisplay(ctx, ModelPlazaDisplayConfig{Mode: "manual"}))
	cfg, err = svc.GetModelPlazaDisplay(ctx)
	require.NoError(t, err)
	require.Equal(t, "manual", cfg.Mode)
	require.NotNil(t, cfg.Groups)
	previous := repo.value
	require.Error(t, svc.SetModelPlazaDisplay(ctx, ModelPlazaDisplayConfig{Mode: "invalid"}))
	require.Equal(t, previous, repo.value)
	repo.value = "not-json"
	_, err = svc.GetModelPlazaDisplay(ctx)
	require.Error(t, err, "malformed configuration must not expose all groups")
	repo.err = errors.New("offline")
	_, err = svc.GetModelPlazaDisplay(ctx)
	require.ErrorIs(t, err, repo.err)
}

func TestModelPlazaDisplayValidation(t *testing.T) {
	svc := newPlazaService(nil, []Group{{ID: 21, Platform: PlatformOpenAI}, {ID: 22, Platform: PlatformComposite}}, nil)
	automatic, err := svc.ValidateDisplayConfig(context.Background(), ModelPlazaDisplayConfig{Mode: "automatic", Groups: []ModelPlazaDisplayGroup{{GroupID: 999, Models: []ModelPlazaDisplayModel{{}}}}})
	require.NoError(t, err)
	require.Empty(t, automatic.Groups, "switching to automatic clears stale IDs and unfinished model rows")
	for _, cfg := range []ModelPlazaDisplayConfig{
		{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: -1}}},
		{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}, {GroupID: 21}}},
		{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 999}}},
		{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21, Models: []ModelPlazaDisplayModel{{Name: "gpt-*", Platform: PlatformOpenAI}}}}},
		{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21, Models: []ModelPlazaDisplayModel{{Name: "gpt-5", Platform: PlatformGemini}}}}},
		{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 22, Models: []ModelPlazaDisplayModel{{Name: "gpt-5", Platform: PlatformComposite}}}}},
	} {
		_, err := svc.ValidateDisplayConfig(context.Background(), cfg)
		require.Error(t, err, "%+v", cfg)
	}
	cfg, err := svc.ValidateDisplayConfig(context.Background(), ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 22, Models: []ModelPlazaDisplayModel{
		{Name: " gpt-5 ", Platform: " OpenAI "}, {Name: "gpt-5", Platform: PlatformOpenAI}, {Name: "gemini-2.5-pro", Platform: PlatformGemini},
	}}}})
	require.NoError(t, err)
	require.Len(t, cfg.Groups[0].Models, 2)
}

func TestModelPlazaDisplayManualGroupsAndModels(t *testing.T) {
	groups := []Group{{ID: 21, Platform: PlatformOpenAI}, {ID: 22, Platform: PlatformOpenAI}, {ID: 23, Platform: PlatformOpenAI}}
	svc := newPlazaService([]Channel{plazaPricedChannel(1, "channel", []int64{21, 23}, PlatformOpenAI, "from-channel")}, groups, nil)
	cfg := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{
		{GroupID: 21, Models: []ModelPlazaDisplayModel{{Name: "custom-model", Platform: PlatformOpenAI}}},
		{GroupID: 22},
	}}
	out, err := svc.ListDisplayGroups(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, out, 2)
	byID := map[int64]PlazaGroup{}
	for _, g := range out {
		byID[g.ID] = g
	}
	require.Equal(t, "custom-model", byID[21].Models[0].Name)
	require.Len(t, byID[21].Models, 1, "manual model list replaces the channel catalogue")
	require.Empty(t, byID[22].Models, "selected groups remain visible without a channel")
	require.NotContains(t, byID, int64(23))
	out, err = svc.ListDisplayGroups(context.Background(), ModelPlazaDisplayConfig{Mode: "manual"})
	require.NoError(t, err)
	require.Empty(t, out, "empty manual selection must never mean all groups")
	out, err = svc.ListDisplayGroups(context.Background(), ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}, {GroupID: 999}}})
	require.NoError(t, err)
	require.Len(t, out, 1, "deleted/inactive groups are not returned")
	require.Equal(t, "from-channel", out[0].Models[0].Name)
}

func TestModelPlazaDisplayManualPricingMatchesBilling(t *testing.T) {
	ctx := context.Background()
	g := Group{ID: 21, Platform: PlatformOpenAI, ImagePrice1K: testPtrFloat64(0), ImagePrice2K: testPtrFloat64(1), RateMultiplier: 1}
	svc := newPlazaServiceWithBilling(nil, []Group{g}, map[int64]string{21: PlatformOpenAI}, nil)
	cfg := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21, Models: []ModelPlazaDisplayModel{
		{Name: "gpt-image-2.6-sunburst", Platform: PlatformOpenAI}, {Name: "unknown-unpriced-model", Platform: PlatformOpenAI}, {Name: "gpt-5.4", Platform: PlatformOpenAI},
	}}}}
	out, err := svc.ListDisplayGroups(ctx, cfg)
	require.NoError(t, err)
	models := plazaModelsByName(out[0].Models)
	p := models["gpt-image-2.6-sunburst"].Pricing
	require.NotNil(t, p)
	require.Equal(t, BillingModeImage, p.BillingMode)
	require.Len(t, p.Intervals, 3)
	for _, tier := range p.Intervals {
		expected := svc.billingService.CalculateImageCost("gpt-image-2.6-sunburst", tier.TierLabel, 1, &ImagePriceConfig{Price1K: g.ImagePrice1K, Price2K: g.ImagePrice2K}, 1)
		require.Equal(t, expected.TotalCost, *tier.PerRequestPrice)
	}
	require.Nil(t, models["unknown-unpriced-model"].Pricing)
	require.NotNil(t, models["gpt-5.4"].Pricing)
	for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest, BillingModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			g.ModelPricing = []ChannelModelPricing{{Models: []string{"gpt-image-*"}, BillingMode: mode, PerRequestPrice: testPtrFloat64(0.03), InputPrice: testPtrFloat64(2e-6)}}
			svc := newPlazaServiceWithBilling(nil, []Group{g}, map[int64]string{21: PlatformOpenAI}, nil)
			out, err := svc.ListDisplayGroups(ctx, cfg)
			require.NoError(t, err)
			p := plazaModelsByName(out[0].Models)["gpt-image-2.6-sunburst"].Pricing
			require.Equal(t, mode, p.BillingMode)
			if mode != BillingModeToken {
				require.Equal(t, 0.03, *p.PerRequestPrice)
			}
		})
	}
}
