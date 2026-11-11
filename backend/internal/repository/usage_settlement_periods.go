package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func incrementSettlementSubscription(ctx context.Context, tx *sql.Tx, id int64, cost float64, at time.Time) error {
	var sub service.UserSubscription
	err := tx.QueryRowContext(ctx, `SELECT us.starts_at,us.expires_at,us.daily_window_start,us.weekly_window_start,us.monthly_window_start
 FROM user_subscriptions us JOIN groups g ON g.id=us.group_id
 WHERE us.id=$1 AND us.deleted_at IS NULL AND g.deleted_at IS NULL FOR NO KEY UPDATE OF us`, id).
		Scan(&sub.StartsAt, &sub.ExpiresAt, &sub.DailyWindowStart, &sub.WeeklyWindowStart, &sub.MonthlyWindowStart)
	if err == sql.ErrNoRows {
		return service.ErrSubscriptionNotFound
	}
	if err != nil {
		return err
	}
	sub.StartsAt = sub.StartsAt.In(timezone.Location())
	sub.ExpiresAt = sub.ExpiresAt.In(timezone.Location())
	daily, weekly, monthly := sub.BillingWindowStartsAt(at)
	result, err := tx.ExecContext(ctx, `UPDATE user_subscriptions us SET
 daily_usage_usd=CASE WHEN daily_window_start>$3 THEN daily_usage_usd WHEN daily_window_start<$4 THEN $1 ELSE daily_usage_usd+$1 END,
 weekly_usage_usd=CASE WHEN weekly_window_start>$3 THEN weekly_usage_usd WHEN weekly_window_start<$5 THEN $1 ELSE weekly_usage_usd+$1 END,
 monthly_usage_usd=CASE WHEN monthly_window_start>$3 THEN monthly_usage_usd WHEN monthly_window_start<$6 THEN $1 ELSE monthly_usage_usd+$1 END,
 daily_window_start=$4,weekly_window_start=$5,monthly_window_start=$6,
 updated_at=now() WHERE us.id=$2`, cost, id, at, daily, weekly, monthly)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrSubscriptionNotFound
	}
	return nil
}

func incrementSettlementKeyWindows(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	sets := make([]string, 0, 7)
	for _, w := range []struct{ name, span, start string }{{"5h", "5 hours", "$3"}, {"1d", "24 hours", "$4"}, {"7d", "7 days", "$4"}} {
		used, start := "usage_"+w.name, "window_"+w.name+"_start"
		sets = append(sets, fmt.Sprintf(`%s=CASE WHEN %s>$3 THEN %s WHEN %s IS NOT NULL AND %s+interval '%s'<=$3 THEN $1 ELSE %s+$1 END`, used, start, used, start, start, w.span, used))
		sets = append(sets, fmt.Sprintf(`%s=CASE WHEN %s IS NULL OR %s+interval '%s'<=$3 THEN %s ELSE %s END`, start, start, start, w.span, w.start, start))
	}
	sets = append(sets, "updated_at=now()")
	result, err := tx.ExecContext(ctx, "UPDATE api_keys SET "+strings.Join(sets, ",")+" WHERE id=$2 AND deleted_at IS NULL", cmd.APIKeyRateLimitCost, cmd.APIKeyID, cmd.EffectiveAt, cmd.EffectiveDayStart)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

// Reuse the existing fixed/rolling quota policy with the persisted event clock,
// while protecting each newer window from an older replay. Total quota is never
// skipped. Account settings still determine whether a dimension is configured.
func incrementSettlementAccountQuota(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (*service.AccountQuotaState, error) {
	set := accountQuotaTotalIncrementExpr
	for _, dimension := range []struct{ name, span, expired, next string }{
		{"daily", "1 day", dailyExpiredExpr, nextDailyResetAtExpr},
		{"weekly", "7 days", weeklyExpiredExpr, nextWeeklyResetAtExpr},
	} {
		prefix := "quota_" + dimension.name
		fixed := fmt.Sprintf(`COALESCE(extra->>'%s_reset_mode','rolling')='fixed'`, prefix)
		// Subtract in the configured local calendar, then convert back to UTC.
		// This preserves fixed boundaries across DST and late same-window replay.
		previousBoundary := func(next string) string {
			// Reconstruct the configured hour: a nonexistent 02:00 may have
			// normalized to 03:00 in next, but the previous day was still 02:00.
			return "(date_trunc('day', (" + next + ")::timestamptz AT TIME ZONE COALESCE(extra->>'quota_reset_timezone','UTC')) - interval '" + dimension.span + "' + (COALESCE((extra->>'" + prefix + "_reset_hour')::int,0) || ' hours')::interval) AT TIME ZONE COALESCE(extra->>'quota_reset_timezone','UTC')"
		}
		stored := fmt.Sprintf(`(extra->>'%s_start')::timestamptz`, prefix)
		stored = fmt.Sprintf(`CASE WHEN %s THEN COALESCE(%s,%s) ELSE %s END`, fixed, previousBoundary("extra->>'"+prefix+"_reset_at'"), stored, stored)
		eventStart := fmt.Sprintf(`CASE WHEN %s THEN %s ELSE NOW() END`, fixed, previousBoundary(dimension.next))
		start := fmt.Sprintf(`CASE WHEN %s THEN (%s) ELSE COALESCE((%s),(%s)) END`, dimension.expired, eventStart, stored, eventStart)
		set += fmt.Sprintf(` || CASE WHEN COALESCE((extra->>'%[1]s_limit')::numeric,0)>0 AND NOT COALESCE((%[2]s)>$3,false) THEN
 jsonb_build_object('%[1]s_used',CASE WHEN %[3]s THEN $1 ELSE COALESCE((extra->>'%[1]s_used')::numeric,0)+$1 END,
 '%[1]s_start',to_char((%[4]s) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'))
 || CASE WHEN %[3]s AND (%[5]s) IS NOT NULL THEN jsonb_build_object('%[1]s_reset_at',%[5]s) ELSE '{}'::jsonb END
 ELSE '{}'::jsonb END`, prefix, stored, dimension.expired, start, dimension.next)
	}
	set = strings.ReplaceAll(set, "NOW()", "$3::timestamptz")
	var state service.AccountQuotaState
	err := tx.QueryRowContext(ctx, "UPDATE accounts SET extra=("+set+") ,updated_at=now() WHERE id=$2 AND deleted_at IS NULL"+accountQuotaIncrementReturning, cmd.AccountQuotaCost, cmd.AccountID, cmd.EffectiveAt).Scan(&state.TotalUsed, &state.TotalLimit, &state.DailyUsed, &state.DailyLimit, &state.WeeklyUsed, &state.WeeklyLimit)
	if err == sql.ErrNoRows {
		return nil, service.ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &cmd.AccountID, nil, nil); err != nil {
		return nil, err
	}
	return &state, nil
}
