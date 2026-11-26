package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	pkgerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type videoBillingReservationContextKey struct{}

type videoBillingReservationOutcome struct {
	mu                sync.Mutex
	repo              ImageBillingReservationRepository
	ref               UsageSettlementRef
	cache             *BillingCacheService
	cfg               *config.Config
	userID            int64
	groupID           *int64
	dispatched        bool
	dispatchFailed    bool
	accepted          bool
	rejected          bool
	uncertainAttempts int
}

func (s *GatewayService) ReserveVideoBilling(ctx context.Context, key *APIKey, sub *UserSubscription, req InflightEstimateRequest) (context.Context, func(), error) {
	return reserveVideoBilling(ctx, s.cfg, s.usageBillingRepo, s.billingCacheService, s.EstimateInflightReservation, key, sub, req)
}

func (s *OpenAIGatewayService) ReserveVideoBilling(ctx context.Context, key *APIKey, sub *UserSubscription, req InflightEstimateRequest) (context.Context, func(), error) {
	return reserveVideoBilling(ctx, s.cfg, s.usageBillingRepo, s.billingCacheService, s.EstimateInflightReservation, key, sub, req)
}

func reserveVideoBilling(
	ctx context.Context,
	cfg *config.Config,
	billing UsageBillingRepository,
	cache *BillingCacheService,
	estimate func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool),
	key *APIKey,
	sub *UserSubscription,
	req InflightEstimateRequest,
) (context.Context, func(), error) {
	noop := func() {}
	if cfg == nil || !cfg.Database.ImageBillingReservationEnabled || cfg.RunMode == config.RunModeSimple {
		return ctx, noop, nil
	}
	if UsageSettlementFromContext(ctx) != "" {
		return ctx, noop, nil
	}
	repo, ok := billing.(ImageBillingReservationRepository)
	if !ok || key == nil || key.User == nil {
		return ctx, noop, ErrBillingServiceUnavailable.WithCause(errors.New("video billing reservation unavailable"))
	}
	amount, priced := estimate(ctx, key, req)
	if !priced || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return ctx, noop, ErrBillingServiceUnavailable.WithCause(errors.New("video billing price unavailable"))
	}
	amount = QuantizeUsageBillingAmount(amount)
	var subscriptionID *int64
	var subscriptionGroupID *int64
	if key.Group != nil && key.Group.IsSubscriptionType() {
		if sub == nil {
			return ctx, noop, ErrSubscriptionNotFound
		}
		subscriptionID = &sub.ID
		subscriptionGroupID = key.GroupID
	}
	ref := UsageSettlementRef{RequestID: "settlement:video:" + uuid.NewString(), APIKeyID: key.ID}
	reserveCtx, cancel := context.WithTimeout(ctx, imageBillingReserveTimeout)
	err := repo.ReserveImageSettlement(reserveCtx, ref, key.UserID, subscriptionID, amount)
	cancel()
	if err != nil {
		logger.FromContext(ctx).Error("video.reservation_failed", zap.String("settlement_request_id", ref.RequestID), zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
		if pkgerrors.Code(err) >= http.StatusInternalServerError {
			return ctx, noop, ErrBillingServiceUnavailable.WithCause(err)
		}
		return ctx, noop, err
	}
	outcome := &videoBillingReservationOutcome{
		repo:    repo,
		ref:     ref,
		cache:   cache,
		cfg:     cfg,
		userID:  key.UserID,
		groupID: subscriptionGroupID,
	}
	ctx = context.WithValue(WithUsageSettlement(ctx, ref.RequestID), videoBillingReservationContextKey{}, outcome)
	invalidateVideoReservationCaches(ctx, cache, key.UserID, key.GroupID)
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			outcome.mu.Lock()
			release := !outcome.accepted && !outcome.dispatchFailed && (!outcome.dispatched || outcome.rejected && outcome.uncertainAttempts == 0)
			outcome.mu.Unlock()
			if release {
				releaseVideoBillingReservation(context.WithoutCancel(ctx), outcome.repo, outcome.cache, outcome.cfg, outcome.ref, outcome.userID, outcome.groupID)
			}
		})
	}, nil
}

