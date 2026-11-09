//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageSettlementFixedAccountOutOfOrder(t *testing.T) {
	ctx := context.Background()
	for _, zone := range []string{"Asia/Shanghai", "America/New_York"} {
		t.Run(zone, func(t *testing.T) {
			_, s := settlementFixture(t)
			loc, err := time.LoadLocation(zone)
			require.NoError(t, err)
			// Sunday is also the DST transition in New York. Both dimensions must
			// retain the earlier same-window charge after a reset by the later one.
			boundary := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
			tx, err := integrationDB.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback() }()
			_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_build_object(
 'quota_used',10,'quota_daily_limit',100,'quota_weekly_limit',100,'quota_daily_used',90,'quota_weekly_used',90,
 'quota_daily_reset_mode','fixed','quota_weekly_reset_mode','fixed','quota_reset_timezone',$2::text,
 'quota_weekly_reset_day',0,'quota_daily_reset_at',$3::text,'quota_weekly_reset_at',$3::text,
 'quota_daily_start',$4::text,'quota_weekly_start',$4::text) WHERE id=$1`, s.Command.AccountID, zone, boundary.Format(time.RFC3339), boundary.AddDate(0, 0, -7).Format(time.RFC3339))
			require.NoError(t, err)
			cmd := s.Command
			cmd.AccountQuotaCost = 1.25
			for _, at := range []time.Time{boundary.Add(11 * time.Hour), boundary.Add(10 * time.Hour)} {
				cmd.EffectiveAt = at
				_, err = incrementSettlementAccountQuota(ctx, tx, &cmd)
				require.NoError(t, err)
			}
			var total, daily, weekly float64
			var ds, ws time.Time
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric,(extra->>'quota_daily_used')::numeric,(extra->>'quota_weekly_used')::numeric,(extra->>'quota_daily_start')::timestamptz,(extra->>'quota_weekly_start')::timestamptz FROM accounts WHERE id=$1`, cmd.AccountID).Scan(&total, &daily, &weekly, &ds, &ws))
			require.Equal(t, []float64{12.5, 2.5, 2.5}, []float64{total, daily, weekly})
			require.True(t, boundary.Equal(ds))
			require.True(t, boundary.Equal(ws))
			cmd.EffectiveAt = boundary.Add(-time.Minute)
			state, err := incrementSettlementAccountQuota(ctx, tx, &cmd)
			require.NoError(t, err)
			require.Equal(t, []float64{13.75, 2.5, 2.5}, []float64{state.TotalUsed, state.DailyUsed, state.WeeklyUsed})
		})
	}
}

func TestUsageSettlementSubscriptionAdvancesBeforeIncrement(t *testing.T) {
	ctx := context.Background()
	_, s := settlementFixture(t)
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformGemini, SubscriptionType: service.SubscriptionTypeSubscription})
	sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: s.Command.UserID, GroupID: group.ID})
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	week := day.Add(-7 * 24 * time.Hour)
	month := day.Add(-30 * 24 * time.Hour)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE user_subscriptions SET starts_at=$2,expires_at=$3,daily_window_start=$4,weekly_window_start=$5,monthly_window_start=$2,daily_usage_usd=90,weekly_usage_usd=90,monthly_usage_usd=90 WHERE id=$1`, sub.ID, month, day.AddDate(0, 2, 0), day.AddDate(0, 0, -1), week)
	require.NoError(t, err)
	// First charge crosses all three boundaries; the second replays earlier in
	// the same window. A stale preflight CAS reset must then leave both intact.
	require.NoError(t, incrementSettlementSubscription(ctx, tx, sub.ID, 1.25, day.Add(2*time.Minute)))
	require.NoError(t, incrementSettlementSubscription(ctx, tx, sub.ID, 1.25, day.Add(time.Minute)))
	_, err = tx.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=0,daily_window_start=$2 WHERE id=$1 AND daily_window_start=$3`, sub.ID, day, day.AddDate(0, 0, -1))
	require.NoError(t, err)
	var d, w, m float64
	var ds, ws, ms time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&d, &w, &m, &ds, &ws, &ms))
	require.Equal(t, []float64{2.5, 2.5, 2.5}, []float64{d, w, m})
	for _, start := range []time.Time{ds, ws, ms} {
		require.True(t, day.Equal(start))
	}
}

