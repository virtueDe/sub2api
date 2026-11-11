package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamRequestIDFromHeaders_AutomaticHeaders(t *testing.T) {
	for _, tc := range []struct {
		platform string
		header   string
	}{
		{PlatformOpenAI, "X-Request-ID"},
		{PlatformAnthropic, "Request-ID"},
		{PlatformGemini, "X-Goog-Request-ID"},
		{PlatformAntigravity, "X-Goog-Request-ID"},
		{PlatformGrok, "Xai-Request-ID"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			h := http.Header{}
			h.Set("X-Client-Request-ID", "client-correlation-only")
			h.Set("X-Request-ID", "generic-request")
			h.Set(tc.header, " provider-request ")
			account := &Account{Platform: tc.platform}
			require.Equal(t, "provider-request", UpstreamRequestIDFromHeaders(account, h))
			account.Extra = map[string]any{AccountExtraUpstreamRequestIDHeader: "   "}
			require.Equal(t, "provider-request", UpstreamRequestIDFromHeaders(account, h))

			h.Set("X-Oneapi-Request-ID", "relay-request")
			require.Equal(t, "relay-request", UpstreamRequestIDFromHeaders(account, h), "prefer the direct relay's request ID")
		})
	}
}

func TestUpstreamRequestIDFromHeaders_AutomaticMissingHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Client-Request-ID", "sub2api-client")

	require.Equal(t, "", UpstreamRequestIDFromHeaders(nil, h))
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity, PlatformGrok} {
		account := &Account{Platform: platform}
		require.Equal(t, "", UpstreamRequestIDFromHeaders(account, h), "do not infer an upstream ID from client correlation")
		require.Equal(t, "", UpstreamRequestIDFromHeaders(account, nil))
	}
	h.Set("X-Request-ID", " generic-request ")
	require.Equal(t, "", UpstreamRequestIDFromHeaders(nil, h))
	require.Equal(t, "generic-request", UpstreamRequestIDFromHeaders(&Account{Platform: PlatformGemini}, h))
	h.Set("X-Request-ID", "  ")
	h.Set("Request-ID", "alternate-request")
	require.Equal(t, "alternate-request", UpstreamRequestIDFromHeaders(&Account{}, h))
}

func TestUpstreamRequestIDFromHeaders_ReadsOnlyConfiguredHeader(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Extra:    map[string]any{AccountExtraUpstreamRequestIDHeader: " x-oneapi-request-id "},
	}
	h := http.Header{}
	h.Set("X-Request-ID", "passthrough-from-real-upstream")
	require.Equal(t, "", UpstreamRequestIDFromHeaders(account, h))

	h.Set("X-Oneapi-Request-Id", " oneapi-2 ")
	require.Equal(t, "oneapi-2", UpstreamRequestIDFromHeaders(account, h))
	require.Equal(t, "", UpstreamRequestIDFromHeaders(account, nil))

	official := &Account{Platform: PlatformAnthropic, Extra: map[string]any{AccountExtraUpstreamRequestIDHeader: "request-id"}}
	only := http.Header{}
	only.Set("Request-Id", "req_official")
	require.Equal(t, "req_official", UpstreamRequestIDFromHeaders(official, only))
}

func TestUsageUpstreamRequestIDPtr(t *testing.T) {
	account := &Account{Extra: map[string]any{AccountExtraUpstreamRequestIDHeader: "X-Request-ID"}}
	h := http.Header{}
	h.Set("X-Request-ID", strings.Repeat("a", 200))
	require.Nil(t, usageUpstreamRequestIDPtr(account, h, true))
	require.Nil(t, usageUpstreamRequestIDPtr(account, http.Header{}, false))
	require.Nil(t, usageUpstreamRequestIDPtr(nil, h, false))
	require.Nil(t, usageUpstreamRequestIDPtr(&Account{}, h, true))
	require.Equal(t, usageUpstreamRequestIDPtr(account, h, false), usageUpstreamRequestIDPtr(&Account{}, h, false))

	got := usageUpstreamRequestIDPtr(account, h, false)
	require.NotNil(t, got)
	require.Len(t, *got, maxUsageUpstreamRequestIDLen)
	h.Set("X-Request-ID", strings.Repeat("图", 100))
	require.Equal(t, optionalTrimmedStringPtr(strings.Repeat("图", 42)), usageUpstreamRequestIDPtr(&Account{}, h, false))
}

func TestValidateUpstreamRequestIDHeaderExtra(t *testing.T) {
	require.NoError(t, ValidateUpstreamRequestIDHeaderExtra(nil))
	require.NoError(t, ValidateUpstreamRequestIDHeaderExtra(map[string]any{}))

	blank := map[string]any{AccountExtraUpstreamRequestIDHeader: "   "}
	require.NoError(t, ValidateUpstreamRequestIDHeaderExtra(blank))
	_, present := blank[AccountExtraUpstreamRequestIDHeader]
	require.False(t, present, "blank header name must be removed")

	valid := map[string]any{AccountExtraUpstreamRequestIDHeader: " X-Oneapi-Request-Id "}
	require.NoError(t, ValidateUpstreamRequestIDHeaderExtra(valid))
	require.Equal(t, "X-Oneapi-Request-Id", valid[AccountExtraUpstreamRequestIDHeader])

	require.Error(t, ValidateUpstreamRequestIDHeaderExtra(map[string]any{AccountExtraUpstreamRequestIDHeader: 1}))
	require.Error(t, ValidateUpstreamRequestIDHeaderExtra(map[string]any{AccountExtraUpstreamRequestIDHeader: "X Request Id"}))
	require.Error(t, ValidateUpstreamRequestIDHeaderExtra(map[string]any{AccountExtraUpstreamRequestIDHeader: "X-Request-Id:"}))
	require.Error(t, ValidateUpstreamRequestIDHeaderExtra(map[string]any{AccountExtraUpstreamRequestIDHeader: strings.Repeat("x", 65)}))
}
