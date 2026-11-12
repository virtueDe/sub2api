//go:build integration

package repository

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func batchAtomicFixture(t *testing.T) (*usageBillingRepository, *service.BatchImageJob, *service.BatchImageBalanceHoldCommand, *service.UsageLog, service.MarkBatchImageJobSettledParams) {
	ctx := context.Background()
	r, s, _ := imageReservationFixture(t)
	repo := NewBatchImageRepository(integrationDB)
	hold := 2.0
	id, err := service.NewBatchImageID()
	require.NoError(t, err)
	job, err := repo.CreateBatchImageJob(ctx, service.CreateBatchImageJobParams{BatchID: id, UserID: s.Command.UserID, APIKeyID: &s.Command.APIKeyID, AccountID: &s.Command.AccountID, Provider: service.BatchImageProviderVertex, Model: "gemini-2.5-flash-image", Status: service.BatchImageJobStatusSettling, ItemCount: 2, SuccessCount: 1, FailCount: 1, EstimatedCost: hold, HoldAmount: &hold, BillableUnitPrice: 1.25, PricingSnapshotVersion: 1})
	require.NoError(t, err)
	_, err = r.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageHoldRequestID(id), BatchID: id, UserID: job.UserID, APIKeyID: *job.APIKeyID, HoldAmount: 2})
	require.NoError(t, err)
	manifest := service.BuildBatchImageSettlementManifestHash(job)
	cmd := &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageCaptureRequestID(id), BatchID: id, UserID: job.UserID, APIKeyID: *job.APIKeyID, HoldAmount: 2, ActualAmount: 1.25, RequestPayloadHash: manifest}
	cmd.Normalize()
	usage := s.Usage
	usage.RequestID = cmd.RequestID
	usage.RequestType = service.RequestTypeImageAsync
	now := time.Now()
	expires := now.Add(time.Hour)
	params := service.MarkBatchImageJobSettledParams{BatchID: id, ActualCost: 1.25, ManifestHash: manifest, Now: &now, OutputExpiresAt: &expires}
	return r, job, cmd, &usage, params
}
func TestBatchImageAtomicCapture(t *testing.T) {
	ctx := context.Background()
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "atomic", true: "legacy_capture_recovery"}[legacy], func(t *testing.T) {
			r, job, cmd, usage, params := batchAtomicFixture(t)
			checkImageWallet(t, job.UserID, 98, 2)
			if legacy {
				_, err := r.CaptureBatchImageBalance(ctx, cmd)
				require.NoError(t, err)
			}
			require.NoError(t, r.SettleBatchImage(ctx, job, cmd, usage, params))
			require.NoError(t, r.SettleBatchImage(ctx, job, cmd, usage, params))
			wrongPayer := *cmd
			wrongPayer.UserID++
			require.ErrorIs(t, r.SettleBatchImage(ctx, job, &wrongPayer, usage, params), service.ErrUsageSettlementConflict)
			wrongUsage := *usage
			wrongUsage.RequestID += ":wrong"
			require.ErrorIs(t, r.SettleBatchImage(ctx, job, cmd, &wrongUsage, params), service.ErrUsageSettlementConflict)
			checkImageWallet(t, job.UserID, 98.75, 0)
			var state string
			var cost float64
			var n int
			require.NoError(t, integrationDB.QueryRow(`SELECT status,actual_cost FROM batch_image_jobs WHERE batch_id=$1`, job.BatchID).Scan(&state, &cost))
			require.Equal(t, "completed", state)
			require.Equal(t, 1.25, cost)
			require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM usage_logs WHERE request_id=$1`, cmd.RequestID).Scan(&n))
			require.Equal(t, 1, n)
			// Another request's frozen funds must never fund a second refund.
			ref := service.UsageSettlementRef{RequestID: "settlement:" + uuid.NewString(), APIKeyID: *job.APIKeyID}
			require.NoError(t, r.ReserveImageSettlement(ctx, ref, job.UserID, nil, 4))
			_, err := r.ReleaseBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageReleaseRequestID(job.BatchID), BatchID: job.BatchID, UserID: job.UserID, APIKeyID: *job.APIKeyID, HoldAmount: 2})
			require.ErrorIs(t, err, service.ErrUsageSettlementConflict)
			checkImageWallet(t, job.UserID, 94.75, 4)
		})
	}
}
func TestBatchImageAtomicRollback(t *testing.T) {
	ctx := context.Background()
	r, job, cmd, usage, params := batchAtomicFixture(t)
	// A missing account fails the minimal bill after capture; everything rolls back.
	_, err := integrationDB.Exec(`UPDATE batch_image_jobs SET account_id=-123 WHERE batch_id=$1`, job.BatchID)
	require.NoError(t, err)
	bad := int64(-123)
	job.AccountID = &bad
	usage.AccountID = bad
	require.Error(t, r.SettleBatchImage(ctx, job, cmd, usage, params))
	checkImageWallet(t, job.UserID, 98, 2)
	var n int
	var state string
	require.NoError(t, integrationDB.QueryRow(`SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, cmd.RequestID).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, integrationDB.QueryRow(`SELECT status FROM batch_image_jobs WHERE batch_id=$1`, job.BatchID).Scan(&state))
	require.Equal(t, "settling", state)
}
func TestBatchImageUnknownSubmissionCannotBeRefundedByAge(t *testing.T) {
	ctx := context.Background()
	r, job, _, _, _ := batchAtomicFixture(t)
	_, err := integrationDB.Exec(`UPDATE batch_image_jobs SET status='uploading',updated_at=now()-interval '3 hours' WHERE batch_id=$1`, job.BatchID)
	require.NoError(t, err)
	repo := NewBatchImageRepository(integrationDB)
	changed, err := repo.FailStaleUnsubmittedBatchImageJob(ctx, job.BatchID, time.Now().Add(-time.Hour), "stale", "stale")
	require.NoError(t, err)
	require.False(t, changed)
	_, err = r.ReleaseBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{RequestID: service.BatchImageReleaseRequestID(job.BatchID), BatchID: job.BatchID, UserID: job.UserID, APIKeyID: *job.APIKeyID, HoldAmount: 2})
	require.ErrorIs(t, err, service.ErrUsageSettlementConflict)
	checkImageWallet(t, job.UserID, 98, 2)
}
