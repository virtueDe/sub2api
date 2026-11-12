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

func TestBillingPayerWaitDoesNotBorrowConnections(t *testing.T) {
	r, s, _ := imageReservationFixture(t)
	r2, s2, ref2 := imageReservationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, release, err := beginPayerBillingTransaction(ctx, r.db, s.Command.UserID, false)
	require.NoError(t, err)
	defer release()
	_, err = tx.ExecContext(ctx, `UPDATE users SET updated_at=now() WHERE id=$1`, s.Command.UserID)
	require.NoError(t, err)
	base := r.db.Stats()
	results := make(chan error, 20)
	for range 20 {
		go func() {
			waitCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
			defer stop()
			results <- r.ReserveImageSettlement(waitCtx, service.UsageSettlementRef{RequestID: "settlement:" + uuid.NewString(), APIKeyID: s.Command.APIKeyID}, s.Command.UserID, nil, 1)
		}()
	}
	require.Eventually(t, func() bool {
		sharedBillingPayerGate.mu.Lock()
		defer sharedBillingPayerGate.mu.Unlock()
		entry := sharedBillingPayerGate.entries[billingPayerKey{db: r.db, userID: s.Command.UserID}]
		return entry != nil && entry.waiting.Len() == 20
	}, time.Second, time.Millisecond)
	require.Equal(t, base.InUse, r.db.Stats().InUse)
	require.Equal(t, base.WaitCount, r.db.Stats().WaitCount)
	require.NoError(t, r2.ReserveImageSettlement(ctx, ref2, s2.Command.UserID, nil, 1))
	for range 20 {
		require.ErrorIs(t, <-results, context.DeadlineExceeded)
	}
	release()
	ref := service.UsageSettlementRef{RequestID: "settlement:" + uuid.NewString(), APIKeyID: s.Command.APIKeyID}
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 1))
	require.NoError(t, r.ReleaseImageSettlement(ctx, ref))
	checkImageWallet(t, s.Command.UserID, 100, 0)
}

func TestBillingPayerCancelledSettlementRetainsReadyFacts(t *testing.T) {
	r, s, ref := imageReservationFixture(t)
	ctx := context.Background()
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 2))
	require.NoError(t, r.PrepareSettlement(ctx, s))
	leave, err := sharedBillingPayerGate.acquire(ctx, billingPayerKey{db: r.db, userID: s.Command.UserID}, false)
	require.NoError(t, err)
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, _, err = r.ApplySettlement(waitCtx, ref, nil)
	cancel()
	leave()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	checkImageWallet(t, s.Command.UserID, 98, 2)
	var state string
	require.NoError(t, r.db.QueryRow(`SELECT state FROM usage_settlements WHERE request_id=$1`, ref.RequestID).Scan(&state))
	require.Equal(t, "ready", state)
	_, result, err := r.ApplySettlement(ctx, ref, nil)
	require.NoError(t, err)
	require.True(t, result.Applied)
	_, result, err = r.ApplySettlement(ctx, ref, nil)
	require.NoError(t, err)
	require.False(t, result.Applied)
	checkImageWallet(t, s.Command.UserID, 100-s.Command.BalanceCost, 0)
}

func TestBillingPayerRefundDecisionWaitDoesNotFillPool(t *testing.T) {
	r, s, ref := imageReservationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, s.Command.UserID, nil, 2))
	tx, err := r.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SELECT id FROM usage_settlements WHERE request_id=$1 FOR UPDATE`, ref.RequestID)
	require.NoError(t, err)
	baseline := r.db.Stats().InUse
	waitCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 20)
	for range 20 {
		go func() { done <- r.ReleaseImageSettlement(waitCtx, ref) }()
	}
	require.Eventually(t, func() bool {
		sharedBillingPayerGate.mu.Lock()
		defer sharedBillingPayerGate.mu.Unlock()
		entry := sharedBillingPayerGate.entries[billingPayerKey{db: r.db, userID: s.Command.UserID}]
		return entry != nil && entry.waiting.Len() == 19
	}, time.Second, time.Millisecond)
	require.LessOrEqual(t, r.db.Stats().InUse, baseline+1, "only the admitted refund may wait on the external row lock")
	stop()
	for range 20 {
		require.Error(t, <-done)
	}
	require.NoError(t, tx.Rollback())
	require.NoError(t, r.ReleaseImageSettlement(ctx, ref))
	checkImageWallet(t, s.Command.UserID, 100, 0)
}

func TestBillingPayerRefundFailureRetainsReleaseDecision(t *testing.T) {
	r, snapshot, ref := imageReservationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, r.ReserveImageSettlement(ctx, ref, snapshot.Command.UserID, nil, 2))
	blocker, err := r.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback() }()
	_, err = blocker.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, snapshot.Command.UserID)
	require.NoError(t, err)
	waitCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- r.ReleaseImageSettlement(waitCtx, ref) }()
	require.Eventually(t, func() bool {
		var state string
		return r.db.QueryRowContext(ctx, `SELECT state FROM usage_settlements WHERE request_id=$1`, ref.RequestID).Scan(&state) == nil && state == "release_pending"
	}, time.Second, time.Millisecond)
	stop()
	require.Error(t, <-done)
	checkImageWallet(t, snapshot.Command.UserID, 98, 2)
	var state string
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT state FROM usage_settlements WHERE request_id=$1`, ref.RequestID).Scan(&state))
	require.Equal(t, "release_pending", state, "refund rollback must not erase the committed decision")
	require.NoError(t, blocker.Rollback())
	require.NoError(t, r.ReleaseImageSettlement(ctx, ref))
	require.NoError(t, r.ReleaseImageSettlement(ctx, ref))
	checkImageWallet(t, snapshot.Command.UserID, 100, 0)
}
