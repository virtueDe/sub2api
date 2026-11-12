package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestContentModerationLazyPolicy(t *testing.T) {
	for _, mode := range []string{"disabled", "off", "group", "keyword_hit", "keyword_miss", "semantic", "observe_keyword"} {
		t.Run(mode, func(t *testing.T) {
			cfg := defaultContentModerationConfig()
			cfg.Enabled, cfg.AllGroups, cfg.PreHashCheckEnabled = true, true, true
			cfg.Mode = ContentModerationModePreBlock
			cfg.BlockedKeywords = []string{"blocked"}
			cfg.KeywordBlockingMode = ContentModerationKeywordModeKeywordOnly
			risk := "true"
			switch mode {
			case "disabled":
				risk = "false"
			case "off":
				cfg.Mode = ContentModerationModeOff
			case "group":
				cfg.AllGroups = false
				cfg.GroupIDs = []int64{99}
			case "semantic":
				cfg.KeywordBlockingMode = ContentModerationKeywordModeAPIOnly
			case "observe_keyword":
				cfg.Mode = ContentModerationModeObserve
			}
			raw, err := json.Marshal(cfg)
			require.NoError(t, err)
			cache := &contentModerationTestHashCache{hashes: map[string]struct{}{}}
			svc := NewContentModerationService(&contentModerationTestSettingRepo{values: map[string]string{
				SettingKeyRiskControlEnabled: risk, SettingKeyContentModerationConfig: string(raw),
			}}, &contentModerationTestRepo{}, cache, nil, nil, nil, nil, nil)
			prompt := "clean prompt"
			if mode == "keyword_hit" {
				prompt = "blocked prompt"
			}
			request := &OpenAIImagesRequest{Prompt: prompt, InputImageURLs: []string{"https://example.com/a.png", "https://example.com/b.png"}}
			content := ExtractContentModerationInput(ContentModerationProtocolOpenAIImages, request.ModerationBody())
			content.Normalize()
			cache.hashes[content.Hash()] = struct{}{}
			var calls []bool
			decision, err := svc.Check(context.Background(), ContentModerationCheckInput{
				Protocol: ContentModerationProtocolOpenAIImages,
				BuildBody: func(images bool) []byte {
					calls = append(calls, images)
					return request.ModerationBodyWithImages(images)
				},
			})
			require.NoError(t, err)
			switch mode {
			case "disabled", "off", "group":
				require.Empty(t, calls)
			case "keyword_hit", "keyword_miss":
				require.Equal(t, []bool{false}, calls)
			default:
				require.Equal(t, []bool{true}, calls)
				require.Equal(t, []string{content.Hash()}, cache.snapshotChecked())
				require.Equal(t, content.Hash(), decision.InputHash)
			}
		})
	}
}
