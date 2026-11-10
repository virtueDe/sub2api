package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/dbstats"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageBillingRepository) BeginSettlement(ctx context.Context, id string, keyID, userID int64) (err error) {
	started := time.Now()
	defer func() {
		dbstats.Record("settlement_begin", time.Since(started), err)
		if err != nil {
			dbstats.TriggerBillingFailure()
		}
	}()
	if !strings.HasPrefix(id, "settlement:") || keyID <= 0 || userID <= 0 {
		return service.ErrUsageSettlementConflict
	}
	result, err := r.db.ExecContext(ctx, `INSERT INTO usage_settlements(request_id,api_key_id,user_id)
 SELECT $1,id,user_id FROM api_keys WHERE id=$2 AND user_id=$3 AND deleted_at IS NULL AND status='active'
 AND (quota<=0 OR quota_used<quota) AND (expires_at IS NULL OR expires_at>now())`, id, keyID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func validateSettlement(s *service.UsageSettlementSnapshot) error {
	if s == nil || s.Version != 1 || !strings.HasPrefix(s.Command.RequestID, "settlement:") || s.Command.RequestFingerprint == "" ||
		s.Command.RequestID != s.Usage.RequestID || s.Command.APIKeyID != s.Usage.APIKeyID || s.Command.UserID != s.Usage.UserID ||
		s.Command.AccountID != s.Usage.AccountID || s.Usage.CreatedAt.IsZero() || s.DayStart.IsZero() || s.WeekStart.IsZero() || !s.Command.EffectiveAt.Equal(s.Usage.CreatedAt) || s.Command.EffectiveDayStart.IsZero() {
		return service.ErrUsageSettlementConflict
	}
	return nil
}
func (r *usageBillingRepository) PrepareSettlement(ctx context.Context, s *service.UsageSettlementSnapshot) (err error) {
	started := time.Now()
	defer func() {
		dbstats.Record("settlement_prepare", time.Since(started), err)
		if err != nil {
			dbstats.TriggerBillingFailure()
		}
	}()
	if err := validateSettlement(s); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("serialize settlement: %w", err)
	}
	if len(data) > 60000 {
		return fmt.Errorf("%w: snapshot exceeds 60000 bytes", service.ErrUsageSettlementConflict)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE usage_settlements SET snapshot=$4::jsonb,state='ready',updated_at=now(),next_attempt_at=now()
 WHERE request_id=$1 AND api_key_id=$2 AND user_id=$3 AND (state='created' OR (state='needs_review' AND last_error_code='final_facts_missing')) AND snapshot IS NULL`, s.Command.RequestID, s.Command.APIKeyID, s.Command.UserID, string(data))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var same bool
	err = r.db.QueryRowContext(ctx, `SELECT COALESCE(snapshot=$4::jsonb,false) AND state IN ('ready','charged','complete') FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2 AND user_id=$3`, s.Command.RequestID, s.Command.APIKeyID, s.Command.UserID, string(data)).Scan(&same)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !same {
		return service.ErrUsageSettlementConflict
	}
	return err
}

func (r *usageBillingRepository) ApplySettlement(ctx context.Context, ref service.UsageSettlementRef, detail *service.ImageGenerationDetail) (_ *service.UsageSettlementSnapshot, _ *service.UsageBillingApplyResult, err error) {
	started := time.Now()
	defer func() {
		dbstats.Record("settlement", time.Since(started), err)
		if err != nil {
			dbstats.TriggerBillingFailure()
		}
	}()
	tx, release, err := beginUsageBillingTransaction(ctx, r.db)
	if err != nil {
		return nil, nil, usageBillingStageError("settlement_begin", started, err)
	}
	defer release()
	defer func() { _ = tx.Rollback() }()
	var data []byte
	var state string
	var reserved sql.NullFloat64
	var reservedSub sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT snapshot,state,reserved_amount,reserved_subscription_id FROM usage_settlements WHERE request_id=$1 AND api_key_id=$2 FOR UPDATE`, ref.RequestID, ref.APIKeyID).Scan(&data, &state, &reserved, &reservedSub); err != nil {
		return nil, nil, err
	}
	if state != "ready" && state != "charged" && state != "complete" {
		return nil, nil, service.ErrUsageSettlementNotReady
	}
	var snapshot service.UsageSettlementSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return nil, nil, service.ErrUsageSettlementConflict
	}
	if err = validateSettlement(&snapshot); err != nil {
		return nil, nil, err
	}
	if snapshot.Command.RequestID != ref.RequestID || snapshot.Command.APIKeyID != ref.APIKeyID {
		return nil, nil, service.ErrUsageSettlementConflict
	}
	result := &service.UsageBillingApplyResult{}
	if state == "charged" || state == "complete" {
		return &snapshot, result, nil
	}
	applied, err := r.claimUsageBillingKey(ctx, tx, &snapshot.Command)
	if err != nil {
		return nil, nil, err
	}
	result.Applied = applied
	if applied {
		command := snapshot.Command
		if reserved.Valid {
			if reservedSub.Valid != (command.SubscriptionID != nil) || (reservedSub.Valid && reservedSub.Int64 != *command.SubscriptionID) {
				return nil, nil, service.ErrUsageSettlementConflict
			}
			if !reservedSub.Valid {
				var balance float64
				// Variable token charges can exceed an estimate. Retain the real
				// liability (including overdraft) instead of dropping the bill.
				err = tx.QueryRowContext(ctx, `UPDATE users SET balance=balance+$2-$3,frozen_balance=frozen_balance-$2,updated_at=now() WHERE id=$1 AND deleted_at IS NULL AND frozen_balance >= $2 RETURNING balance`, command.UserID, reserved.Float64, command.BalanceCost).Scan(&balance)
				if err != nil {
					return nil, nil, err
				}
				result.NewBalance, result.BalanceOverdrafted = &balance, balance < 0
				command.BalanceCost = 0
			}
		}
		if err = r.applyUsageBillingEffects(ctx, tx, &command, result); err != nil {
			return nil, nil, err
		}
		// Transactional outbox survives process loss before cache invalidation.
		if _, err = tx.ExecContext(ctx, `SELECT enqueue_auth_cache_invalidation(key) FROM api_keys WHERE id=$1`, snapshot.Command.APIKeyID); err != nil {
			return nil, nil, err
		}
	}
	// No $0 placeholder: the charge, actual-cost log and state commit together.
	log := snapshot.Usage
	// Enrichment is outside the critical financial transaction.
	log.ImageGeneration = nil
	if err = execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(&log)); err != nil {
		return nil, nil, usageBillingStageError("settlement_usage_log", started, err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE usage_settlements SET state='charged',charged_at=now(),updated_at=now(),last_error_code=NULL WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID); err != nil {
		return nil, nil, err
	}
	commitStarted := time.Now()
	err = tx.Commit()
	recordBillingStage("commit", commitStarted, err)
	if err != nil {
		return nil, nil, usageBillingStageError("settlement_commit", commitStarted, err)
	}
	return &snapshot, result, nil
}