func TestUsageSettlementFixedAccountBeforeMissingDSTHour(t *testing.T) {
	ctx := context.Background()
	_, s := settlementFixture(t)
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	day := time.Date(2026, 3, 7, 2, 0, 0, 0, loc)
	week := time.Date(2026, 3, 1, 2, 0, 0, 0, loc)
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_build_object(
 'quota_used',0,'quota_daily_limit',100,'quota_weekly_limit',100,'quota_daily_used',90,'quota_weekly_used',90,
 'quota_daily_reset_mode','fixed','quota_weekly_reset_mode','fixed','quota_reset_timezone','America/New_York',
 'quota_daily_reset_hour',2,'quota_weekly_reset_hour',2,'quota_weekly_reset_day',0,
 'quota_daily_reset_at',$2::text,'quota_weekly_reset_at',$3::text) WHERE id=$1`, s.Command.AccountID, day.Format(time.RFC3339), week.Format(time.RFC3339))
	require.NoError(t, err)
	cmd := s.Command
	cmd.AccountQuotaCost = 1.25
	for _, at := range []time.Time{day.Add(20 * time.Minute), day.Add(10 * time.Minute)} {
		cmd.EffectiveAt = at
		_, err = incrementSettlementAccountQuota(ctx, tx, &cmd)
		require.NoError(t, err)
	}
	var d, w float64
	var ds, ws time.Time
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT (extra->>'quota_daily_used')::numeric,(extra->>'quota_weekly_used')::numeric,(extra->>'quota_daily_start')::timestamptz,(extra->>'quota_weekly_start')::timestamptz FROM accounts WHERE id=$1`, cmd.AccountID).Scan(&d, &w, &ds, &ws))
	require.Equal(t, []float64{2.5, 2.5}, []float64{d, w})
	require.True(t, day.Equal(ds), "daily start %v", ds)
	require.True(t, week.Equal(ws), "weekly start %v", ws)
}

