package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const dailyTokenRankingCacheTTL = time.Minute

var ErrDailyTokenRankingDisabled = infraerrors.NotFound(
	"DAILY_TOKEN_RANKING_DISABLED",
	"daily token ranking is disabled",
)

type DailyTokenRankingRepository interface {
	GetDailyTokenRanking(ctx context.Context, startTime, endTime time.Time, limit int) ([]usagestats.DailyTokenRankingSource, error)
}

type DailyTokenRankingEntry struct {
	Rank        int    `json:"rank"`
	DisplayName string `json:"display_name"`
	TotalTokens int64  `json:"total_tokens"`
}

type DailyTokenRankingResponse struct {
	Ranking   []DailyTokenRankingEntry `json:"ranking"`
	Date      string                   `json:"date"`
	Timezone  string                   `json:"timezone"`
	UpdatedAt time.Time                `json:"updated_at"`
}

type dailyTokenRankingCacheEntry struct {
	key       string
	expiresAt time.Time
	value     *DailyTokenRankingResponse
}

type DailyTokenRankingService struct {
	repo           DailyTokenRankingRepository
	settingService *SettingService
	mu             sync.Mutex
	cache          dailyTokenRankingCacheEntry
}

func ProvideDailyTokenRankingRepository(repo UsageLogRepository) (DailyTokenRankingRepository, error) {
	rankingRepo, ok := repo.(DailyTokenRankingRepository)
	if !ok {
		return nil, fmt.Errorf("usage log repository does not support daily token ranking")
	}
	return rankingRepo, nil
}

func NewDailyTokenRankingService(
	repo DailyTokenRankingRepository,
	settingService *SettingService,
) *DailyTokenRankingService {
	return &DailyTokenRankingService{repo: repo, settingService: settingService}
}

func (s *DailyTokenRankingService) GetToday(ctx context.Context) (*DailyTokenRankingResponse, error) {
	runtime := s.settingService.GetDailyTokenRankingRuntime(ctx)
	if !runtime.Enabled {
		return nil, ErrDailyTokenRankingDisabled
	}

	start := timezone.Today()
	end := start.AddDate(0, 0, 1)
	cacheKey := start.Format("2006-01-02") + "|" + timezone.Name() + "|" + strconv.Itoa(runtime.Limit)
	now := timezone.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache.key == cacheKey && now.Before(s.cache.expiresAt) && s.cache.value != nil {
		return cloneDailyTokenRankingResponse(s.cache.value), nil
	}

	rows, err := s.repo.GetDailyTokenRanking(ctx, start, end, runtime.Limit)
	if err != nil {
		return nil, err
	}

	entries := make([]DailyTokenRankingEntry, 0, len(rows))
	for index, row := range rows {
		entries = append(entries, DailyTokenRankingEntry{
			Rank:        index + 1,
			DisplayName: maskRankingIdentity(row.Username, row.Email),
			TotalTokens: row.TotalTokens,
		})
	}
	result := &DailyTokenRankingResponse{
		Ranking:   entries,
		Date:      start.Format("2006-01-02"),
		Timezone:  timezone.Name(),
		UpdatedAt: now,
	}
	s.cache = dailyTokenRankingCacheEntry{
		key:       cacheKey,
		expiresAt: now.Add(dailyTokenRankingCacheTTL),
		value:     cloneDailyTokenRankingResponse(result),
	}
	return result, nil
}

func maskRankingIdentity(username, email string) string {
	identity := strings.TrimSpace(username)
	if identity == "" {
		identity = strings.TrimSpace(email)
	}
	if at := strings.LastIndex(identity, "@"); at > 0 {
		return maskRankingText(identity[:at]) + identity[at:]
	}
	return maskRankingText(identity)
}

func maskRankingText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	switch len(runes) {
	case 0, 1:
		return "***"
	default:
		return string(runes[0]) + "***" + string(runes[len(runes)-1])
	}
}

func cloneDailyTokenRankingResponse(source *DailyTokenRankingResponse) *DailyTokenRankingResponse {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Ranking = append([]DailyTokenRankingEntry(nil), source.Ranking...)
	return &clone
}
