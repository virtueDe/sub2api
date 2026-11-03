package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type guardedBalanceRepo struct {
	UserRepository
	calls atomic.Int64
	delay time.Duration
	err   error
}

func (r *guardedBalanceRepo) GetBalance(ctx context.Context, _ int64) (float64, error) {
	r.calls.Add(1)
	select {
	case <-time.After(r.delay):
		return 12, r.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

type guardedBillingCache struct {
	billingCacheWorkerStub
	balance float64
	sub     *SubscriptionCacheData
}

func (c *guardedBillingCache) GetUserBalance(context.Context, int64) (float64, error) {
	return c.balance, nil
}
func (c *guardedBillingCache) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return c.sub, nil
}

func billingBreakerConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Billing.CircuitBreaker = config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 5, ResetTimeoutSeconds: 30, HalfOpenRequests: 1}
	return cfg
}

func TestBillingLoadSharedFailureCountsOnce(t *testing.T) {
	for _, cache := range []BillingCache{nil, &billingCacheWorkerStub{}} {
		repo := &guardedBalanceRepo{delay: 150 * time.Millisecond, err: errors.New("database unavailable")}
		svc := NewBillingCacheService(cache, repo, nil, nil, nil, nil, billingBreakerConfig(), nil)
		t.Cleanup(svc.Stop)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := svc.GetUserBalance(context.Background(), 1)
				require.Error(t, err)
			}()
		}
		close(start)
		wg.Wait()
		require.Equal(t, int64(1), repo.calls.Load())
		require.Equal(t, 1, svc.circuitBreaker.failures)
		require.Equal(t, billingCircuitClosed, svc.circuitBreaker.state)
	}
}

func TestBillingLoadOpenCircuitAllowsCachedChecks(t *testing.T) {
	cache := &guardedBillingCache{balance: 12, sub: &SubscriptionCacheData{Status: SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour)}}
	repo := &guardedBalanceRepo{}
	svc := NewBillingCacheService(cache, repo, nil, nil, nil, nil, billingBreakerConfig(), nil)
	t.Cleanup(svc.Stop)
	svc.circuitBreaker.state = billingCircuitOpen
	svc.circuitBreaker.openedAt = time.Now()
	require.NoError(t, svc.CheckBillingEligibility(context.Background(), &User{ID: 1}, nil, nil, nil, ""))
	cache.balance = 0
	require.ErrorIs(t, svc.CheckBillingEligibility(context.Background(), &User{ID: 1}, nil, nil, nil, ""), ErrInsufficientBalance)
	require.NoError(t, svc.checkSubscriptionEligibility(context.Background(), 1, &Group{ID: 2}, &UserSubscription{}))
	cache.sub.ExpiresAt = time.Now().Add(-time.Hour)
	require.ErrorIs(t, svc.checkSubscriptionEligibility(context.Background(), 1, &Group{ID: 2}, &UserSubscription{}), ErrSubscriptionInvalid)
	require.Equal(t, billingCircuitOpen, svc.circuitBreaker.state, "cache hits must not claim DB recovery")
	svc.cache = nil
	_, err := svc.GetUserBalance(context.Background(), 1)
	require.ErrorIs(t, err, ErrBillingServiceUnavailable)
	require.Zero(t, repo.calls.Load())
}

func TestBillingLoadCallerCancellationDoesNotCancelSharedQuery(t *testing.T) {
	repo := &guardedBalanceRepo{delay: 150 * time.Millisecond}
	svc := NewBillingCacheService(nil, repo, nil, nil, nil, nil, billingBreakerConfig(), nil)
	t.Cleanup(svc.Stop)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := svc.GetUserBalance(ctx, 1); done <- err }()
	require.Eventually(t, func() bool { return repo.calls.Load() == 1 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	balance, err := svc.GetUserBalance(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, float64(12), balance)
	require.Equal(t, int64(1), repo.calls.Load())
	require.Zero(t, svc.circuitBreaker.failures)
}

func TestBillingLoadHalfOpenUsesRealDatabaseRecovery(t *testing.T) {
	repo := &guardedBalanceRepo{}
	svc := NewBillingCacheService(nil, repo, nil, nil, nil, nil, billingBreakerConfig(), nil)
	t.Cleanup(svc.Stop)
	svc.circuitBreaker.state = billingCircuitOpen
	svc.circuitBreaker.openedAt = time.Now().Add(-time.Minute)
	_, err := svc.GetUserBalance(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, billingCircuitClosed, svc.circuitBreaker.state)
	svc.circuitBreaker.state = billingCircuitOpen
	svc.circuitBreaker.openedAt = time.Now().Add(-time.Minute)
	repo.err = errors.New("still unavailable")
	_, err = svc.GetUserBalance(context.Background(), 2)
	require.Error(t, err)
	require.Equal(t, billingCircuitOpen, svc.circuitBreaker.state)
}