func BeginVideoBillingDispatch(ctx context.Context) error {
	outcome, _ := ctx.Value(videoBillingReservationContextKey{}).(*videoBillingReservationOutcome)
	if outcome == nil {
		return nil
	}
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	if outcome.dispatched {
		return nil
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := outcome.repo.MarkImageSettlementDispatched(dispatchCtx, outcome.ref); err != nil {
		outcome.dispatchFailed = true
		return err
	}
	outcome.dispatched = true
	return nil
}

// ObserveVideoBillingAttempt records whether a create attempt was definitely
// rejected. A transport error or server response can follow an accepted async
// job, so those outcomes keep the reservation until a later status resolves it.
func ObserveVideoBillingAttempt(ctx context.Context) func(*http.Response, error) {
	outcome, _ := ctx.Value(videoBillingReservationContextKey{}).(*videoBillingReservationOutcome)
	if outcome == nil {
		return func(*http.Response, error) {}
	}
	var once sync.Once
	return func(resp *http.Response, err error) {
		once.Do(func() {
			outcome.mu.Lock()
			defer outcome.mu.Unlock()
			if err == nil && resp != nil && resp.StatusCode >= http.StatusBadRequest && resp.StatusCode < http.StatusInternalServerError &&
				resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusConflict {
				outcome.rejected = true
				return
			}
			outcome.uncertainAttempts++
		})
	}
}

func CommitVideoBillingReservation(ctx context.Context) {
	outcome, _ := ctx.Value(videoBillingReservationContextKey{}).(*videoBillingReservationOutcome)
	if outcome == nil {
		return
	}
	outcome.mu.Lock()
	outcome.accepted = true
	outcome.mu.Unlock()
}

func RejectVideoBillingReservation(ctx context.Context) {
	outcome, _ := ctx.Value(videoBillingReservationContextKey{}).(*videoBillingReservationOutcome)
	if outcome == nil {
		return
	}
	outcome.mu.Lock()
	outcome.rejected = true
	if outcome.uncertainAttempts > 0 {
		outcome.uncertainAttempts--
	}
	outcome.mu.Unlock()
}

func (s *OpenAIGatewayService) ReleaseVideoBillingReservation(ctx context.Context, requestID string, key *APIKey) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil
	}
	if !strings.HasPrefix(requestID, "settlement:video:") || key == nil || key.ID <= 0 || key.UserID <= 0 {
		return ErrUsageSettlementConflict
	}
	repo, ok := s.usageBillingRepo.(ImageBillingReservationRepository)
	if !ok {
		return ErrBillingServiceUnavailable.WithCause(errors.New("video billing reservation unavailable"))
	}
	var subscriptionGroupID *int64
	if key.Group != nil && key.Group.IsSubscriptionType() {
		subscriptionGroupID = key.GroupID
	}
	return releaseVideoBillingReservation(ctx, repo, s.billingCacheService, s.cfg, UsageSettlementRef{RequestID: requestID, APIKeyID: key.ID}, key.UserID, subscriptionGroupID)
}

func releaseVideoBillingReservation(
	ctx context.Context,
	repo ImageBillingReservationRepository,
	cache *BillingCacheService,
	cfg *config.Config,
	ref UsageSettlementRef,
	userID int64,
	groupID *int64,
) error {
	releaseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := repo.ReleaseImageSettlement(releaseCtx, ref)
	cancel()
	if err != nil {
		logger.FromContext(ctx).Error("video.reservation_release_failed", zap.String("request_id", ref.RequestID), zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
		if !errors.Is(err, ErrUsageSettlementConflict) && cfg != nil {
			if _, journalErr := saveImageSettlementRelease(cfg, ref); journalErr != nil {
				logger.FromContext(ctx).Error("video.reservation_release_journal_failed", zap.String("request_id", ref.RequestID), zap.Error(journalErr), zap.Bool(logger.OpsSystemLogSkipField, true))
			}
		}
		return err
	}
	invalidateVideoReservationCaches(ctx, cache, userID, groupID)
	return nil
}

func invalidateVideoReservationCaches(ctx context.Context, cache *BillingCacheService, userID int64, groupID *int64) {
	if cache == nil {
		return
	}
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := cache.InvalidateUserBalance(cacheCtx, userID); err != nil {
		logger.FromContext(ctx).Warn("video.reservation_cache_invalidation_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
	}
	if groupID != nil {
		if err := cache.InvalidateSubscription(cacheCtx, userID, *groupID); err != nil {
			logger.FromContext(ctx).Warn("video.reservation_subscription_cache_invalidation_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
		}
	}
}
