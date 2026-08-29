//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type dailyTokenRankingRepoStub struct {
	rows  []usagestats.DailyTokenRankingSource
	err   error
	calls int
	start time.Time
	end   time.Time
	limit int
}

func (s *dailyTokenRankingRepoStub) GetDailyTokenRanking(
	_ context.Context,
	start, end time.Time,
	limit int,
) ([]usagestats.DailyTokenRankingSource, error) {
	s.calls++
	s.start = start
	s.end = end
	s.limit = limit
	return s.rows, s.err
}

func TestDailyTokenRankingServiceMasksIdentityAndCaches(t *testing.T) {
	settings := &settingRepoStub{values: map[string]string{
		SettingKeyDailyTokenRankingEnabled: "true",
		SettingKeyDailyTokenRankingLimit:   "2",
	}}
	repo := &dailyTokenRankingRepoStub{rows: []usagestats.DailyTokenRankingSource{
		{UserID: 1, Username: "xiaoming", Email: "hidden@example.com", TotalTokens: 1234},
		{UserID: 2, Email: "xiaoming@example.com", TotalTokens: 456},
	}}
	service := NewDailyTokenRankingService(repo, &SettingService{settingRepo: settings})

	first, err := service.GetToday(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, repo.limit)
	require.Equal(t, dailyTokenRankingTimezone, repo.start.Location().String())
	require.Equal(t, 0, repo.start.Hour())
	require.Equal(t, repo.start.AddDate(0, 0, 1), repo.end)
	require.Equal(t, dailyTokenRankingTimezone, first.Timezone)
	require.Equal(t, []DailyTokenRankingEntry{
		{Rank: 1, DisplayName: "x***g", TotalTokens: 1234},
		{Rank: 2, DisplayName: "x***g@example.com", TotalTokens: 456},
	}, first.Ranking)

	second, err := service.GetToday(context.Background())
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, repo.calls)
}

func TestDailyTokenRankingServiceFailsClosedWhenDisabled(t *testing.T) {
	service := NewDailyTokenRankingService(
		&dailyTokenRankingRepoStub{},
		&SettingService{settingRepo: &settingRepoStub{values: map[string]string{}}},
	)

	_, err := service.GetToday(context.Background())
	require.Error(t, err)
	require.True(t, infraerrors.IsNotFound(err))
}

func TestDailyTokenRankingRuntimeDefaultsAndClampsLimit(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "missing", want: dailyTokenRankingLimitFallback},
		{name: "invalid", value: "abc", want: dailyTokenRankingLimitFallback},
		{name: "below range", value: "0", want: dailyTokenRankingLimitMin},
		{name: "above range", value: "51", want: dailyTokenRankingLimitMax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &SettingService{settingRepo: &settingRepoStub{values: map[string]string{
				SettingKeyDailyTokenRankingEnabled: "true",
				SettingKeyDailyTokenRankingLimit:   tt.value,
			}}}
			runtime := service.GetDailyTokenRankingRuntime(context.Background())
			require.True(t, runtime.Enabled)
			require.Equal(t, tt.want, runtime.Limit)
		})
	}
}

func TestDailyTokenRankingServiceReturnsRepositoryError(t *testing.T) {
	wantErr := errors.New("query failed")
	service := NewDailyTokenRankingService(
		&dailyTokenRankingRepoStub{err: wantErr},
		&SettingService{settingRepo: &settingRepoStub{values: map[string]string{
			SettingKeyDailyTokenRankingEnabled: "true",
		}}},
	)

	_, err := service.GetToday(context.Background())
	require.ErrorIs(t, err, wantErr)
}

func TestMaskDailyTokenRankingIdentity(t *testing.T) {
	tests := []struct {
		username string
		email    string
		want     string
	}{
		{username: "x", want: "***"},
		{username: "小明", want: "小***明"},
		{username: "xiaoming", email: "hidden@example.com", want: "x***g"},
		{email: "x@example.com", want: "***@example.com"},
		{email: "xiaoming@example.com", want: "x***g@example.com"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, maskRankingIdentity(tt.username, tt.email))
	}
}
