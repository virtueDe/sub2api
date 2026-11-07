package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const modelPlazaDisplaySettingKey = "model_plaza_display"

// Display selection never grants access or changes routing or billing.
type ModelPlazaDisplayConfig struct {
	Mode   string                   `json:"mode"`
	Groups []ModelPlazaDisplayGroup `json:"groups"`
}

type ModelPlazaDisplayGroup struct {
	GroupID int64                    `json:"group_id"`
	Models  []ModelPlazaDisplayModel `json:"models"`
}

type ModelPlazaDisplayModel struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func normalizeModelPlazaDisplay(cfg ModelPlazaDisplayConfig) (ModelPlazaDisplayConfig, error) {
	invalid := func(message string) (ModelPlazaDisplayConfig, error) {
		return ModelPlazaDisplayConfig{}, infraerrors.BadRequest("INVALID_MODEL_PLAZA_DISPLAY", message)
	}
	if cfg.Mode != "automatic" && cfg.Mode != "manual" {
		return invalid("mode must be automatic or manual")
	}
	// Automatic mode has no manual selection. Discard stale references so an
	// administrator can switch back even after selected groups are removed.
	if cfg.Mode == "automatic" {
		return ModelPlazaDisplayConfig{Mode: "automatic", Groups: []ModelPlazaDisplayGroup{}}, nil
	}
	if len(cfg.Groups) > 500 {
		return invalid("select at most 500 groups")
	}
	out := ModelPlazaDisplayConfig{Mode: cfg.Mode, Groups: []ModelPlazaDisplayGroup{}}
	seen := make(map[int64]bool)
	total := 0
	for _, entry := range cfg.Groups {
		if entry.GroupID <= 0 || seen[entry.GroupID] {
			return invalid("group IDs must be positive and unique")
		}
		seen[entry.GroupID] = true
		total += len(entry.Models)
		if len(entry.Models) > 200 || total > 5000 {
			return invalid("select at most 200 models per group and 5000 models in total")
		}
		item := ModelPlazaDisplayGroup{GroupID: entry.GroupID, Models: []ModelPlazaDisplayModel{}}
		models := make(map[ModelPlazaDisplayModel]bool)
		for _, model := range entry.Models {
			model.Name = strings.TrimSpace(model.Name)
			model.Platform = strings.ToLower(strings.TrimSpace(model.Platform))
			if model.Name == "" || len(model.Name) > 200 || strings.ContainsAny(model.Name, "*?") || strings.IndexFunc(model.Name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
				return invalid("use specific model names without wildcards or whitespace")
			}
			if !isConcreteRequestPlatform(model.Platform) {
				return invalid("each model requires a supported concrete platform")
			}
			if !models[model] {
				item.Models = append(item.Models, model)
				models[model] = true
			}
		}
		sort.Slice(item.Models, func(i, j int) bool {
			if item.Models[i].Platform != item.Models[j].Platform {
				return item.Models[i].Platform < item.Models[j].Platform
			}
			return item.Models[i].Name < item.Models[j].Name
		})
		out.Groups = append(out.Groups, item)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].GroupID < out.Groups[j].GroupID })
	return out, nil
}

func (s *SettingService) GetModelPlazaDisplay(ctx context.Context) (ModelPlazaDisplayConfig, error) {
	value, err := s.settingRepo.GetValue(ctx, modelPlazaDisplaySettingKey)
	if errors.Is(err, ErrSettingNotFound) {
		return ModelPlazaDisplayConfig{Mode: "automatic", Groups: []ModelPlazaDisplayGroup{}}, nil
	}
	if err != nil {
		return ModelPlazaDisplayConfig{}, fmt.Errorf("load model plaza display: %w", err)
	}
	var cfg ModelPlazaDisplayConfig
	if err := json.Unmarshal([]byte(value), &cfg); err != nil {
		return cfg, fmt.Errorf("decode model plaza display: %w", err)
	}
	return normalizeModelPlazaDisplay(cfg)
}

func (s *SettingService) SetModelPlazaDisplay(ctx context.Context, cfg ModelPlazaDisplayConfig) error {
	normalized, err := normalizeModelPlazaDisplay(cfg)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, modelPlazaDisplaySettingKey, string(encoded))
}

