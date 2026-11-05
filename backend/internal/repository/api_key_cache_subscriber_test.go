package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyCacheSubscriber_ReconnectInvalidatesRelatedCaches(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	svc := service.NewAPIKeyService(nil, nil, nil, nil, nil, NewAPIKeyCache(client), nil)
	t.Cleanup(svc.StopAuthCacheInvalidationSubscriber)
	invalidated := make(chan struct{}, 8)
	svc.AddAuthCacheInvalidationListener(func() { invalidated <- struct{}{} })
	awaitInvalidation := func() {
		t.Helper()
		select {
		case <-invalidated:
		case <-time.After(5 * time.Second):
			t.Fatal("cache was not invalidated on subscription readiness")
		}
	}
	awaitInvalidation()
	server.Close()
	require.NoError(t, server.Restart())
	// No message is published after restart: the subscription event itself must
	// flush stale values from the time the connection was down.
	awaitInvalidation()
	require.NoError(t, client.Publish(context.Background(), authCacheInvalidateChannel, "hash").Err())
	awaitInvalidation()
}

func TestAPIKeyCacheSubscriber_BlocksUntilContextCancellation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer func() { _ = client.Close() }()
	cache := NewAPIKeyCache(client)
	ctx, cancel := context.WithCancel(context.Background())
	received := make(chan string, 1)
	returned := make(chan error, 1)
	go func() {
		returned <- cache.SubscribeAuthCacheInvalidation(ctx, func(value string) { received <- value })
	}()

	var value string
	require.Eventually(t, func() bool {
		require.NoError(t, client.Publish(context.Background(), authCacheInvalidateChannel, "hash").Err())
		select {
		case value = <-received:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, "hash", value)
	select {
	case err := <-returned:
		t.Fatalf("subscriber returned while connection was active: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-returned:
		require.True(t, errors.Is(err, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("subscriber did not stop after context cancellation")
	}
}
