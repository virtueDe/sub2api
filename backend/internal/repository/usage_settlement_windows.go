package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func incrementSettlementSubscription(ctx context.Context, tx *sql.Tx, id int64, cost float64, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE user_subscriptions us SET
 daily_usage_usd=CASE WHEN daily_window_start>$3 THEN daily_usage_usd ELSE daily_usage_usd+$1 END,
 weekly_usage_usd=CASE WHEN weekly_window_start>$3 THEN weekly_usage_usd ELSE weekly_usage_usd+$1 END,
 monthly_usage_usd=CASE WHEN monthly_window_start>$3 THEN monthly_usage_usd ELSE monthly_usage_usd+$1 END,
 updated_at=now() FROM groups g WHERE us.id=$2 AND us.deleted_at IS NULL AND g.id=us.group_id AND g.deleted_at IS NULL`, cost, id, at)
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
	for _, dimension := range []struct{ start, expr string }{{"quota_daily_start", accountQuotaDailyIncrementExpr}, {"quota_weekly_start", accountQuotaWeeklyIncrementExpr}} {
		set += fmt.Sprintf(` || CASE WHEN (extra->>'%s')::timestamptz>$3 THEN '{}'::jsonb ELSE ('{}'::jsonb %s) END`, dimension.start, dimension.expr)
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