func (s *ModelPlazaService) ValidateDisplayConfig(ctx context.Context, cfg ModelPlazaDisplayConfig) (ModelPlazaDisplayConfig, error) {
	result, err := normalizeModelPlazaDisplay(cfg)
	if err != nil || len(result.Groups) == 0 {
		return result, err
	}
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return result, err
	}
	byID := make(map[int64]Group, len(groups))
	for _, g := range groups {
		byID[g.ID] = g
	}
	for _, entry := range result.Groups {
		g, ok := byID[entry.GroupID]
		if !ok {
			return result, infraerrors.BadRequest("INVALID_MODEL_PLAZA_GROUP", fmt.Sprintf("group %d is unavailable; remove it from the selection", entry.GroupID))
		}
		for _, model := range entry.Models {
			if g.Platform != PlatformComposite && model.Platform != g.Platform {
				return result, infraerrors.BadRequest("INVALID_MODEL_PLAZA_PLATFORM", fmt.Sprintf("model %s platform does not match group %d", model.Name, g.ID))
			}
		}
	}
	return result, nil
}

// ListDisplayGroupModels shares the manual catalogue with the administrator's
// preview. It never saves the discovered models into the display configuration.
func (s *ModelPlazaService) ListDisplayGroupModels(ctx context.Context, groupID int64) ([]ModelPlazaDisplayModel, error) {
	g, err := s.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if g.Status != StatusActive {
		return nil, ErrGroupNotFound
	}
	models, err := s.groupDisplayModels(ctx, g)
	if err != nil {
		return nil, err
	}
	out := make([]ModelPlazaDisplayModel, 0, len(models))
	for _, model := range models {
		out = append(out, ModelPlazaDisplayModel{Name: model.Name, Platform: model.Platform})
	}
	return out, nil
}

// Model availability is based on persistent account settings, so temporary
// rate limits do not make the plaza catalogue disappear.
func (s *ModelPlazaService) groupDisplayModels(ctx context.Context, g *Group) ([]PlazaModel, error) {
	if s.accountRepo == nil {
		return nil, errors.New("model plaza account repository is unavailable")
	}
	platforms := []string{g.Platform}
	if g.Platform == PlatformComposite {
		platforms = domain.ConcretePlatformIDs()
	} else if g.Platform == PlatformGemini || g.Platform == PlatformAnthropic {
		platforms = append(platforms, PlatformAntigravity)
	}
	accounts, err := s.accountRepo.ListModelAvailabilityCandidates(ctx, &g.ID, platforms, false)
	if err != nil {
		return nil, fmt.Errorf("list model plaza group accounts: %w", err)
	}
	models := make([]PlazaModel, 0)
	seen := make(map[ModelPlazaDisplayModel]bool)
	for i := range accounts {
		account := &accounts[i]
		if account.Status != StatusActive || !account.Schedulable || !isConcreteRequestPlatform(account.Platform) {
			continue
		}
		mixed := g.Platform != PlatformComposite && account.Platform != g.Platform
		if mixed && (!(g.Platform == PlatformGemini || g.Platform == PlatformAnthropic) || !account.IsMixedSchedulingEnabled()) {
			continue
		}
		names, err := s.accountDisplayModelNames(ctx, account, g)
		if err != nil {
			return nil, fmt.Errorf("load model plaza group %d models: %w", g.ID, err)
		}
		platform := account.Platform
		if mixed {
			platform = g.Platform
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			key := ModelPlazaDisplayModel{Name: strings.ToLower(name), Platform: platform}
			if name == "" || strings.ContainsAny(name, "*?") || seen[key] || !g.ModelAllowlist.Allows(name) {
				continue
			}
			if mixed && ((g.Platform == PlatformGemini && !mixedListingModelAllowed(g.Platform, name)) ||
				(g.Platform == PlatformAnthropic && !strings.HasPrefix(mapAntigravityModel(account, name), "claude-"))) {
				continue
			}
			seen[key] = true
			models = append(models, PlazaModel{Name: name, Platform: platform})
		}
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Name != models[j].Name {
			return models[i].Name < models[j].Name
		}
		return models[i].Platform < models[j].Platform
	})
	return models, nil
}

