package repository

import (
	"context"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func waitPayerQueue(t *testing.T, gate *billingPayerGate, n int) {
	t.Helper()
	require.Eventually(t, func() bool { gate.mu.Lock(); defer gate.mu.Unlock(); return gate.waiting == n }, time.Second, time.Millisecond)
}

func TestBillingPayerGateFIFOAndCancellation(t *testing.T) {
	g := newBillingPayerGate(2, 2, 2)
	key := billingPayerKey{userID: 1}
	first, err := g.acquire(context.Background(), key, false)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { _, err := g.acquire(ctx, key, false); cancelled <- err }()
	waitPayerQueue(t, g, 1)
	acquired := make(chan func(), 1)
	go func() {
		leave, err := g.acquire(context.Background(), key, false)
		if err == nil {
			acquired <- leave
		}
	}()
	waitPayerQueue(t, g, 2)
	_, err = g.acquire(context.Background(), key, false)
	require.ErrorIs(t, err, errBillingPayerQueueFull)
	other, err := g.acquire(context.Background(), billingPayerKey{userID: 2}, false)
	require.NoError(t, err)
	other()
	cancel()
	require.ErrorIs(t, <-cancelled, context.Canceled)
	first()
	first()
	select {
	case leave := <-acquired:
		leave()
	case <-time.After(time.Second):
		t.Fatal("payer lease lost")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Empty(t, g.entries)
	require.Zero(t, g.waiting)
}

func TestBillingPayerGateCapacityWaitKeepsLegacyApply(t *testing.T) {
	g := newBillingPayerGate(1, 1, 1)
	leave, err := g.acquire(context.Background(), billingPayerKey{userID: 1}, false)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		next, err := g.acquire(ctx, billingPayerKey{userID: 2}, true)
		if err == nil {
			next()
		}
		done <- err
	}()
	leave()
	require.NoError(t, <-done)
}

func TestBillingPayerGateCancelReleaseRace(t *testing.T) {
	g := newBillingPayerGate(2, 4, 4)
	key := billingPayerKey{userID: 1}
	for range 100 {
		leave, err := g.acquire(context.Background(), key, false)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Go(func() {
			next, err := g.acquire(ctx, key, false)
			if err == nil {
				next()
			}
		})
		waitPayerQueue(t, g, 1)
		wg.Go(cancel)
		wg.Go(leave)
		wg.Wait()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Empty(t, g.entries)
	require.Zero(t, g.waiting)
}
