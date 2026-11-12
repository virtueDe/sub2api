package repository

import (
	"context"
	"database/sql"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"math"
	"strings"
	"time"
)

const activeImageReservationStates = "('created','ready','needs_review','release_pending')"

func (r *usageBillingRepository) MarkImageSettlementDispatched(ctx context.Context, ref service.UsageSettlementRef) error {
	result, err := r.db.ExecContext(ctx, `UPDATE usage_settlements SET dispatched_at=COALESCE(dispatched_at,now()),updated_at=now() WHERE request_id=$1 AND api_key_id=$2 AND state='created' AND reserved_amount IS NOT NULL`, ref.RequestID, ref.APIKeyID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrUsageSettlementConflict
	}
	return nil
}

func (r *usageBillingRepository) ReserveImageSettlement(ctx context.Context, ref service.UsageSettlementRef, userID int64, subID *int64, amount float64) error {
	if !strings.HasPrefix(ref.RequestID, "settlement:") || ref.APIKeyID <= 0 || userID <= 0 || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return service.ErrUsageSettlementConflict
	}
	amount = service.QuantizeUsageBillingAmount(amount)
	tx, release, err := beginPayerBillingTransaction(ctx, r.db, userID, false)
	if err != nil {
		return err
	}
	defer release()
	defer func() { _ = tx.Rollback() }()
	// Serialize payer/quota changes without blocking usage-log foreign-key checks.
	// These operations never change the referenced parent keys.
	if subID != nil {
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM user_subscriptions WHERE id=$1 FOR NO KEY UPDATE`, *subID).Scan(&id); err != nil {
			return err
		}
	}
	var balance float64
	if err := tx.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' FOR NO KEY UPDATE`, userID).Scan(&balance); err != nil {
		return err
	}
	var oldAmount sql.NullFloat64
	var oldSub sql.NullInt64
	var oldUser int64
	var state string
	err = tx.QueryRowContext(ctx, `SELECT reserved_amount,reserved_subscription_id,user_id,state FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID).Scan(&oldAmount, &oldSub, &oldUser, &state)
	if err == nil {
		if oldAmount.Valid && oldAmount.Float64 == amount && oldUser == userID && oldSub.Valid == (subID != nil) && (subID == nil || oldSub.Int64 == *subID) && state != "released" && state != "release_pending" {
			return nil
		}
		return service.ErrUsageSettlementConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if subID != nil {
		if err := reserveImageSubscription(ctx, tx, *subID, userID, ref.APIKeyID, amount); err != nil {
			return err
		}
	}
	var quota, used float64
	err = tx.QueryRowContext(ctx, `SELECT quota,quota_used FROM api_keys WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL AND status='active' AND (expires_at IS NULL OR expires_at>now()) FOR NO KEY UPDATE`, ref.APIKeyID, userID).Scan(&quota, &used)
	if err != nil {
		return err
	}
	if quota > 0 {
		var held float64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(reserved_amount),0) FROM usage_settlements WHERE api_key_id=$1 AND reserved_amount IS NOT NULL AND state IN `+activeImageReservationStates, ref.APIKeyID).Scan(&held); err != nil {
			return err
		}
		if service.QuantizeUsageBillingAmount(used+held+amount) > quota {
			return service.ErrAPIKeyQuotaExhausted
		}
	}
	if subID == nil {
		if balance < amount {
			return service.ErrInsufficientBalance
		}
		if _, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance-$2,frozen_balance=COALESCE(frozen_balance,0)+$2,updated_at=now() WHERE id=$1`, userID, amount); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_settlements(request_id,api_key_id,user_id,reserved_amount,reserved_subscription_id) VALUES($1,$2,$3,$4,$5)`, ref.RequestID, ref.APIKeyID, userID, amount, subID); err != nil {
		return err
	}
	return tx.Commit()
}

