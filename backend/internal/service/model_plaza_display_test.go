//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type plazaDisplayAccountsRepo struct {
	AccountRepository
	groups    map[int64][]Account
	err       error
	platforms []string
}

func (r *plazaDisplayAccountsRepo) ListModelAvailabilityCandidates(_ context.Context, groupID *int64, platforms []string, _ bool) ([]Account, error) {
	r.platforms = append([]string(nil), platforms...)
	return r.groups[*groupID], r.err
}

func plazaDisplayAccount(platform string, names ...string) Account {
	mapping := make(map[string]any, len(names))
	for _, name := range names {
		mapping[name] = name
	}
	return Account{ID: 1, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"model_mapping": mapping}}
}

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
	groups := []Group{
		{ID: 21, Platform: PlatformOpenAI, ModelPricing: []ChannelModelPricing{{Models: []string{"custom-model", "CUSTOM-MODEL", "custom-*", ""}, InputPrice: testPtrFloat64(2e-6)}}},
		{ID: 22, Platform: PlatformOpenAI},
		{ID: 23, Platform: PlatformOpenAI},
	}
	svc := newPlazaService([]Channel{plazaPricedChannel(1, "channel", []int64{21, 22, 23}, PlatformOpenAI, "from-channel")}, groups, nil)
	accounts := &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {plazaDisplayAccount(PlatformOpenAI, "custom-model")}}}
	svc.accountRepo = accounts
	cfg := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{
		{GroupID: 21, Models: []ModelPlazaDisplayModel{{Name: "legacy-model", Platform: PlatformOpenAI}}},
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
	require.Len(t, byID[21].Models, 1, "account models replace both legacy selections and channel models")
	require.Equal(t, 2e-6, *byID[21].Models[0].Pricing.InputPrice)
	require.Empty(t, byID[22].Models, "an empty group catalogue never falls back to linked channels")
	require.NotContains(t, byID, int64(23))
	out, err = svc.ListDisplayGroups(context.Background(), ModelPlazaDisplayConfig{Mode: "manual"})
	require.NoError(t, err)
	require.Empty(t, out, "empty manual selection must never mean all groups")
	out, err = svc.ListDisplayGroups(context.Background(), ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}, {GroupID: 999}}})
	require.NoError(t, err)
	require.Len(t, out, 1, "deleted/inactive groups are not returned")
	require.Equal(t, "custom-model", out[0].Models[0].Name)

	groups[0].ModelPricing = []ChannelModelPricing{{Models: []string{"new-model"}, InputPrice: testPtrFloat64(3e-6)}}
	accounts.groups[21] = []Account{plazaDisplayAccount(PlatformOpenAI, "new-model")}
	out, err = svc.ListDisplayGroups(context.Background(), cfg)
	require.NoError(t, err)
	for _, group := range out {
		if group.ID == 21 {
			require.Equal(t, "new-model", group.Models[0].Name, "group edits are reflected without resaving display settings")
			require.Equal(t, 3e-6, *group.Models[0].Pricing.InputPrice)
		}
	}
}

func TestModelPlazaDisplayVisibilityBeforeModelDiscovery(t *testing.T) {
	groups := []Group{{ID: 21, Platform: PlatformOpenAI}, {ID: 22, Platform: PlatformOpenAI, IsExclusive: true}}
	svc := newPlazaService(nil, groups, nil)
	broken := plazaDisplayAccount(PlatformOpenAI)
	broken.ID = 22
	svc.accountRepo = &plazaDisplayAccountsRepo{groups: map[int64][]Account{
		21: {plazaDisplayAccount(PlatformOpenAI, "public-model")},
		22: {broken},
	}}
	discoveries := 0
	svc.fetchAccountModels = func(context.Context, *Account) ([]string, error) {
		discoveries++
		return nil, errors.New("upstream unavailable")
	}
	cfg := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}, {GroupID: 22}}}
	visible, err := svc.ListVisibleDisplayGroups(context.Background(), cfg, func(_ int64, exclusive bool) bool { return !exclusive })
	require.NoError(t, err)
	require.Len(t, visible, 1)
	require.Equal(t, int64(21), visible[0].ID)
	require.Zero(t, discoveries, "inaccessible groups must not call their upstreams")
	_, err = svc.ListDisplayGroups(context.Background(), cfg)
	require.Error(t, err, "visible upstream failures must still be reported")
	require.Equal(t, 1, discoveries)
}

