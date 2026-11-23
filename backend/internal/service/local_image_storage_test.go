//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestLocalImageStoragePolicyDefaults(t *testing.T) {
	policy, err := LocalImageStoragePolicy(&config.ImageStorageConfig{Backend: "local", LocalPublicBaseURL: "https://direct.test"})
	require.NoError(t, err)
	require.EqualValues(t, 20<<30, policy.MaxBytes)
	require.Equal(t, 12*time.Hour, policy.Retention)
	require.Equal(t, 2*time.Hour, policy.CleanupInterval)
	require.EqualValues(t, 2<<30, policy.AlertThresholdBytes)
}

func TestLocalImageAlertRecipientsUseExistingSettings(t *testing.T) {
	repo := newStubSettingRepo()
	ctx := context.Background()
	require.NoError(t, repo.Set(ctx, SettingKeyAccountQuotaNotifyEmails, `[{"email":"fallback@test.example","verified":true},{"email":"unverified@test.example","verified":false}]`))
	require.NoError(t, repo.Set(ctx, SettingKeyOpsEmailNotificationConfig, `{"alert":{"enabled":true,"recipients":[" admin@test.example ","ADMIN@test.example",""]}}`))
	recipients, err := localImageAlertRecipients(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, []string{"admin@test.example"}, recipients)
	require.NoError(t, repo.Set(ctx, SettingKeyOpsEmailNotificationConfig, `{"alert":{"enabled":false,"recipients":["ignored@test.example"]}}`))
	recipients, err = localImageAlertRecipients(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, []string{"fallback@test.example"}, recipients)
}