func TestUsageSettlementQuotaModeSurvivesIntakeDisable(t *testing.T) {
	ctx := context.Background()
	// An isolated schema models restart without changing other fixtures.
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	tx, err := conn.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE settings(key text PRIMARY KEY,value text,updated_at timestamptz) ON COMMIT DROP; CREATE TEMP TABLE usage_settlements(state text) ON COMMIT DROP`)
	require.NoError(t, err)
	for _, step := range []struct{ intake, flusher, want, fail bool }{
		{false, false, false, false}, {true, true, false, true}, {true, false, true, false}, {false, false, true, false}, {false, true, false, true},
	} {
		mode, err := initializeUsageSettlementQuotaMode(ctx, tx, step.intake, step.flusher)
		if step.fail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, step.want, mode)
		}
	}
}

// Compile-time check that a transaction can isolate mode initialization tests.
var _ settlementModeDB = (*sql.Tx)(nil)

func settlementFixture(t *testing.T) (*usageBillingRepository, *service.UsageSettlementSnapshot) {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@settlement.test", PasswordHash: "hash", Balance: 100})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "settlement"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Type: service.AccountTypeAPIKey})
	now := time.Now().UTC()
	id := "settlement:" + uuid.NewString()
	cmd := service.UsageBillingCommand{RequestID: id, APIKeyID: key.ID, UserID: user.ID, AccountID: account.ID, AccountType: account.Type, Model: "gemini-image", BalanceCost: 1.25, APIKeyQuotaCost: 1.25}
	cmd.EffectiveAt = now
	cmd.EffectiveDayStart = timezone.StartOfDay(now)
	cmd.PlatformQuota = &service.UsageBillingPlatformQuota{Platform: service.PlatformGemini, Cost: 1.25, OccurredAt: now, DayStart: timezone.StartOfDay(now), WeekStart: timezone.StartOfWeek(now)}
	cmd.Normalize()
	s := &service.UsageSettlementSnapshot{Version: 1, Command: cmd, Platform: service.PlatformGemini, PlatformCost: 1.25, DayStart: timezone.StartOfDay(now), WeekStart: timezone.StartOfWeek(now), Usage: service.UsageLog{
		RequestID: id, UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, Model: "gemini-image", ImageCount: 1, TotalCost: 1.25, ActualCost: 1.25, RateMultiplier: 1, CreatedAt: now,
	}}
	r := &usageBillingRepository{db: integrationDB}
	require.NoError(t, r.BeginSettlement(ctx, id, key.ID, user.ID))
	return r, s
}
func TestUsageSettlementAtomicRecovery(t *testing.T) {
	ctx := context.Background()
	r, s := settlementFixture(t)
	ref := service.UsageSettlementRef{RequestID: s.Command.RequestID, APIKeyID: s.Command.APIKeyID}
	_, _, err := r.ApplySettlement(ctx, ref, nil)
	require.ErrorIs(t, err, service.ErrUsageSettlementNotReady)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id,platform,daily_limit_usd) VALUES($1,$2,100)`, s.Command.UserID, s.Platform)
	require.NoError(t, err)
	require.NoError(t, r.PrepareSettlement(ctx, s))
	require.NoError(t, r.PrepareSettlement(ctx, s))
	altered := *s
	altered.Command.BalanceCost = 50
	require.ErrorIs(t, r.PrepareSettlement(ctx, &altered), service.ErrUsageSettlementConflict)
	// Simulate process loss after ready: another repository uses only stored facts.
	replay := &usageBillingRepository{db: integrationDB}
	saved, result, err := replay.ApplySettlement(ctx, ref, nil)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.Equal(t, s.Command.RequestFingerprint, saved.Command.RequestFingerprint)
	// Simulate commit succeeded but the caller lost the reply/ack.
	_, result, err = replay.ApplySettlement(ctx, ref, nil)
	require.NoError(t, err)
	require.False(t, result.Applied)
	var balance, actual, platform float64
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", s.Command.UserID).Scan(&balance))
	require.Equal(t, 98.75, balance)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*),max(actual_cost) FROM usage_logs WHERE request_id=$1 AND api_key_id=$2", ref.RequestID, ref.APIKeyID).Scan(&count, &actual))
	require.Equal(t, 1, count)
	require.Equal(t, 1.25, actual)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2", s.Command.UserID, s.Platform).Scan(&platform))
	require.Equal(t, 1.25, platform)
	require.NoError(t, r.CompleteSettlement(ctx, ref))
	require.NoError(t, r.CompleteSettlement(ctx, ref))
}
func TestUsageSettlementRollbackAndReview(t *testing.T) {
	ctx := context.Background()
	r, s := settlementFixture(t)
	ref := service.UsageSettlementRef{RequestID: s.Command.RequestID, APIKeyID: s.Command.APIKeyID}
	// A missing account makes the usage-log FK fail after balance SQL. The whole
	// transaction, including dedup, must roll back and remain retryable.
	s.Command.AccountID = -123
	s.Usage.AccountID = -123
	require.NoError(t, r.PrepareSettlement(ctx, s))
	_, _, err := r.ApplySettlement(ctx, ref, nil)
	require.Error(t, err)
	var balance float64
	var count int
	var state string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id=$1", s.Command.UserID).Scan(&balance))
	require.Equal(t, 100.0, balance)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1 AND api_key_id=$2", ref.RequestID, ref.APIKeyID).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT state FROM usage_settlements WHERE request_id=$1", ref.RequestID).Scan(&state))
	require.Equal(t, "ready", state)
	_, err = integrationDB.ExecContext(ctx, `UPDATE usage_settlements SET snapshot=jsonb_set(snapshot,'{Command,BalanceCost}','999') WHERE request_id=$1`, ref.RequestID)
	require.ErrorContains(t, err, "immutable")
	_, unknown := settlementFixture(t)
	_, err = integrationDB.ExecContext(ctx, "UPDATE usage_settlements SET created_at=now()-interval '3 hours' WHERE request_id=$1", unknown.Command.RequestID)
	require.NoError(t, err)
	n, err := r.ReviewExpiredSettlements(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1))
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT state FROM usage_settlements WHERE request_id=$1", unknown.Command.RequestID).Scan(&state))
	require.Equal(t, "needs_review", state)
	_, _, err = r.ApplySettlement(ctx, service.UsageSettlementRef{RequestID: unknown.Command.RequestID, APIKeyID: unknown.Command.APIKeyID}, nil)
	require.ErrorIs(t, err, service.ErrUsageSettlementNotReady)
}
func TestUsageSettlementLatePlatformWindows(t *testing.T) {
	ctx := context.Background()
	r, s := settlementFixture(t)
	newer := s.Usage.CreatedAt.Add(48 * time.Hour)
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO user_platform_quotas(user_id,platform,daily_usage_usd,weekly_usage_usd,monthly_usage_usd,daily_window_start,weekly_window_start,monthly_window_start) VALUES($1,$2,5,6,7,$3,$3,$3)`, s.Command.UserID, s.Platform, newer)
	require.NoError(t, err)
	require.NoError(t, r.PrepareSettlement(ctx, s))
	_, _, err = r.ApplySettlement(ctx, service.UsageSettlementRef{RequestID: s.Command.RequestID, APIKeyID: s.Command.APIKeyID}, nil)
	require.NoError(t, err)
	var d, w, m float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_platform_quotas WHERE user_id=$1 AND platform=$2", s.Command.UserID, s.Platform).Scan(&d, &w, &m))
	require.Equal(t, "5/6/7", fmt.Sprintf("%g/%g/%g", d, w, m))
}

func TestUsageSettlementPreservesNewKeyAndAccountWindows(t *testing.T) {
	ctx := context.Background()
	r, s := settlementFixture(t)
	s.Command.APIKeyRateLimitCost = 1.25
	s.Command.AccountQuotaCost = 1.25
	s.Command.AccountQuotaHasDailyLimit = true
	s.Command.AccountQuotaHasWeeklyLimit = true
	newer := s.Usage.CreatedAt.Add(time.Hour)
	_, err := integrationDB.ExecContext(ctx, `UPDATE api_keys SET usage_5h=5,usage_1d=6,usage_7d=7,window_5h_start=$2,window_1d_start=$2,window_7d_start=$2 WHERE id=$1`, s.Command.APIKeyID, newer)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_build_object('quota_limit',100,'quota_used',10,'quota_daily_limit',100,'quota_weekly_limit',100,'quota_daily_used',5,'quota_weekly_used',6,'quota_daily_start',$2::text,'quota_weekly_start',$2::text) WHERE id=$1`, s.Command.AccountID, newer.Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, r.PrepareSettlement(ctx, s))
	_, _, err = r.ApplySettlement(ctx, service.UsageSettlementRef{RequestID: s.Command.RequestID, APIKeyID: s.Command.APIKeyID}, nil)
	require.NoError(t, err)
	var a, b, c float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT usage_5h,usage_1d,usage_7d FROM api_keys WHERE id=$1`, s.Command.APIKeyID).Scan(&a, &b, &c))
	require.Equal(t, []float64{5, 6, 7}, []float64{a, b, c})
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (extra->>'quota_used')::numeric,(extra->>'quota_daily_used')::numeric,(extra->>'quota_weekly_used')::numeric FROM accounts WHERE id=$1`, s.Command.AccountID).Scan(&a, &b, &c))
	require.Equal(t, []float64{11.25, 5, 6}, []float64{a, b, c})
}