func TestModelPlazaDisplayManualPricingMatchesBilling(t *testing.T) {
	ctx := context.Background()
	cfg := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}}}
	for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest, BillingModeVideo, BillingModeToken} {
		t.Run(string(mode), func(t *testing.T) {
			g := Group{ID: 21, Platform: PlatformOpenAI, ImagePrice1K: testPtrFloat64(10), RateMultiplier: 1,
				ModelPricing: []ChannelModelPricing{{Models: []string{"priced-model"}, BillingMode: mode, PerRequestPrice: testPtrFloat64(0.03), InputPrice: testPtrFloat64(2e-6), OutputPrice: testPtrFloat64(0)}},
			}
			channel := plazaPricedChannel(1, "channel", []int64{21}, PlatformOpenAI, "priced-model")
			channel.ModelPricing[0].InputPrice = testPtrFloat64(99)
			channel.ModelPricing[0].PerRequestPrice = testPtrFloat64(99)
			svc := newPlazaServiceWithBilling([]Channel{channel}, []Group{g}, map[int64]string{21: PlatformOpenAI}, nil)
			svc.accountRepo = &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {plazaDisplayAccount(PlatformOpenAI, "priced-model")}}}
			out, err := svc.ListDisplayGroups(ctx, cfg)
			require.NoError(t, err)
			require.Len(t, out, 1)
			require.Len(t, out[0].Models, 1)
			p := out[0].Models[0].Pricing
			require.Equal(t, mode, p.BillingMode)
			if mode != BillingModeToken {
				require.Equal(t, 0.03, *p.PerRequestPrice)
			} else {
				require.Equal(t, 2e-6, *p.InputPrice)
				require.Equal(t, float64(0), *p.OutputPrice)
			}
			require.Nil(t, out[0].Models[0].TimePricing)
		})
	}
}

func TestModelPlazaDisplayManualDoesNotReadChannels(t *testing.T) {
	groups := []Group{
		{ID: 21, Platform: PlatformOpenAI},
		{ID: 22, Platform: PlatformOpenAI},
	}
	svc := newPlazaServiceWithBilling(nil, groups, nil, nil)
	svc.accountRepo = &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {plazaDisplayAccount(PlatformOpenAI, "gpt-5.4", "gpt-image-2.6-sunburst", "unknown-model")}}}
	channelReads := 0
	svc.channelRepo.(*mockChannelRepository).listAllFn = func(context.Context) ([]Channel, error) {
		channelReads++
		return nil, errors.New("channels unavailable")
	}
	out, err := svc.ListDisplayGroups(context.Background(), ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}, {GroupID: 22}}})
	require.NoError(t, err)
	require.Len(t, out, 2)
	models := plazaModelsByName(out[0].Models)
	require.Len(t, models, 3)
	require.NotNil(t, models["gpt-5.4"].Pricing)
	require.Nil(t, models["unknown-model"].Pricing)
	require.Equal(t, BillingModeImage, models["gpt-image-2.6-sunburst"].Pricing.BillingMode)
	require.Zero(t, channelReads, "neither catalogue enumeration nor pricing resolution should read channels")
	_, err = svc.ListGroups(context.Background())
	require.ErrorContains(t, err, "channels unavailable")
	require.Equal(t, 1, channelReads)
}

func TestModelPlazaDisplayModesUseSeparateCatalogues(t *testing.T) {
	groups := []Group{
		{ID: 21, Platform: PlatformOpenAI},
		{ID: 22, Platform: PlatformComposite, ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-5.4", "gemini-2.5-pro"}}}},
	}
	svc := newPlazaService([]Channel{plazaPricedChannel(1, "channel", []int64{21}, PlatformOpenAI, "channel-model")}, groups, nil)
	svc.accountRepo = &plazaDisplayAccountsRepo{groups: map[int64][]Account{22: {plazaDisplayAccount(PlatformOpenAI, "gpt-5.4"), plazaDisplayAccount(PlatformGemini, "gemini-2.5-pro")}}}
	manual := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 22}}}
	out, err := svc.ListDisplayGroups(context.Background(), manual)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, int64(22), out[0].ID)
	require.Len(t, out[0].Models, 2)
	require.Equal(t, PlatformGemini, out[0].Models[0].Platform)
	require.Equal(t, "gemini-2.5-pro", out[0].Models[0].Name)
	require.Equal(t, "gpt-5.4", out[0].Models[1].Name)
	manual.Mode = "automatic"
	out, err = svc.ListDisplayGroups(context.Background(), manual)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, int64(21), out[0].ID)
	require.Equal(t, "channel-model", out[0].Models[0].Name)
}

