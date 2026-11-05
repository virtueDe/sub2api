//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type invalidationRateRepo struct {
	UserGroupRateRepository
	rate    atomic.Int64
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (r *invalidationRateRepo) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	rate := float64(r.rate.Load()) / 10000
	if r.calls.Add(1) == 1 && r.started != nil {
		close(r.started)
		<-r.release
	}
	return &rate, nil
}

func TestUserGroupRateInvalidationDuringLoad(t *testing.T) {
	repo := &invalidationRateRepo{started: make(chan struct{}), release: make(chan struct{})}
	repo.rate.Store(10000)
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "test")
	result := make(chan float64, 1)
	go func() { result <- resolver.Resolve(context.Background(), 1, 21, 1) }()
	<-repo.started
	repo.rate.Store(100)
	resolver.Invalidate()
	// A post-commit caller must not join the in-flight load from the old generation.
	require.Equal(t, 0.01, resolver.Resolve(context.Background(), 1, 21, 1))
	close(repo.release)
	select {
	case rate := <-result:
		require.Equal(t, 0.01, rate)
	case <-time.After(5 * time.Second):
		t.Fatal("old load did not finish")
	}
	require.Equal(t, 0.01, resolver.Resolve(context.Background(), 1, 21, 1))
	require.Equal(t, int64(2), repo.calls.Load())
}

func TestUserGroupRateInvalidationAcrossInstancesWithoutAuthL1(t *testing.T) {
	repo := &invalidationRateRepo{}
	repo.rate.Store(10000)
	callbacks := make(chan func(string), 2)
	cache := &authInvalidationCacheStub{subscribeFn: func(ctx context.Context, callback func(string)) error {
		callbacks <- callback
		<-ctx.Done()
		return ctx.Err()
	}}
	first := &APIKeyService{cache: cache}
	second := &APIKeyService{cache: cache}
	t.Cleanup(first.StopAuthCacheInvalidationSubscriber)
	t.Cleanup(second.StopAuthCacheInvalidationSubscriber)
	resolvers := []*userGroupRateResolver{
		newUserGroupRateResolver(repo, nil, time.Minute, nil, "gateway"),
		newUserGroupRateResolver(repo, nil, time.Minute, nil, "openai"),
		newUserGroupRateResolver(repo, nil, time.Minute, nil, "second-instance"),
	}
	first.AddAuthCacheInvalidationListener(resolvers[0].Invalidate)
	first.AddAuthCacheInvalidationListener(resolvers[1].Invalidate)
	second.AddAuthCacheInvalidationListener(resolvers[2].Invalidate)
	listeners := []func(string){}
	for range 2 {
		select {
		case fn := <-callbacks:
			listeners = append(listeners, fn)
		case <-time.After(5 * time.Second):
			t.Fatal("rate invalidation subscriber did not start with auth L1 disabled")
		}
	}
	cache.publishFn = func(_ context.Context, key string) error {
		for _, listener := range listeners {
			listener(key)
		}
		return nil
	}
	for _, resolver := range resolvers {
		require.Equal(t, 1.0, resolver.Resolve(context.Background(), 1, 21, 1))
	}
	repo.rate.Store(100)
	first.deleteAuthCache(context.Background(), "test-hash")
	for _, resolver := range resolvers {
		require.Equal(t, 0.01, resolver.Resolve(context.Background(), 1, 21, 1))
	}
}
