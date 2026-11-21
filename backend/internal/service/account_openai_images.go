package service

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// OpenAIImageModelsCredentialKey contains upstream model IDs, before public
// aliases are applied. It is independent of native Codex image model detection.
const OpenAIImageModelsCredentialKey = "openai_image_models"

func (a *Account) HasOpenAICompatibleImages() bool {
	if a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeAPIKey {
		return false
	}
	capabilities, _ := a.openAIEndpointCapabilitySet()
	return capabilities[string(OpenAIEndpointCapabilityImages)]
}

func (a *Account) ConfiguredOpenAIImageModels() []string {
	if !a.HasOpenAICompatibleImages() {
		return nil
	}
	models, err := normalizeOpenAIImageModels(a.Credentials[OpenAIImageModelsCredentialKey])
	if err != nil {
		return nil
	}
	return models
}

// SupportsOpenAIImageModel checks the final account mapping target. In
// particular, a GPT-looking alias may point at a non-native provider model.
func (a *Account) SupportsOpenAIImageModel(model string) bool {
	if a == nil || a.Platform != PlatformOpenAI || strings.TrimSpace(model) == "" {
		return false
	}
	upstream := strings.TrimSpace(a.GetMappedModel(model))
	if a.Type == AccountTypeOAuth || a.Type == AccountTypeSetupToken {
		return validateOpenAIImagesModel(upstream) == nil
	}
	if a.Type != AccountTypeAPIKey {
		return false
	}
	if a.HasOpenAICompatibleImages() {
		if strings.TrimSpace(a.GetCredential("base_url")) == "" {
			return false
		}
		for _, configured := range a.ConfiguredOpenAIImageModels() {
			if configured == upstream {
				return true
			}
		}
		return false
	}
	return validateCompatibleImagesModel(upstream) == nil
}

// OpenAIImageModelIDs includes explicitly configured models and public aliases
// whose targets support Images. Wildcards are filters, not concrete model IDs.
func (a *Account) OpenAIImageModelIDs() []string {
	set := make(map[string]struct{})
	if a == nil || a.Platform != PlatformOpenAI {
		return nil
	}
	for _, model := range a.ConfiguredOpenAIImageModels() {
		if a.IsModelSupported(model) && a.SupportsOpenAIImageModel(model) {
			set[model] = struct{}{}
		}
	}
	for model := range a.GetModelMapping() {
		if !strings.Contains(model, "*") && a.IsModelSupported(model) && a.SupportsOpenAIImageModel(model) {
			set[model] = struct{}{}
		}
	}
	models := make([]string, 0, len(set))
	for model := range set {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

// AppendOpenAIImageModels supplements discovery with operator-declared images.
// Many image-only compatible providers do not implement a /models endpoint.
func AppendOpenAIImageModels(models []openai.Model, account *Account) []openai.Model {
	result := append([]openai.Model(nil), models...)
	if !account.HasOpenAICompatibleImages() {
		return result
	}
	seen := make(map[string]bool, len(result))
	for _, model := range result {
		seen[model.ID] = true
	}
	for _, id := range account.OpenAIImageModelIDs() {
		if !seen[id] {
			result = append(result, openai.Model{ID: id, Object: "model", Type: "model", DisplayName: id})
			seen[id] = true
		}
	}
	return result
}

func normalizeOpenAIImageModels(raw any) ([]string, error) {
	var values []string
	switch typed := raw.(type) {
	case nil:
		return nil, nil
	case []string:
		values = typed
	case []any:
		for _, item := range typed {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("openai_image_models must be an array of model IDs")
			}
			values = append(values, value)
		}
	default:
		return nil, fmt.Errorf("openai_image_models must be an array of model IDs")
	}
	if len(values) > 1000 {
		return nil, fmt.Errorf("openai_image_models must contain at most 1000 model IDs")
	}
	models := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		model := strings.TrimSpace(value)
		if model == "" || strings.Contains(model, "*") || utf8.RuneCountInString(model) > 256 || strings.ContainsFunc(model, unicode.IsControl) {
			return nil, fmt.Errorf("openai_image_models must contain non-empty model IDs without wildcards or control characters")
		}
		if !seen[model] {
			models = append(models, model)
			seen[model] = true
		}
	}
	return models, nil
}

// NormalizeOpenAIImageCredentials is shared by create, update and bulk writes.
func NormalizeOpenAIImageCredentials(platform, accountType string, credentials map[string]any) error {
	account := &Account{Platform: platform, Type: accountType, Credentials: credentials}
	capabilities, _ := account.openAIEndpointCapabilitySet()
	enabled := capabilities[string(OpenAIEndpointCapabilityImages)]
	models, err := normalizeOpenAIImageModels(credentials[OpenAIImageModelsCredentialKey])
	if err == nil && (enabled || len(models) > 0) && (platform != PlatformOpenAI || accountType != AccountTypeAPIKey) {
		err = fmt.Errorf("OpenAI compatible images require an OpenAI API key account")
	}
	if err == nil && enabled && len(models) == 0 {
		err = fmt.Errorf("configure at least one upstream image model ID for OpenAI compatible images")
	}
	if err == nil && enabled && strings.TrimSpace(account.GetCredential("base_url")) == "" {
		err = fmt.Errorf("configure an upstream base URL for OpenAI compatible images")
	}
	if err != nil {
		return infraerrors.BadRequest("OPENAI_IMAGE_MODELS_INVALID", err.Error())
	}
	if _, provided := credentials[OpenAIImageModelsCredentialKey]; provided && credentials[OpenAIImageModelsCredentialKey] != nil {
		credentials[OpenAIImageModelsCredentialKey] = models
	}
	return nil
}
