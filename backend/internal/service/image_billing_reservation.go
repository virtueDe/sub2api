package service

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type ImageBillingReservationRepository interface {
	ReserveImageSettlement(context.Context, UsageSettlementRef, int64, *int64, float64) error
	ReleaseImageSettlement(context.Context, UsageSettlementRef) error
	MarkImageSettlementDispatched(context.Context, UsageSettlementRef) error
}

type imageReservationContextKey struct{}
type imageReservationOutcome struct {
	mu                 sync.Mutex
	attempts, pending  int
	uncertain          bool
	dispatched         bool
	dispatchFailed     bool
	failedWithoutImage bool
	imageObserved      bool
	settling           bool
	repo               ImageBillingReservationRepository
	ref                UsageSettlementRef
}

// A durable dispatch fence distinguishes a pre-upstream crash (safe to refund)
// from an accepted request whose final outcome has not yet been recorded.
func BeginImageBillingDispatch(ctx context.Context) error {
	outcome, _ := ctx.Value(imageReservationContextKey{}).(*imageReservationOutcome)
	if outcome == nil {
		return nil
	}
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	if outcome.dispatched {
		return nil
	}
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := outcome.repo.MarkImageSettlementDispatched(dbCtx, outcome.ref); err != nil {
		// Commit-unknown is deliberately retained for investigation.
		outcome.uncertain = true
		outcome.dispatchFailed = true
		return err
	}
	outcome.dispatched = true
	return nil
}

// WebSocket errors cannot be classified by an HTTP rejection status.
func BeginImageBillingUnknownTransport(ctx context.Context) error {
	if err := BeginImageBillingDispatch(ctx); err != nil {
		return err
	}
	outcome, _ := ctx.Value(imageReservationContextKey{}).(*imageReservationOutcome)
	if outcome != nil {
		outcome.mu.Lock()
		outcome.uncertain = true
		outcome.failedWithoutImage = false
		outcome.mu.Unlock()
	}
	return nil
}

// Failed attempts without a response/image are refunded under the customer
// billing policy, even when the provider's internal outcome is unknown. Headers
// alone do not prove image generation; the forwarding result refines this below.
func ObserveImageBillingAttempt(ctx context.Context) func(*http.Response, error) {
	outcome, _ := ctx.Value(imageReservationContextKey{}).(*imageReservationOutcome)
	if outcome == nil {
		return func(*http.Response, error) {}
	}
	outcome.mu.Lock()
	outcome.attempts++
	outcome.pending++
	outcome.failedWithoutImage = false
	outcome.mu.Unlock()
	var once sync.Once
	return func(resp *http.Response, err error) {
		once.Do(func() {
			rejected := resp == nil && err != nil
			if err == nil && resp != nil {
				switch resp.StatusCode {
				case 400, 401, 403, 404, 405, 408, 413, 415, 422, 429,
					http.StatusUnavailableForLegalReasons, http.StatusBadGateway, 499, 524:
					rejected = true
				}
			}
			outcome.mu.Lock()
			outcome.pending--
			outcome.uncertain = outcome.uncertain || !rejected
			outcome.mu.Unlock()
		})
	}
}

// ObserveImageBillingResult records the completed forwarding attempt, before
// retries or billing. A real image takes precedence over a later disconnect.
// An error without any image permits a refund, including errors inside a 200
// stream. This does not claim that the provider incurred no internal cost.
func ObserveImageBillingResult(ctx context.Context, hasImage, clientDisconnected bool, err error) {
	outcome, _ := ctx.Value(imageReservationContextKey{}).(*imageReservationOutcome)
	if outcome == nil {
		return
	}
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	outcome.imageObserved = outcome.imageObserved || hasImage
	outcome.failedWithoutImage = (err != nil || clientDisconnected) && !hasImage
	outcome.uncertain = outcome.uncertain || err == nil || hasImage
}

