package httputil

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitImageQueue(t *testing.T, budget *BodyMemoryBudget, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.images != nil && budget.images.waiting == n
	}, time.Second, time.Millisecond)
}

func TestImageMemoryFairQueueAndCancel(t *testing.T) {
	b := NewBodyMemoryBudget(1024)
	first, err := b.acquireImageMemory(context.Background(), 1, b.imageLimit())
	require.NoError(t, err)
	ready := make(chan func(), 3)
	start := func(user int64, ctx context.Context, result chan<- func()) {
		go func() {
			release, err := b.acquireImageMemory(ctx, user, b.imageLimit())
			if err == nil {
				result <- release
			}
		}()
	}
	start(1, context.Background(), ready)
	waitImageQueue(t, b, 1)
	start(1, context.Background(), ready)
	waitImageQueue(t, b, 2)
	cold := make(chan func(), 1)
	start(2, context.Background(), cold)
	waitImageQueue(t, b, 3)
	ctx, cancel := context.WithCancel(context.Background())
	start(3, ctx, ready)
	waitImageQueue(t, b, 4)
	cancel()
	waitImageQueue(t, b, 3)
	first()
	select {
	case leave := <-ready:
		leave()
	case <-time.After(time.Second):
		t.Fatal("first user stalled")
	}
	select {
	case leave := <-cold:
		leave()
	case <-time.After(time.Second):
		t.Fatal("other user starved by first user's backlog")
	}
	select {
	case leave := <-ready:
		leave()
	case <-time.After(time.Second):
		t.Fatal("remaining request stalled")
	}
	require.Zero(t, requestBodyBudgetUsed(b))
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Empty(t, b.images.users)
	require.Empty(t, b.images.order)
}

func TestImageMemoryDetachedAndNestedOwners(t *testing.T) {
	b := NewBodyMemoryBudget(32 << 20)
	base, releaseBase := WithRequestBodyMemoryBudget(context.Background(), b)
	defer releaseBase()
	ctx, release, err := AdmitImageRequestMemory(base, 1, -1, false)
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/", bytes.NewReader([]byte("short"))).WithContext(ctx)
	req.ContentLength = -1
	body, err := ReadRequestBodyWithPrealloc(req)
	require.NoError(t, err)
	require.Greater(t, cap(body), len(body))
	detached, done, err := DetachRequestBodyMemory(ctx, int64(cap(body)))
	require.NoError(t, err)
	scope, closeScope := WithRequestBodyMemoryScope(detached)
	require.NoError(t, ReserveImageWorkingMemory(scope, len(body), 8))
	release()
	releaseBase()
	require.Positive(t, requestBodyBudgetUsed(b))
	done()
	require.Positive(t, requestBodyBudgetUsed(b), "nested async work still owns the envelope")
	closeScope()
	closeScope()
	done()
	require.Zero(t, requestBodyBudgetUsed(b))
}

func TestImageMemoryGrowthAndFailedConversionNeverWait(t *testing.T) {
	b := NewBodyMemoryBudget(8 << 20)
	base, done := WithRequestBodyMemoryBudget(context.Background(), b)
	defer done()
	ctx, release, err := AdmitImageRequestMemory(base, 1, 16, false)
	require.NoError(t, err)
	require.NoError(t, ReserveRequestBodyMemory(ctx, 3<<20), "URL conversion may grow a small ingress envelope")
	require.ErrorIs(t, ReserveRequestBodyMemory(ctx, 8<<20), ErrRequestBodyMemoryBudget)
	require.LessOrEqual(t, requestBodyBudgetUsed(b), b.imageLimit())
	release()
	require.Zero(t, requestBodyBudgetUsed(b))
}

func TestImageMemoryMixedBurst88(t *testing.T) {
	b := NewBodyMemoryBudget(128 << 20)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan error, 88)
	var active, peak atomic.Int64
	var wg sync.WaitGroup
	for i := range 88 {
		wg.Go(func() {
			<-start
			user := int64(467)
			if i >= 73 {
				user = int64(1000 + i)
			}
			size := 1 << 20
			if i%3 == 0 {
				size = 5 << 20
			}
			base, closeBase := WithRequestBodyMemoryBudget(ctx, b)
			defer closeBase()
			requestCtx, release, err := AdmitImageRequestMemory(base, user, int64(size), false)
			if err != nil {
				results <- err
				return
			}
			defer release()
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			input := bytes.Repeat([]byte{'x'}, size)
			req := httptest.NewRequest("POST", "/images/edits", bytes.NewReader(input)).WithContext(requestCtx)
			body, err := ReadRequestBodyWithPrealloc(req)
			if err != nil || !bytes.Equal(body, input) {
				results <- fmt.Errorf("body changed: %w", err)
				return
			}
			for attempt := 0; attempt < 3; attempt++ {
				work, closeWork := WithRequestBodyMemoryScope(requestCtx)
				err = ReserveImageWorkingMemory(work, len(body), 8)
				closeWork()
				if err != nil {
					results <- err
					return
				}
			}
			if requestBodyBudgetUsed(b) > b.imageLimit() {
				results <- fmt.Errorf("budget exceeded")
				return
			}
			results <- nil
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Zero(t, requestBodyBudgetUsed(b))
	t.Logf("88 admitted, 73 from one user, max simultaneous=%d, final reserved bytes=0", peak.Load())
}
