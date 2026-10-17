//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSettingServiceImageUpstreamResponsePassthrough(t *testing.T) {
	cacheImagePassthroughSettings(t, false, nil)
	repo := &forwardedIPMigrationRepoStub{values: map[string]string{
		SettingKeyImageUpstreamResponsePassthroughEnabled:    "true",
		SettingKeyImageUpstreamResponsePassthroughAccountIDs: "[9,7,9,-1]",
	}}
	svc := NewSettingService(repo, &config.Config{})
	parsed := svc.parseSettings(repo.values)
	require.True(t, parsed.ImageUpstreamResponsePassthroughEnabled)
	require.ElementsMatch(t, []int64{7, 9}, parsed.ImageUpstreamResponsePassthroughAccountIDs)
	require.False(t, svc.parseSettings(nil).ImageUpstreamResponsePassthroughEnabled)
	require.Empty(t, svc.parseSettings(nil).ImageUpstreamResponsePassthroughAccountIDs)

	// Exercise the cold database read, then the cached account filter.
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	require.True(t, svc.IsImageUpstreamResponsePassthroughEnabledForAccount(context.Background(), 7))
	require.False(t, svc.IsImageUpstreamResponsePassthroughEnabledForAccount(context.Background(), 8))

	updates, err := svc.buildSystemSettingsUpdates(context.Background(), parsed)
	require.NoError(t, err)
	require.Equal(t, "true", updates[SettingKeyImageUpstreamResponsePassthroughEnabled])
	require.JSONEq(t, "[7,9]", updates[SettingKeyImageUpstreamResponsePassthroughAccountIDs])

	// Saving a setting refreshes the live cache without waiting for its TTL.
	parsed.ImageUpstreamResponsePassthroughEnabled = false
	svc.refreshCachedSettings(parsed)
	require.False(t, svc.IsImageUpstreamResponsePassthroughEnabledForAccount(context.Background(), 7))
}
