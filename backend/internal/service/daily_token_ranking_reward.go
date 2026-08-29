package service

import (
	"context"
	"fmt"
	"sort"
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
	SettleRank(ctx context.Context, rewardDate string, operatorID int64, entries []DailyTokenRankingRewardEntry, rank int) (DailyTokenRankingRewardEntry, error)
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
	existing, err := s.rewardRepo.ListByDate(ctx, date)
	if err != nil {
		return nil, err
	}
	rows, err := s.rankingRepo.GetDailyTokenRankingForSettlement(ctx, start, end, 3)
	if err != nil {
		return nil, err
	}
	entries := mergeDailyTokenRankingRewardEntries(buildDailyTokenRankingRewardEntries(date, rows), existing)
	return &DailyTokenRankingRewardResponse{
		Date:     date,
		Timezone: dailyTokenRankingRewardTimezone,
		Settled:  dailyTokenRankingRewardsSettled(entries),
		Entries:  entries,
	}, nil
}

func (s *DailyTokenRankingRewardService) SettleRank(ctx context.Context, rewardDate string, rank int, operatorID int64) (*DailyTokenRankingRewardResponse, error) {
	if rank < 1 || rank > 3 {
		return nil, fmt.Errorf("rank must be between 1 and 3")
	}
	preview, err := s.Preview(ctx, rewardDate)
	if err != nil {
		return nil, err
	}
	var target *DailyTokenRankingRewardEntry
	for index := range preview.Entries {
		if preview.Entries[index].Rank == rank {
			target = &preview.Entries[index]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("rank %d is not available for %s", rank, preview.Date)
	}
	if target.Status != "pending" {
		return preview, nil
	}
	settledEntry, err := s.rewardRepo.SettleRank(ctx, preview.Date, operatorID, preview.Entries, rank)
	if err != nil {
		return nil, err
	}
	if settledEntry.Status == "paid" {
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, settledEntry.UserID)
		}
		if s.billingCache != nil {
			_ = s.billingCache.InvalidateUserBalance(ctx, settledEntry.UserID)
		}
	}
	return s.Preview(ctx, preview.Date)
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

func mergeDailyTokenRankingRewardEntries(candidates, existing []DailyTokenRankingRewardEntry) []DailyTokenRankingRewardEntry {
	byRank := make(map[int]DailyTokenRankingRewardEntry, len(candidates)+len(existing))
	for _, entry := range candidates {
		if entry.Rank > 0 {
			byRank[entry.Rank] = entry
		}
	}
	for _, entry := range existing {
		if entry.Rank > 0 {
			byRank[entry.Rank] = entry
		}
	}
	merged := make([]DailyTokenRankingRewardEntry, 0, len(byRank))
	for _, entry := range byRank {
		merged = append(merged, entry)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Rank < merged[j].Rank })
	return merged
}

func dailyTokenRankingRewardsSettled(entries []DailyTokenRankingRewardEntry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if entry.Status == "pending" {
			return false
		}
	}
	return true
}