// Apply only to the window containing the original event. Late recovery never
// rewinds a newer daily/weekly/monthly window or charges it for old usage.
func applySettlementPlatformQuota(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) error {
	s := cmd.PlatformQuota
	if s == nil || s.Platform == "" || s.Cost <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE user_platform_quotas SET
 daily_usage_usd=CASE WHEN daily_window_start>$4 THEN daily_usage_usd WHEN daily_window_start=$4 THEN daily_usage_usd+$3 ELSE $3 END,
 daily_window_start=CASE WHEN daily_window_start>$4 THEN daily_window_start ELSE $4 END,
 weekly_usage_usd=CASE WHEN weekly_window_start>$5 THEN weekly_usage_usd WHEN weekly_window_start=$5 THEN weekly_usage_usd+$3 ELSE $3 END,
 weekly_window_start=CASE WHEN weekly_window_start>$5 THEN weekly_window_start ELSE $5 END,
 monthly_usage_usd=CASE WHEN monthly_window_start>$6 THEN monthly_usage_usd WHEN monthly_window_start IS NULL OR monthly_window_start+interval '30 days'<=$6 THEN $3 ELSE monthly_usage_usd+$3 END,
 monthly_window_start=CASE WHEN monthly_window_start IS NULL OR monthly_window_start+interval '30 days'<=$6 THEN $6 ELSE monthly_window_start END,
 updated_at=now() WHERE user_id=$1 AND platform=$2 AND deleted_at IS NULL`, cmd.UserID, s.Platform, s.Cost, s.DayStart, s.WeekStart, s.OccurredAt)
	return err
}
func (r *usageBillingRepository) CompleteSettlement(ctx context.Context, ref service.UsageSettlementRef) error {
	_, err := r.db.ExecContext(ctx, `UPDATE usage_settlements SET state='complete',updated_at=now(),last_error_code=NULL WHERE request_id=$1 AND api_key_id=$2 AND state='charged'`, ref.RequestID, ref.APIKeyID)
	return err
}

func (r *usageBillingRepository) UpdateSettlementImageDetail(ctx context.Context, ref service.UsageSettlementRef, detail *service.ImageGenerationDetail) error {
	if detail == nil {
		return nil
	}
	data, ok := service.ImageGenerationDetailJSON(detail).(string)
	if !ok || data == "" {
		return errors.New("serialize settlement image detail failed")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE usage_logs SET image_generation=$3::jsonb WHERE request_id=$1 AND api_key_id=$2`, ref.RequestID, ref.APIKeyID, data)
	return err
}
func (r *usageBillingRepository) ClaimSettlement(ctx context.Context) (*service.UsageSettlementRef, error) {
	var ref service.UsageSettlementRef
	err := r.db.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM usage_settlements WHERE state IN ('ready','charged') AND next_attempt_at<=now() ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE usage_settlements s SET attempts=attempts+1,next_attempt_at=now()+interval '30 seconds',updated_at=now()
 FROM candidate c WHERE s.id=c.id RETURNING request_id,api_key_id`).Scan(&ref.RequestID, &ref.APIKeyID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &ref, err
}
func (r *usageBillingRepository) FailSettlement(ctx context.Context, ref service.UsageSettlementRef, permanent bool) error {
	state, code := "ready", "retryable"
	if permanent {
		state, code = "needs_review", "facts_conflict"
	}
	_, err := r.db.ExecContext(ctx, `UPDATE usage_settlements SET state=CASE WHEN $3='needs_review' THEN $3 ELSE state END,last_error_code=$4,
 next_attempt_at=now()+interval '30 seconds',updated_at=now() WHERE request_id=$1 AND api_key_id=$2 AND state IN ('ready','charged')`, ref.RequestID, ref.APIKeyID, state, code)
	return err
}
func (r *usageBillingRepository) ReviewExpiredSettlements(ctx context.Context) (int64, error) {
	_, err := r.db.ExecContext(ctx, `WITH stale AS (SELECT id FROM usage_settlements WHERE state='created' AND created_at<now()-interval '2 hours' ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED)
 UPDATE usage_settlements s SET state='needs_review',last_error_code='final_facts_missing',updated_at=now() FROM stale WHERE s.id=stale.id`)
	if err != nil {
		return 0, err
	}
	var n int64
	err = r.db.QueryRowContext(ctx, `SELECT count(*) FROM usage_settlements WHERE state='needs_review'`).Scan(&n)
	return n, err
}
