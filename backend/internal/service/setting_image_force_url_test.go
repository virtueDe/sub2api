//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSettingServiceImageForceURLResponse(t *testing.T) {
	cacheImagePassthroughSettings(t, false, nil)
	repo := &forwardedIPMigrationRepoStub{values: map[string]string{
		SettingKeyImageForceURLResponseEnabled:    "true",
		SettingKeyImageForceURLResponseAccountIDs: "[9,7,9,-1]",
	}}
	svc := NewSettingService(repo, &config.Config{})
	parsed := svc.parseSettings(repo.values)
	require.True(t, parsed.ImageForceURLResponseEnabled)
	require.ElementsMatch(t, []int64{7, 9}, parsed.ImageForceURLResponseAccountIDs)
	require.False(t, svc.parseSettings(nil).ImageForceURLResponseEnabled)
	require.Empty(t, svc.parseSettings(nil).ImageForceURLResponseAccountIDs)

	// Exercise the cold database read, then the cached account filter.
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	require.True(t, svc.IsImageForceURLResponseEnabledForAccount(context.Background(), 7))
	require.False(t, svc.IsImageForceURLResponseEnabledForAccount(context.Background(), 8))

	updates, err := svc.buildSystemSettingsUpdates(context.Background(), parsed)
	require.NoError(t, err)
	require.Equal(t, "true", updates[SettingKeyImageForceURLResponseEnabled])
	require.JSONEq(t, "[7,9]", updates[SettingKeyImageForceURLResponseAccountIDs])

	// Saving a setting refreshes the live cache without waiting for its TTL.
	parsed.ImageForceURLResponseEnabled = false
	svc.refreshCachedSettings(parsed)
	require.False(t, svc.IsImageForceURLResponseEnabledForAccount(context.Background(), 7))
}