func TestModelPlazaDisplayAccountModelsWithoutPricing(t *testing.T) {
	g := Group{ID: 21, Platform: PlatformOpenAI}
	account := plazaDisplayAccount(PlatformOpenAI)
	repo := &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {account}}}
	svc := newPlazaService(nil, []Group{g}, nil)
	svc.accountRepo = repo
	fetches := 0
	svc.fetchAccountModels = func(_ context.Context, account *Account) ([]string, error) {
		fetches++
		if account.ID != 1 {
			return nil, errors.New("unexpected account for discovery")
		}
		return []string{"upstream-image-model", "upstream-chat-model"}, nil
	}
	cfg := ModelPlazaDisplayConfig{Mode: "manual", Groups: []ModelPlazaDisplayGroup{{GroupID: 21}}}
	for i := 0; i < 2; i++ {
		out, err := svc.ListDisplayGroups(context.Background(), cfg)
		require.NoError(t, err)
		require.Len(t, out[0].Models, 2)
		require.Equal(t, "upstream-chat-model", out[0].Models[0].Name)
		require.Nil(t, out[0].Models[0].Pricing, "missing pricing must not hide discovered models")
	}
	require.Equal(t, 1, fetches, "preview/public reads share the discovery cache")
	repo.groups[21][0].UpdatedAt = time.Now()
	_, err := svc.ListDisplayGroups(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 2, fetches, "account changes invalidate cached discovery")
}

func TestModelPlazaDisplayAccountFiltering(t *testing.T) {
	g := Group{ID: 21, Platform: PlatformComposite, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"shared", "gpt-5.4"}}}
	inactive := plazaDisplayAccount(PlatformOpenAI, "inactive-model")
	inactive.Status = "inactive"
	unschedulable := plazaDisplayAccount(PlatformOpenAI, "disabled-model")
	unschedulable.Schedulable = false
	svc := newPlazaService(nil, []Group{g}, nil)
	svc.accountRepo = &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {
		plazaDisplayAccount(PlatformOpenAI, "shared", "private-model"),
		plazaDisplayAccount(PlatformGemini, "shared"),
		inactive, unschedulable,
		{ID: 5, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
	}}}
	models, err := svc.groupDisplayModels(context.Background(), &g)
	require.NoError(t, err)
	require.Len(t, models, 3)
	require.Equal(t, "gpt-5.4", models[0].Name, "OAuth accounts without mappings contribute default models")
	require.Equal(t, "shared", models[1].Name)
	require.Equal(t, PlatformGemini, models[1].Platform)
	require.Equal(t, PlatformOpenAI, models[2].Platform, "same names on different platforms remain distinct")
}

func TestModelPlazaDisplayWildcardModelsAndErrors(t *testing.T) {
	g := Group{ID: 21, Platform: PlatformOpenAI}
	svc := newPlazaService(nil, []Group{g}, nil)
	repo := &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {plazaDisplayAccount(PlatformOpenAI, "gpt-*")}}}
	svc.accountRepo = repo
	svc.fetchAccountModels = func(context.Context, *Account) ([]string, error) {
		return []string{"gpt-5.4", "gemini-2.5-pro"}, nil
	}
	models, err := svc.groupDisplayModels(context.Background(), &g)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "gpt-5.4", models[0].Name)
	svc.accountModelsCache.Flush()
	svc.fetchAccountModels = func(context.Context, *Account) ([]string, error) { return nil, errors.New("private upstream detail") }
	models, err = svc.groupDisplayModels(context.Background(), &g)
	require.Nil(t, models)
	require.ErrorContains(t, err, "Unable to load account models")
	require.NotContains(t, err.Error(), "private upstream detail")
	repo.err = errors.New("account database unavailable")
	_, err = svc.groupDisplayModels(context.Background(), &g)
	require.ErrorIs(t, err, repo.err)
}

