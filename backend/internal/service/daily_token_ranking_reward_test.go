//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type dailyTokenRankingRewardRepoStub struct {
	existing    []DailyTokenRankingRewardEntry
	settledRank int
	snapshot    []DailyTokenRankingRewardEntry
	afterSettle func()
}

func (s *dailyTokenRankingRewardRepoStub) ListByDate(context.Context, string) ([]DailyTokenRankingRewardEntry, error) {
	return append([]DailyTokenRankingRewardEntry(nil), s.existing...), nil
}

func (s *dailyTokenRankingRewardRepoStub) SettleRank(_ context.Context, _ string, _ int64, entries []DailyTokenRankingRewardEntry, rank int) (DailyTokenRankingRewardEntry, error) {
	s.settledRank = rank
	s.snapshot = append([]DailyTokenRankingRewardEntry(nil), entries...)
	s.existing = append([]DailyTokenRankingRewardEntry(nil), entries...)
	for index := range s.existing {
		if s.existing[index].Rank == rank {
			s.existing[index].Status = "paid"
			if s.afterSettle != nil {
				s.afterSettle()
			}
			return s.existing[index], nil
		}
	}
	return DailyTokenRankingRewardEntry{}, nil
}

func dailyTokenRankingRewardRows() []usagestats.DailyTokenRankingSource {
	return []usagestats.DailyTokenRankingSource{
		{UserID: 11, Email: "first@example.com", TotalTokens: 3000, RequestCount: 30},
		{UserID: 12, Email: "second@example.com", TotalTokens: 2000, RequestCount: 20},
		{UserID: 13, Email: "third@example.com", TotalTokens: 1000, RequestCount: 10},
	}
}

func TestDailyTokenRankingRewardPreviewKeepsPendingRanksAfterPartialSettlement(t *testing.T) {
	rewardRepo := &dailyTokenRankingRewardRepoStub{existing: []DailyTokenRankingRewardEntry{
		{Rank: 1, UserID: 11, DisplayName: "f***t@example.com", TotalTokens: 3000, RequestCount: 30, RewardAmount: 3, Status: "paid"},
	}}
	service := NewDailyTokenRankingRewardService(
		&dailyTokenRankingRepoStub{rows: dailyTokenRankingRewardRows()},
		rewardRepo,
		nil,
		nil,
	)

	result, err := service.Preview(context.Background(), "2026-08-28")
	require.NoError(t, err)
	require.False(t, result.Settled)
	require.Equal(t, []string{"paid", "pending", "pending"}, []string{
		result.Entries[0].Status,
		result.Entries[1].Status,
		result.Entries[2].Status,
	})
}

func TestDailyTokenRankingRewardSettleRankOnlySettlesSelectedRank(t *testing.T) {
	rankingRepo := &dailyTokenRankingRepoStub{rows: dailyTokenRankingRewardRows()}
	rewardRepo := &dailyTokenRankingRewardRepoStub{afterSettle: func() { rankingRepo.err = dbstats.ErrBusy }}
	service := NewDailyTokenRankingRewardService(
		rankingRepo,
		rewardRepo,
		nil,
		nil,
	)

	result, err := service.SettleRank(context.Background(), "2026-08-28", 2, 99)
	require.NoError(t, err)
	require.Equal(t, 1, rankingRepo.calls, "a committed payment must not turn into a busy statistics error")
	require.Equal(t, 2, rewardRepo.settledRank)
	require.Len(t, rewardRepo.snapshot, 3)
	require.False(t, result.Settled)
	require.Equal(t, []string{"pending", "paid", "pending"}, []string{
		result.Entries[0].Status,
		result.Entries[1].Status,
		result.Entries[2].Status,
	})
}

func TestDailyTokenRankingRewardReturnsOtherCommittedRanks(t *testing.T) {
	rewardRepo := &dailyTokenRankingRewardRepoStub{}
	rewardRepo.afterSettle = func() {
		// Another payment and the frozen candidate snapshot became visible
		// after Preview; the response must reflect the persisted rewards.
		for i := range rewardRepo.existing {
			rewardRepo.existing[i].Status = "paid"
		}
		rewardRepo.existing[0].UserID = 99
	}
	s := NewDailyTokenRankingRewardService(&dailyTokenRankingRepoStub{rows: dailyTokenRankingRewardRows()}, rewardRepo, nil, nil)
	result, err := s.SettleRank(context.Background(), "2026-08-28", 2, 99)
	require.NoError(t, err)
	require.True(t, result.Settled)
	require.Equal(t, int64(99), result.Entries[0].UserID)
}
