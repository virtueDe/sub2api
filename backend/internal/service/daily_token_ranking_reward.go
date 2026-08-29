package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

const dailyTokenRankingRewardTimezone = "Asia/Shanghai"

var dailyTokenRankingRewardLocation = time.FixedZone(dailyTokenRankingRewardTimezone, 8*60*60)

type DailyTokenRankingRewardEntry struct {
	Rank         int        `json:"rank"`
	UserID       int64      `json:"-"`
	DisplayName  string     `json:"display_name"`
	TotalTokens  int64      `json:"total_tokens"`
	RequestCount int64      `json:"request_count"`
	RewardAmount float64    `json:"reward_amount"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"`
	Note         string     `json:"note"`
	SettledAt    *time.Time `json:"settled_at,omitempty"`
}

type DailyTokenRankingRewardResponse struct {
	Date     string                         `json:"date"`
	Timezone string                         `json:"timezone"`
	Settled  bool                           `json:"settled"`
	Entries  []DailyTokenRankingRewardEntry `json:"entries"`
}

type DailyTokenRankingRewardRepository interface {
	ListByDate(ctx context.Context, rewardDate string) ([]DailyTokenRankingRewardEntry, error)
	Settle(ctx context.Context, rewardDate string, operatorID int64, entries []DailyTokenRankingRewardEntry) ([]DailyTokenRankingRewardEntry, error)
}

type DailyTokenRankingRewardService struct {
	rankingRepo          DailyTokenRankingRepository
	rewardRepo           DailyTokenRankingRewardRepository
	billingCache         *BillingCacheService
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

func NewDailyTokenRankingRewardService(
	rankingRepo DailyTokenRankingRepository,
	rewardRepo DailyTokenRankingRewardRepository,
	billingCache *BillingCacheService,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *DailyTokenRankingRewardService {
	return &DailyTokenRankingRewardService{
		rankingRepo:          rankingRepo,
		rewardRepo:           rewardRepo,
		billingCache:         billingCache,
		authCacheInvalidator: authCacheInvalidator,
	}
}

func (s *DailyTokenRankingRewardService) Preview(ctx context.Context, rewardDate string) (*DailyTokenRankingRewardResponse, error) {
	date, start, end, err := normalizeDailyTokenRankingRewardDate(rewardDate)
	if err != nil {
		return nil, err
	}
	if existing, err := s.rewardRepo.ListByDate(ctx, date); err != nil {
		return nil, err
	} else if len(existing) > 0 {
		return &DailyTokenRankingRewardResponse{Date: date, Timezone: dailyTokenRankingRewardTimezone, Settled: true, Entries: filterDailyTokenRankingRewardEntries(existing)}, nil
	}

	rows, err := s.rankingRepo.GetDailyTokenRankingForSettlement(ctx, start, end, 3)
	if err != nil {
		return nil, err
	}
	return &DailyTokenRankingRewardResponse{
		Date:     date,
		Timezone: dailyTokenRankingRewardTimezone,
		Entries:  buildDailyTokenRankingRewardEntries(date, rows),
	}, nil
}

func (s *DailyTokenRankingRewardService) Settle(ctx context.Context, rewardDate string, operatorID int64) (*DailyTokenRankingRewardResponse, error) {
	preview, err := s.Preview(ctx, rewardDate)
	if err != nil {
		return nil, err
	}
	if preview.Settled {
		return preview, nil
	}
	entries, err := s.rewardRepo.Settle(ctx, preview.Date, operatorID, preview.Entries)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Status != "paid" {
			continue
		}
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, entry.UserID)
		}
		if s.billingCache != nil {
			_ = s.billingCache.InvalidateUserBalance(ctx, entry.UserID)
		}
	}
	return &DailyTokenRankingRewardResponse{Date: preview.Date, Timezone: dailyTokenRankingRewardTimezone, Settled: true, Entries: filterDailyTokenRankingRewardEntries(entries)}, nil
}

func normalizeDailyTokenRankingRewardDate(raw string) (string, time.Time, time.Time, error) {
	now := time.Now().In(dailyTokenRankingRewardLocation)
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, dailyTokenRankingRewardLocation).AddDate(0, 0, -1)
	date := yesterday
	if raw != "" {
		parsed, err := time.ParseInLocation("2006-01-02", raw, dailyTokenRankingRewardLocation)
		if err != nil {
			return "", time.Time{}, time.Time{}, fmt.Errorf("invalid reward date, expected YYYY-MM-DD")
		}
		if parsed.After(yesterday) {
			return "", time.Time{}, time.Time{}, fmt.Errorf("reward date must not be later than yesterday")
		}
		date = parsed
	}
	return date.Format("2006-01-02"), date, date.AddDate(0, 0, 1), nil
}

func buildDailyTokenRankingRewardEntries(date string, rows []usagestats.DailyTokenRankingSource) []DailyTokenRankingRewardEntry {
	rewards := []float64{3, 2, 1}
	entries := make([]DailyTokenRankingRewardEntry, 0, len(rows))
	for index, row := range rows {
		if index >= len(rewards) {
			break
		}
		rank := index + 1
		entries = append(entries, DailyTokenRankingRewardEntry{
			Rank:         rank,
			UserID:       row.UserID,
			DisplayName:  maskRankingIdentity(row.Username, row.Email),
			TotalTokens:  row.TotalTokens,
			RequestCount: row.RequestCount,
			RewardAmount: rewards[index],
			Status:       "pending",
			Note:         fmt.Sprintf("%s 每日 Token 排行奖励，第 %d 名", date, rank),
		})
	}
	return entries
}

func filterDailyTokenRankingRewardEntries(entries []DailyTokenRankingRewardEntry) []DailyTokenRankingRewardEntry {
	filtered := make([]DailyTokenRankingRewardEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Rank > 0 {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
