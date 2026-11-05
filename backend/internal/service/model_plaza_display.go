package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

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

// Explicit models need no channel association. Resolve configured pricing from
// the same group/platform as billing; unknown models keep an unavailable price.
func (s *ModelPlazaService) explicitDisplayModel(ctx context.Context, spec ModelPlazaDisplayModel, g *Group) PlazaModel {
	m := PlazaModel{Name: spec.Name, Platform: spec.Platform}
	if s.resolver != nil {
		resolved := s.resolver.Resolve(WithResolvedTargetPlatform(ctx, spec.Platform), PricingInput{Model: spec.Name, GroupID: &g.ID, Group: g})
		if resolved != nil && resolved.channelPricing != nil {
			clone := resolved.channelPricing.Clone()
			m.Pricing = &clone
		}
	}
	if m.Pricing == nil {
		m.Pricing = matchGroupModelPricing(g, spec.Name)
	}
	// GPT Images without an explicit token card use the gateway's per-image
	// billing path. Do not let the token catalogue hide the group's image tiers.
	if m.Pricing == nil && IsGPTImageGenerationModel(spec.Name) && s.billingService != nil {
		m.Pricing = &ChannelModelPricing{BillingMode: BillingModeImage}
		for i, tier := range []string{ImageBillingSize1K, ImageBillingSize2K, ImageBillingSize4K} {
			cost := s.billingService.CalculateImageCost(spec.Name, tier, 1, &ImagePriceConfig{Price1K: g.ImagePrice1K, Price2K: g.ImagePrice2K, Price4K: g.ImagePrice4K}, 1)
			price := cost.TotalCost
			m.Pricing.Intervals = append(m.Pricing.Intervals, PricingInterval{TierLabel: tier, PerRequestPrice: &price, SortOrder: i})
		}
		return m
	}
	s.fillDisplayPricing(ctx, &m, g)
	return m
}
