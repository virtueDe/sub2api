package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

type userGroupRateResolver struct {
	mu           sync.RWMutex
	generation   uint64
	repo         UserGroupRateRepository
	cache        *gocache.Cache
	cacheTTL     time.Duration
	sf           *singleflight.Group
	logComponent string
}

var errUserGroupRateInvalidated = errors.New("user group rate cache invalidated during load")

func (r *userGroupRateResolver) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generation++
	if r.cache != nil {
		r.cache.Flush()
	}
}

func newUserGroupRateResolver(repo UserGroupRateRepository, cache *gocache.Cache, cacheTTL time.Duration, sf *singleflight.Group, logComponent string) *userGroupRateResolver {
	if cacheTTL <= 0 {
		cacheTTL = defaultUserGroupRateCacheTTL
	}
	if cache == nil {
		cache = gocache.New(cacheTTL, time.Minute)
	}
	if logComponent == "" {
		logComponent = "service.gateway"
	}
	if sf == nil {
		sf = &singleflight.Group{}
	}

	return &userGroupRateResolver{
		repo:         repo,
		cache:        cache,
		cacheTTL:     cacheTTL,
		sf:           sf,
		logComponent: logComponent,
	}
}

func (r *userGroupRateResolver) Resolve(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	if r == nil || userID <= 0 || groupID <= 0 {
		return groupDefaultMultiplier
	}

	key := fmt.Sprintf("%d:%d", userID, groupID)
	for {
		multiplier, err := r.resolveGeneration(ctx, key, userID, groupID, groupDefaultMultiplier)
		if errors.Is(err, errUserGroupRateInvalidated) && ctx.Err() == nil {
			continue
		}
		if err != nil {
			userGroupRateCacheFallbackTotal.Add(1)
			logger.LegacyPrintf(r.logComponent, "get user group rate failed, fallback to group default: user=%d group=%d err=%v", userID, groupID, err)
			return groupDefaultMultiplier
		}
		return multiplier
	}
}

func (r *userGroupRateResolver) resolveGeneration(ctx context.Context, key string, userID, groupID int64, groupDefaultMultiplier float64) (float64, error) {
	r.mu.RLock()
	generation := r.generation
	if r.cache != nil {
		if cached, ok := r.cache.Get(key); ok {
			if multiplier, castOK := cached.(float64); castOK {
				r.mu.RUnlock()
				userGroupRateCacheHitTotal.Add(1)
				return multiplier, nil
			}
		}
	}
	r.mu.RUnlock()
	if r.repo == nil {
		return groupDefaultMultiplier, nil
	}
	userGroupRateCacheMissTotal.Add(1)

	flightKey := fmt.Sprintf("%s:%d", key, generation)
	value, err, shared := r.sf.Do(flightKey, func() (any, error) {
		r.mu.RLock()
		if r.generation != generation {
			r.mu.RUnlock()
			return nil, errUserGroupRateInvalidated
		}
		if r.cache != nil {
			if cached, ok := r.cache.Get(key); ok {
				if multiplier, castOK := cached.(float64); castOK {
					r.mu.RUnlock()
					userGroupRateCacheHitTotal.Add(1)
					return multiplier, nil
				}
			}
		}
		r.mu.RUnlock()

		userGroupRateCacheLoadTotal.Add(1)
		userRate, repoErr := r.repo.GetByUserAndGroup(ctx, userID, groupID)
		if repoErr != nil {
			return nil, repoErr
		}

		multiplier := groupDefaultMultiplier
		if userRate != nil {
			multiplier = *userRate
		}
		r.mu.RLock()
		defer r.mu.RUnlock()
		if r.generation != generation {
			return nil, errUserGroupRateInvalidated
		}
		if r.cache != nil {
			r.cache.Set(key, multiplier, r.cacheTTL)
		}
		return multiplier, nil
	})
	if shared {
		userGroupRateCacheSFSharedTotal.Add(1)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.generation != generation {
		return 0, errUserGroupRateInvalidated
	}
	if err != nil {
		return 0, err
	}

	multiplier, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("unexpected user group rate result %T", value)
	}
	return multiplier, nil
}
