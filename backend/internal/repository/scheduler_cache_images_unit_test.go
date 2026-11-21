//go:build unit

package repository

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerCachePreservesConfiguredImageAdmission(t *testing.T) {
	credentials := filterSchedulerCredentials(map[string]any{
		"base_url":                             "https://compatible.example/v1",
		"openai_capabilities":                  []string{"images"},
		service.OpenAIImageModelsCredentialKey: []string{"seedream-5.5", "vendor-art-v9"},
		"model_mapping":                        map[string]any{"paint": "seedream-5.5"},
	})
	encoded, err := json.Marshal(credentials)
	require.NoError(t, err)
	var hydrated map[string]any
	require.NoError(t, json.Unmarshal(encoded, &hydrated))
	account := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: hydrated}
	require.True(t, account.SupportsOpenAIImageModel("paint"))
	require.True(t, account.SupportsOpenAIImageModel("vendor-art-v9"))
	require.False(t, account.SupportsOpenAIImageModel("unknown-image-model"))
}
