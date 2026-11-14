package dbstats

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudgetRejectsBeforeLoaderAndReleasesAfterCleanup(t *testing.T) {
	b := NewBudget(1)
	entered, cleanup, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := Run(context.Background(), b, time.Second, func(context.Context) (int, error) {
			close(entered)
			<-cleanup
			return 0, errors.New("query failed")
		})
		finished <- err
	}()
	<-entered
	var calls atomic.Int32
	_, err := Run(context.Background(), b, time.Second, func(context.Context) (int, error) { calls.Add(1); return 1, nil })
	require.ErrorIs(t, err, ErrBusy)
	require.Zero(t, calls.Load())
	close(cleanup)
	require.Error(t, <-finished)
	v, err := Run(context.Background(), b, time.Second, func(context.Context) (int, error) { return 7, nil })
	require.NoError(t, err)
	require.Equal(t, 7, v)
	require.Equal(t, uint64(1), b.rejected.Load())
	require.Equal(t, uint64(1), b.failed.Load())
}

func TestBudgetHonorsCancellationAndQueryDeadline(t *testing.T) {
	b := NewBudget(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, b, time.Second, func(context.Context) (int, error) { t.Fatal("cancelled caller acquired admission"); return 0, nil })
	require.ErrorIs(t, err, context.Canceled)
	_, err = Run(context.Background(), b, time.Millisecond, func(ctx context.Context) (int, error) { <-ctx.Done(); return 0, ctx.Err() })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, b.slots)
}

func TestBatchReadLeavesMaintenanceAnExecutionOpportunity(t *testing.T) {
	b := NewBudget(1)
	_, err := RunBatchRead(context.Background(), b, func(ctx context.Context) (int, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 8*time.Second)
		return 1, nil
	})
	require.NoError(t, err)
	_, err = RunBatchRead(context.Background(), b, func(context.Context) (int, error) { t.Fatal("batch cooldown bypassed"); return 0, nil })
	require.ErrorIs(t, err, ErrBusy)
	require.NoError(t, RunMaintenance(context.Background(), b, func(context.Context) error { return nil }))
	_, err = Run(context.Background(), b, ReadTimeout, func(context.Context) (int, error) { return 1, nil })
	require.NoError(t, err, "short reads remain usable between batch queries")
}
