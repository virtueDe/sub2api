package domain

const (
	PlatformSeedance = "seedance"
	PlatformWan      = "wan"
	PlatformKling    = "kling"
	PlatformSora     = "sora"
)

type VideoProviderSpec struct {
	BaseURL string   `json:"base_url"`
	Models  []string `json:"models"`
}

func IsVideoPlatform(platform string) bool {
	spec, ok := LookupPlatform(platform)
	return ok && spec.Video != nil
}

func VideoPlatformModels(platform string) []string {
	spec, _ := LookupPlatform(platform)
	if spec.Video == nil {
		return nil
	}
	return append([]string(nil), spec.Video.Models...)
}

func VideoPlatformBaseURL(platform string) string {
	spec, _ := LookupPlatform(platform)
	if spec.Video == nil {
		return ""
	}
	return spec.Video.BaseURL
}
