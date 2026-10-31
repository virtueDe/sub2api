package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

func (s *OpenAIGatewayService) BoundVideoAccountPlatform(ctx context.Context, id int64) (string, error) {
	a, err := s.accountRepo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if a == nil {
		return "", fmt.Errorf("video account not found")
	}
	return a.Platform, nil
}

const (
	PlatformSeedance = domain.PlatformSeedance
	PlatformWan      = domain.PlatformWan
	PlatformKling    = domain.PlatformKling
	PlatformSora     = domain.PlatformSora
)

func IsVideoPlatform(platform string) bool { return domain.IsVideoPlatform(platform) }

func IsProviderVideoTask(key string) bool {
	for _, prefix := range []string{"seedance:", "wan:", "kling:", "sora:"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func videoUsageDuration(result *OpenAIForwardResult) int {
	if IsProviderVideoTask(result.ResponseID) {
		return result.VideoDurationSeconds
	}
	return NormalizeVideoBillingDurationSecondsOrDefault(result.VideoDurationSeconds)
}

func lookupProviderVideoResolution(resolution string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(resolution))
	switch value {
	case "540p", "768p", "1024p", "1440p", "2160p":
		return value, true
	}
	return LookupVideoBillingResolution(value)
}

func videoTaskResolution(taskKey, resolution string) string {
	if IsProviderVideoTask(taskKey) {
		if normalized, ok := lookupProviderVideoResolution(resolution); ok {
			return normalized
		}
		return strings.TrimSpace(resolution)
	}
	return NormalizeVideoBillingResolutionOrDefault(resolution)
}
func VideoPlatformModels(platform string) []string { return domain.VideoPlatformModels(platform) }

func ValidateVideoAccount(platform, accountType string, credentials map[string]any) error {
	if err := validateVideoModelRoutes(platform, credentials); err != nil {
		return err
	}
	if !IsVideoPlatform(platform) {
		return nil
	}
	if accountType != AccountTypeAPIKey {
		return fmt.Errorf("%s accounts only support apikey credentials", platform)
	}
	a := &Account{Credentials: credentials}
	if platform == PlatformKling && (a.GetCredential("kling_access_key") != "" || a.GetCredential("kling_secret_key") != "") {
		if strings.TrimSpace(a.GetCredential("kling_access_key")) == "" || strings.TrimSpace(a.GetCredential("kling_secret_key")) == "" {
			return fmt.Errorf("Kling requires both Access Key and Secret Key")
		}
		return nil
	}
	if strings.TrimSpace(a.GetCredential("api_key")) == "" {
		return fmt.Errorf("%s requires an API key", platform)
	}
	return nil
}

func videoAccountBaseURL(a *Account) string {
	if base := strings.TrimSpace(a.GetCredential("base_url")); base != "" {
		return base
	}
	return domain.VideoPlatformBaseURL(a.Platform)
}
