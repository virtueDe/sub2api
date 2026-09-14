package service

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisImageURLProxyCache Redis 实现的图片 URL 代理缓存
type RedisImageURLProxyCache struct {
	client *redis.Client
}

// NewRedisImageURLProxyCache 创建 Redis 缓存适配器
func NewRedisImageURLProxyCache(client *redis.Client) ImageURLProxyCache {
	if client == nil {
		return nil
	}
	return &RedisImageURLProxyCache{client: client}
}

// Get 获取缓存值
func (c *RedisImageURLProxyCache) Get(ctx context.Context, key string) (string, error) {
	if c == nil || c.client == nil {
		return "", nil
	}
	return c.client.Get(ctx, key).Result()
}

// Set 设置缓存值
func (c *RedisImageURLProxyCache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Set(ctx, key, value, ttl).Err()
}