func TestModelPlazaDisplayPlatformRoutingRules(t *testing.T) {
	svc := newPlazaService(nil, nil, nil)
	svc.fetchAccountModels = func(context.Context, *Account) ([]string, error) {
		return nil, errors.New("native/static models must not query an unsupported upstream endpoint")
	}
	t.Run("typesafe", func(t *testing.T) {
		account := plazaDisplayAccount(PlatformTypeSafe)
		models, err := svc.accountDisplayModelNames(context.Background(), &account, &Group{Platform: PlatformTypeSafe})
		require.NoError(t, err)
		require.Equal(t, []string{"jev-latest"}, models)
	})
	t.Run("bedrock", func(t *testing.T) {
		account := plazaDisplayAccount(PlatformAnthropic)
		account.Type = AccountTypeBedrock
		models, err := svc.accountDisplayModelNames(context.Background(), &account, &Group{Platform: PlatformAnthropic})
		require.NoError(t, err)
		require.NotEmpty(t, models)
		require.NotContains(t, models, "claude-opus-5-5")
		for _, name := range models {
			_, supported := ResolveBedrockModelID(&account, name)
			require.True(t, supported, name)
		}
		account.Credentials = map[string]any{"model_mapping": map[string]any{"bad-alias": "invalid-bedrock-target"}}
		models, err = svc.accountDisplayModelNames(context.Background(), &account, &Group{Platform: PlatformAnthropic})
		require.NoError(t, err)
		require.Empty(t, models)
	})
	t.Run("anthropic normalized mappings", func(t *testing.T) {
		for _, accountType := range []string{AccountTypeOAuth, AccountTypeServiceAccount} {
			account := plazaDisplayAccount(PlatformAnthropic, "claude-sonnet-4-5")
			account.Type = accountType
			models, err := svc.accountDisplayModelNames(context.Background(), &account, &Group{Platform: PlatformAnthropic})
			require.NoError(t, err)
			require.Empty(t, models, "the request path normalizes before looking up the mapping")
			validName := "claude-sonnet-4-5-20250929"
			if accountType == AccountTypeServiceAccount {
				validName = "claude-sonnet-4-5@20250929"
			}
			account = plazaDisplayAccount(PlatformAnthropic, validName)
			account.Type = accountType
			models, err = svc.accountDisplayModelNames(context.Background(), &account, &Group{Platform: PlatformAnthropic})
			require.NoError(t, err)
			require.Equal(t, []string{validName}, models)
		}
	})
	t.Run("anthropic mixed scheduling", func(t *testing.T) {
		g := Group{ID: 21, Platform: PlatformAnthropic, ModelAllowlist: GroupModelAllowlist{Enabled: true, Models: []string{"claude-sonnet-4-6", "gemini-2.5-pro"}}}
		account := plazaDisplayAccount(PlatformAntigravity, "claude-sonnet-4-6", "gemini-2.5-pro")
		account.Extra = map[string]any{"mixed_scheduling": true}
		svc.accountRepo = &plazaDisplayAccountsRepo{groups: map[int64][]Account{21: {account}}}
		models, err := svc.groupDisplayModels(context.Background(), &g)
		require.NoError(t, err)
		require.Len(t, models, 1)
		require.Equal(t, "claude-sonnet-4-6", models[0].Name)
		require.Equal(t, PlatformAnthropic, models[0].Platform)
		require.Contains(t, svc.accountRepo.(*plazaDisplayAccountsRepo).platforms, PlatformAntigravity)
	})
}

func TestModelPlazaDisplayDiscoverySurvivesCallerCancellation(t *testing.T) {
	svc := newPlazaService(nil, nil, nil)
	account := plazaDisplayAccount(PlatformOpenAI)
	started, release := make(chan struct{}), make(chan struct{})
	svc.fetchAccountModels = func(ctx context.Context, _ *Account) ([]string, error) {
		close(started)
		<-release
		return []string{"discovered-model"}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstErr := make(chan error, 1)
	go func() { _, err := svc.cachedAccountDisplayModels(ctx, &account); firstErr <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	cancel()
	select {
	case err := <-firstErr:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller did not return")
	}
	close(release)
	models, err := svc.cachedAccountDisplayModels(context.Background(), &account)
	require.NoError(t, err)
	require.Equal(t, []string{"discovered-model"}, models)
}

func TestModelPlazaDisplayManualImagePricing(t *testing.T) {
	g := Group{ID: 21, Platform: PlatformOpenAI, ImagePrice1K: testPtrFloat64(0), ImagePrice2K: testPtrFloat64(1), RateMultiplier: 1}
	svc := newPlazaServiceWithBilling(nil, []Group{g}, nil, nil)
	model := PlazaModel{Name: "gpt-image-2.6-sunburst", Platform: PlatformOpenAI}
	svc.fillManualDisplayPricing(context.Background(), &model, &g)
	require.Equal(t, BillingModeImage, model.Pricing.BillingMode)
	require.Len(t, model.Pricing.Intervals, 3)
	for _, tier := range model.Pricing.Intervals {
		expected := svc.billingService.CalculateImageCost(model.Name, tier.TierLabel, 1, &ImagePriceConfig{Price1K: g.ImagePrice1K, Price2K: g.ImagePrice2K}, 1)
		require.Equal(t, expected.TotalCost, *tier.PerRequestPrice)
	}
	catalog := newStubPricingServiceFromJSON(t, `{"other-image":{"mode":"image_generation","output_cost_per_image":0.04}}`)
	svc = newPlazaServiceWithBilling(nil, []Group{g}, nil, catalog)
	model = PlazaModel{Name: "other-image", Platform: PlatformOpenAI}
	svc.fillManualDisplayPricing(context.Background(), &model, &Group{})
	require.NotNil(t, model.Pricing)
	require.Equal(t, BillingModeImage, model.Pricing.BillingMode)
	require.Equal(t, 0.04, *model.Pricing.PerRequestPrice)
}