// BeginImageBillingSettlement protects successful output before pricing or
// persistence can fail. False means a failed image request has no output to bill.
// Non-image requests keep their existing usage accounting behavior.
func BeginImageBillingSettlement(ctx context.Context) bool {
	outcome, _ := ctx.Value(imageReservationContextKey{}).(*imageReservationOutcome)
	if outcome == nil {
		return true
	}
	outcome.mu.Lock()
	defer outcome.mu.Unlock()
	if outcome.failedWithoutImage && !outcome.imageObserved {
		return false
	}
	outcome.settling = true
	return true
}

func (s *GatewayService) ReserveImageBilling(ctx context.Context, key *APIKey, sub *UserSubscription, req InflightEstimateRequest) (context.Context, func(), error) {
	return reserveImageBilling(ctx, s.cfg, s.usageBillingRepo, s.billingCacheService, s.EstimateInflightReservation, key, sub, req)
}
func (s *OpenAIGatewayService) ReserveImageBilling(ctx context.Context, key *APIKey, sub *UserSubscription, req InflightEstimateRequest) (context.Context, func(), error) {
	return reserveImageBilling(ctx, s.cfg, s.usageBillingRepo, s.billingCacheService, s.EstimateInflightReservation, key, sub, req)
}

func reserveImageBilling(ctx context.Context, cfg *config.Config, billing UsageBillingRepository, cache *BillingCacheService,
	estimate func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool),
	key *APIKey, sub *UserSubscription, req InflightEstimateRequest) (context.Context, func(), error) {
	noop := func() {}
	if cfg == nil || !cfg.Database.ImageBillingReservationEnabled || cfg.RunMode == config.RunModeSimple {
		return ctx, noop, nil
	}
	if UsageSettlementFromContext(ctx) != "" {
		return ctx, noop, nil
	}
	repo, ok := billing.(ImageBillingReservationRepository)
	if !ok || key == nil || key.User == nil {
		return ctx, noop, errors.New("image billing reservation unavailable")
	}
	amount, priced := estimate(ctx, key, req)
	if !priced || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return ctx, noop, errors.New("image billing price unavailable")
	}
	amount = QuantizeUsageBillingAmount(amount)
	var subID *int64
	if key.Group != nil && key.Group.IsSubscriptionType() {
		if sub == nil {
			return ctx, noop, ErrSubscriptionNotFound
		}
		subID = &sub.ID
	}
	ref := UsageSettlementRef{RequestID: "settlement:" + uuid.NewString(), APIKeyID: key.ID}
	reserveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err := repo.ReserveImageSettlement(reserveCtx, ref, key.UserID, subID, amount)
	cancel()
	if err != nil {
		logger.L().Error("image.reservation_failed", zap.String("request_id", ref.RequestID), zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
		return ctx, noop, err
	}
	outcome := &imageReservationOutcome{repo: repo, ref: ref}
	ctx = context.WithValue(WithUsageSettlement(ctx, ref.RequestID), imageReservationContextKey{}, outcome)
	invalidate := func() {
		if cache == nil {
			return
		}
		c, done := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer done()
		if err := cache.InvalidateUserBalance(c, key.UserID); err != nil {
			logger.L().Warn("image.reservation_cache_invalidation_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
		}
	}
	invalidate()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			outcome.mu.Lock()
			rejected := outcome.pending == 0 && !outcome.dispatchFailed && !outcome.imageObserved && !outcome.settling &&
				(outcome.failedWithoutImage || (!outcome.uncertain && (!outcome.dispatched || outcome.attempts > 0)))
			outcome.mu.Unlock()
			if !rejected {
				return
			}
			releaseCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer done()
			if err := repo.ReleaseImageSettlement(releaseCtx, ref); err != nil {
				logger.L().Error("image.reservation_release_failed", zap.String("request_id", ref.RequestID), zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
				if !errors.Is(err, ErrUsageSettlementConflict) {
					if _, journalErr := saveImageSettlementRelease(cfg, ref); journalErr != nil {
						logger.L().Error("image.reservation_release_journal_failed", zap.String("request_id", ref.RequestID), zap.Error(journalErr), zap.Bool(logger.OpsSystemLogSkipField, true))
					}
				}
				return
			}
			invalidate()
		})
	}, nil
}