func TestUsageSettlementPreservesNewSubscriptionWindows(t *testing.T) {
	ctx := context.Background()
	r, s := settlementFixture(t)
	client := testEntClient(t)
	group := mustCreateGroup(t, client, &service.Group{Name: uuid.NewString(), Platform: service.PlatformGemini, SubscriptionType: service.SubscriptionTypeSubscription})
	sub := mustCreateSubscription(t, client, &service.UserSubscription{UserID: s.Command.UserID, GroupID: group.ID})
	s.Command.BalanceCost = 0
	s.Command.SubscriptionID = &sub.ID
	s.Command.SubscriptionCost = 1.25
	s.Command.PlatformQuota = nil
	s.Usage.SubscriptionID = &sub.ID
	s.Usage.GroupID = &group.ID
	_, err := integrationDB.ExecContext(ctx, `UPDATE user_subscriptions SET daily_usage_usd=5,weekly_usage_usd=6,monthly_usage_usd=7,daily_window_start=$2,weekly_window_start=$2,monthly_window_start=$2 WHERE id=$1`, sub.ID, s.Usage.CreatedAt.Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, r.PrepareSettlement(ctx, s))
	_, _, err = r.ApplySettlement(ctx, service.UsageSettlementRef{RequestID: s.Command.RequestID, APIKeyID: s.Command.APIKeyID}, nil)
	require.NoError(t, err)
	var d, w, m float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT daily_usage_usd,weekly_usage_usd,monthly_usage_usd FROM user_subscriptions WHERE id=$1`, sub.ID).Scan(&d, &w, &m))
	require.Equal(t, []float64{5, 6, 7}, []float64{d, w, m})
}