func reserveImageSubscription(ctx context.Context, tx *sql.Tx, id, userID, keyID int64, amount float64) error {
	var sub service.UserSubscription
	var dl, wl, ml sql.NullFloat64
	err := tx.QueryRowContext(ctx, `SELECT us.starts_at,us.expires_at,us.daily_window_start,us.weekly_window_start,us.monthly_window_start,
 us.daily_usage_usd,us.weekly_usage_usd,us.monthly_usage_usd,g.daily_limit_usd,g.weekly_limit_usd,g.monthly_limit_usd
 FROM user_subscriptions us JOIN groups g ON g.id=us.group_id
 WHERE us.id=$1 AND us.user_id=$2 AND us.status='active' AND us.starts_at<=now() AND us.expires_at>now() AND us.deleted_at IS NULL AND g.deleted_at IS NULL
 AND EXISTS(SELECT 1 FROM api_keys k WHERE k.id=$3 AND k.group_id=us.group_id AND k.user_id=us.user_id) FOR NO KEY UPDATE OF us`, id, userID, keyID).
		Scan(&sub.StartsAt, &sub.ExpiresAt, &sub.DailyWindowStart, &sub.WeeklyWindowStart, &sub.MonthlyWindowStart, &sub.DailyUsageUSD, &sub.WeeklyUsageUSD, &sub.MonthlyUsageUSD, &dl, &wl, &ml)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrSubscriptionNotFound
	}
	if err != nil {
		return err
	}
	sub.StartsAt, sub.ExpiresAt = sub.StartsAt.In(timezone.Location()), sub.ExpiresAt.In(timezone.Location())
	now := time.Now()
	if sub.NeedsDailyResetAt(now) {
		sub.DailyUsageUSD = 0
	}
	if sub.NeedsWeeklyResetAt(now) {
		sub.WeeklyUsageUSD = 0
	}
	if sub.NeedsMonthlyResetAt(now) {
		sub.MonthlyUsageUSD = 0
	}
	var held float64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(reserved_amount),0) FROM usage_settlements WHERE reserved_subscription_id=$1 AND state IN `+activeImageReservationStates, id).Scan(&held); err != nil {
		return err
	}
	// Unknown old holds remain reserved across a quota window reset.
	for _, w := range []struct {
		limit sql.NullFloat64
		used  float64
		err   error
	}{{dl, sub.DailyUsageUSD, service.ErrDailyLimitExceeded}, {wl, sub.WeeklyUsageUSD, service.ErrWeeklyLimitExceeded}, {ml, sub.MonthlyUsageUSD, service.ErrMonthlyLimitExceeded}} {
		if w.limit.Valid && w.limit.Float64 > 0 && service.QuantizeUsageBillingAmount(w.used+held+amount) > w.limit.Float64 {
			return w.err
		}
	}
	return nil
}

// Persist the refund decision separately so a database timeout is retryable.
// Late failure journals may release missing-facts reviews, never finalized bills.
func (r *usageBillingRepository) ReleaseImageSettlement(ctx context.Context, ref service.UsageSettlementRef) error {
	payer, err := r.settlementPayer(ctx, ref)
	if err != nil {
		return err
	}
	leave, err := sharedBillingPayerGate.acquire(ctx, billingPayerKey{db: r.db, userID: payer}, false)
	if err != nil {
		return err
	}
	defer leave()
	// The decision must survive a later refund failure, but must not itself
	// queue for a settlement row lock while bypassing payer admission.
	_, err = r.db.ExecContext(ctx, `UPDATE usage_settlements SET state='release_pending',last_error_code='image_request_failed',updated_at=now() WHERE request_id=$1 AND api_key_id=$2 AND (state='created' OR (state='needs_review' AND last_error_code='final_facts_missing')) AND snapshot IS NULL AND reserved_amount IS NOT NULL`, ref.RequestID, ref.APIKeyID)
	if err != nil {
		return err
	}
	tx, release, err := beginUsageBillingTransaction(ctx, r.db)
	if err != nil {
		return err
	}
	defer release()
	defer func() { _ = tx.Rollback() }()
	var state string
	var amount sql.NullFloat64
	var sub sql.NullInt64
	var userID int64
	if err = tx.QueryRowContext(ctx, `SELECT state,reserved_amount,reserved_subscription_id,user_id FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2 FOR UPDATE`, ref.RequestID, ref.APIKeyID).Scan(&state, &amount, &sub, &userID); err != nil {
		return err
	}
	if userID != payer {
		return service.ErrUsageSettlementConflict
	}
	if state == "released" {
		return nil
	}
	if state != "release_pending" || !amount.Valid {
		return service.ErrUsageSettlementConflict
	}
	if !sub.Valid {
		result, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$2,frozen_balance=frozen_balance-$2,updated_at=now() WHERE id=$1 AND deleted_at IS NULL AND frozen_balance >= $2`, userID, amount.Float64)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return service.ErrUsageSettlementConflict
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_settlements SET state='released',last_error_code='cache_invalidation_pending',updated_at=now() WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID); err != nil {
		return err
	}
	return tx.Commit()
}
func (r *usageBillingRepository) RetryImageSettlementRelease(ctx context.Context, invalidate func(context.Context, int64) error) error {
	// A stalled admission may be cancelled only before the dispatch fence.
	// The generator must successfully cross that fence before contacting upstream.
	if _, err := r.db.ExecContext(ctx, `WITH stale AS (SELECT id FROM usage_settlements WHERE state='created' AND reserved_amount IS NOT NULL AND dispatched_at IS NULL AND created_at<now()-interval '10 minutes' ORDER BY created_at,id LIMIT 10 FOR UPDATE SKIP LOCKED)
 UPDATE usage_settlements s SET state='release_pending',last_error_code='not_dispatched',updated_at=now() FROM stale WHERE s.id=stale.id`); err != nil {
		return err
	}
	var ref service.UsageSettlementRef
	var userID int64
	// Rotate failed cache invalidations as well, so one Redis failure cannot
	// prevent other users' pending database refunds from making progress.
	err := r.db.QueryRowContext(ctx, `WITH candidate AS (
 SELECT id FROM usage_settlements WHERE state='release_pending' OR (state='released' AND last_error_code='cache_invalidation_pending')
 ORDER BY updated_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE usage_settlements s SET updated_at=now() FROM candidate c WHERE s.id=c.id
 RETURNING s.request_id,s.api_key_id,s.user_id`).Scan(&ref.RequestID, &ref.APIKeyID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = r.ReleaseImageSettlement(ctx, ref); err != nil {
		return err
	}
	if err = invalidate(ctx, userID); err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE usage_settlements SET last_error_code=NULL WHERE request_id=$1 AND api_key_id=$2 AND state='released' AND last_error_code='cache_invalidation_pending'`, ref.RequestID, ref.APIKeyID)
	return err
}
