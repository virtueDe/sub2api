package repository

import (
	"context"
	"database/sql"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Serialize hold/capture/release even when a legacy caller has no job-row lock.
// Separate dedup keys alone do not make capture and refund mutually exclusive.
func lockBatchImageBalance(ctx context.Context, tx *sql.Tx, batchID string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "image_batch:"+batchID)
	return err
}

func (r *usageBillingRepository) SettleBatchImage(ctx context.Context, expected *service.BatchImageJob, cmd *service.BatchImageBalanceHoldCommand, usage *service.UsageLog, params service.MarkBatchImageJobSettledParams) error {
	if expected == nil || cmd == nil || usage == nil || params.Now == nil || expected.BatchID != cmd.BatchID || params.BatchID != cmd.BatchID {
		return service.ErrUsageSettlementConflict
	}
	tx, release, err := beginUsageBillingTransaction(ctx, r.db)
	if err != nil {
		return err
	}
	defer release()
	defer func() { _ = tx.Rollback() }()
	if err := lockBatchImageBalance(ctx, tx, cmd.BatchID); err != nil {
		return err
	}
	job, err := scanBatchImageJob(tx.QueryRowContext(ctx, batchImageJobSelectSQL+" WHERE batch_id=$1 FOR UPDATE", cmd.BatchID))
	if err != nil {
		return err
	}
	if job.Status == service.BatchImageJobStatusCompleted {
		if job.ActualCost != nil && *job.ActualCost == cmd.ActualAmount && job.ManifestHash != nil && *job.ManifestHash == params.ManifestHash {
			return nil
		}
		return service.ErrUsageSettlementConflict
	}
	if job.Status != service.BatchImageJobStatusSettling || job.UserID != cmd.UserID || job.APIKeyID == nil || *job.APIKeyID != cmd.APIKeyID || service.BuildBatchImageSettlementManifestHash(job) != params.ManifestHash {
		return service.ErrUsageSettlementConflict
	}
	held := job.EstimatedCost
	if job.HoldAmount != nil {
		held = *job.HoldAmount
	}
	if service.QuantizeUsageBillingAmount(held) != cmd.HoldAmount || usage.ActualCost != cmd.ActualAmount || usage.RequestID != cmd.RequestID || usage.APIKeyID != cmd.APIKeyID || usage.UserID != cmd.UserID || job.AccountID == nil || usage.AccountID != *job.AccountID {
		return service.ErrUsageSettlementConflict
	}
	if job.PricingSnapshotVersion >= 1 && service.QuantizeUsageBillingAmount(float64(job.SuccessCount)*job.BillableUnitPrice) != cmd.ActualAmount {
		return service.ErrUsageSettlementConflict
	}
	cmd.Normalize()
	applied, err := r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
	if err != nil {
		return err
	}
	if applied {
		if cmd.HoldAmount > 0 {
			held, err := batchImageHoldClaimExists(ctx, tx, service.BatchImageHoldRequestID(cmd.BatchID), cmd.APIKeyID)
			if err != nil {
				return err
			}
			refunded, err := batchImageHoldClaimExists(ctx, tx, service.BatchImageReleaseRequestID(cmd.BatchID), cmd.APIKeyID)
			if err != nil {
				return err
			}
			if !held || refunded {
				return service.ErrUsageSettlementConflict
			}
		}
		if _, err := captureUsageBillingBatchImageBalance(ctx, tx, cmd); err != nil {
			return err
		}
	}
	// Repairs the legacy capture-before-log crash window without double charging.
	minimal := *usage
	minimal.ImageGeneration = nil
	if err := execUsageLogInsertNoResult(ctx, tx, prepareUsageLogInsert(&minimal)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_image_jobs SET status='completed',actual_cost=$2,manifest_hash=$3,
 settled_at=COALESCE(settled_at,$4),finished_at=COALESCE(finished_at,$4),output_expires_at=COALESCE(output_expires_at,$5),updated_at=$4,version=version+1
 WHERE batch_id=$1 AND status='settling'`, cmd.BatchID, cmd.ActualAmount, params.ManifestHash, params.Now, params.OutputExpiresAt)
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
	if _, err := tx.ExecContext(ctx, `SELECT enqueue_auth_cache_invalidation(key) FROM api_keys WHERE user_id=$1`, cmd.UserID); err != nil {
		return err
	}
	return tx.Commit()
}
