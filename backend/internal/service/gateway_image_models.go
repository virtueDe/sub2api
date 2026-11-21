package service

import (
	"context"
	"sort"
)

// GetAvailableOpenAIImageModels retains account ownership when resolving image
// aliases; the ordinary model list has already lost that information.
func (s *GatewayService) GetAvailableOpenAIImageModels(ctx context.Context, groupID *int64, platform string, candidates []string) []string {
	if s == nil || s.accountRepo == nil || (platform != PlatformOpenAI && platform != PlatformComposite) {
		return nil
	}
	var accounts []Account
	var err error
	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulable(ctx)
	}
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	for i := range accounts {
		for _, model := range accounts[i].OpenAIImageModelIDs() {
			seen[model] = true
		}
		for _, model := range candidates {
			if accounts[i].IsModelSupported(model) && accounts[i].SupportsOpenAIImageModel(model) {
				seen[model] = true
			}
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}