func (s *ModelPlazaService) accountDisplayModelNames(ctx context.Context, account *Account, g *Group) ([]string, error) {
	if account.IsTypeSafe() {
		return defaultModelsListCandidateIDs(PlatformTypeSafe), nil
	}
	mapping := account.GetModelMapping()
	if account.IsOpenAIPassthroughEnabled() {
		mapping = nil
	}
	names := make([]string, 0, len(mapping))
	needsCatalogue := len(mapping) == 0
	hasWildcard := false
	for name := range mapping {
		if strings.ContainsAny(name, "*?") {
			needsCatalogue = true
			hasWildcard = true
		} else {
			names = append(names, name)
		}
	}
	if needsCatalogue {
		if account.IsBedrock() {
			for name := range domain.DefaultBedrockModelMapping {
				names = append(names, name)
			}
		} else if account.Type == AccountTypeAPIKey {
			upstream, err := s.cachedAccountDisplayModels(ctx, account)
			if err != nil {
				return nil, err
			}
			names = append(names, upstream...)
		} else {
			names = append(names, defaultModelsListCandidateIDs(account.Platform)...)
		}
	}
	// Exact group allowlist entries can expand account wildcard mappings.
	if hasWildcard && g.ModelAllowlist.Enabled {
		names = append(names, g.ModelAllowlist.Models...)
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if accountSupportsModel(account, name) {
			out = append(out, name)
		}
	}
	return dedupeAndSortModelIDs(out), nil
}

func (s *ModelPlazaService) cachedAccountDisplayModels(ctx context.Context, account *Account) ([]string, error) {
	if s.fetchAccountModels == nil || s.accountModelsCache == nil {
		return nil, errors.New("model plaza account model discovery is unavailable")
	}
	key := fmt.Sprintf("%d:%d", account.ID, account.UpdatedAt.UnixNano())
	if value, ok := s.accountModelsCache.Get(key); ok {
		return value.([]string), nil
	}
	result := s.accountModelsFlight.DoChan(key, func() (any, error) {
		if cached, ok := s.accountModelsCache.Get(key); ok {
			return cached, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		models, err := s.fetchAccountModels(fetchCtx, account)
		if err != nil {
			// Upstream errors can contain private URLs or response bodies. Keep
			// the public error actionable without disclosing account details.
			return nil, infraerrors.ServiceUnavailable("MODEL_PLAZA_MODELS_UNAVAILABLE", "Unable to load account models; check the account model configuration and retry")
		}
		s.accountModelsCache.SetDefault(key, models)
		return models, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case fetched := <-result:
		if fetched.Err != nil {
			return nil, fetched.Err
		}
		return fetched.Val.([]string), nil
	}
}

// Manual prices use group cards and the global catalogue, never channel prices.
func (s *ModelPlazaService) fillManualDisplayPricing(ctx context.Context, m *PlazaModel, g *Group) {
	if matchGroupModelPricing(g, m.Name) == nil && IsGPTImageGenerationModel(m.Name) && s.billingService != nil {
		m.Pricing = &ChannelModelPricing{BillingMode: BillingModeImage}
		for i, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
			cost := s.billingService.CalculateImageCost(m.Name, tier, 1, &ImagePriceConfig{Price1K: g.ImagePrice1K, Price2K: g.ImagePrice2K, Price4K: g.ImagePrice4K}, 1)
			price := cost.TotalCost
			m.Pricing.Intervals = append(m.Pricing.Intervals, PricingInterval{TierLabel: tier, PerRequestPrice: &price, SortOrder: i})
		}
		return
	}
	if s.pricingService != nil && matchGroupModelPricing(g, m.Name) == nil {
		m.Pricing = synthesizePricingFromLiteLLM(s.pricingService.GetModelPricing(m.Name), nil)
	}
	s.fillDisplayPricingWithResolver(ctx, m, g, NewModelPricingResolver(nil, s.billingService))
}
