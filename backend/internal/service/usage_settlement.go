package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var ErrUsageSettlementNotReady = errors.New("usage settlement has no final facts")
var ErrUsageSettlementConflict = errors.New("usage settlement facts conflict")

type usageSettlementContextKey struct{}

func WithUsageSettlement(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, usageSettlementContextKey{}, id)
}
func UsageSettlementFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(usageSettlementContextKey{}).(string)
	return id
}

// Versioned, fixed financial facts. Usage is built with an explicit whitelist;
// no related entities, credentials, prompt, image payload or request body.
type UsageSettlementSnapshot struct {
	Version      int
	Command      UsageBillingCommand
	Usage        UsageLog
	Platform     string
	PlatformCost float64
	DayStart     time.Time
	WeekStart    time.Time
}
type UsageSettlementRef struct {
	RequestID string
	APIKeyID  int64
}
type UsageSettlementRepository interface {
	BeginSettlement(context.Context, string, int64, int64) error
	PrepareSettlement(context.Context, *UsageSettlementSnapshot) error
	ApplySettlement(context.Context, UsageSettlementRef, *ImageGenerationDetail) (*UsageSettlementSnapshot, *UsageBillingApplyResult, error)
	CompleteSettlement(context.Context, UsageSettlementRef) error
	ClaimSettlement(context.Context) (*UsageSettlementRef, error)
	FailSettlement(context.Context, UsageSettlementRef, bool) error
	ReviewExpiredSettlements(context.Context) (int64, error)
	UpdateSettlementImageDetail(context.Context, UsageSettlementRef, *ImageGenerationDetail) error
}

// BeginGeminiImageSettlement runs before the first upstream attempt of each
// native child; failover retains this identity, siblings get distinct identities.
func (s *GatewayService) BeginGeminiImageSettlement(ctx context.Context, key *APIKey) (context.Context, error) {
	if UsageSettlementFromContext(ctx) != "" {
		return ctx, nil
	}
	if s.cfg == nil || !s.cfg.Database.UsageSettlementEnabled || s.cfg.RunMode == config.RunModeSimple {
		return ctx, nil
	}
	repo, ok := s.usageBillingRepo.(UsageSettlementRepository)
	if !ok || key == nil {
		return ctx, errors.New("durable usage settlement unavailable")
	}
	id := "settlement:" + uuid.NewString()
	beginCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := repo.BeginSettlement(beginCtx, id, key.ID, key.UserID); err != nil {
		return ctx, err
	}
	return WithUsageSettlement(ctx, id), nil
}

func newUsageSettlementSnapshot(cmd *UsageBillingCommand, u *UsageLog, p *postUsageBillingParams) *UsageSettlementSnapshot {
	// Explicit scalar whitelist prevents newly added relations from entering the
	// recovery table. Image details remain in usage_logs on the live path only.
	v := UsageLog{
		UserID: u.UserID, APIKeyID: u.APIKeyID, AccountID: u.AccountID, RequestID: u.RequestID,
		Model: u.Model, RequestedModel: u.RequestedModel, UpstreamModel: u.UpstreamModel,
		UpstreamResponseModel: u.UpstreamResponseModel, UpstreamModelMismatch: u.UpstreamModelMismatch,
		ChannelID: u.ChannelID, ModelMappingChain: u.ModelMappingChain, BillingTier: u.BillingTier, BillingMode: u.BillingMode,
		ServiceTier: u.ServiceTier, ReasoningEffort: u.ReasoningEffort, RequestedReasoningEffort: u.RequestedReasoningEffort,
		InboundEndpoint: u.InboundEndpoint, UpstreamEndpoint: u.UpstreamEndpoint, GroupID: u.GroupID, SubscriptionID: u.SubscriptionID,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheCreationTokens: u.CacheCreationTokens, CacheReadTokens: u.CacheReadTokens,
		CacheCreation5mTokens: u.CacheCreation5mTokens, CacheCreation1hTokens: u.CacheCreation1hTokens,
		ImageInputTokens: u.ImageInputTokens, ImageOutputTokens: u.ImageOutputTokens, ImageInputCost: u.ImageInputCost, ImageOutputCost: u.ImageOutputCost,
		InputCost: u.InputCost, OutputCost: u.OutputCost, CacheCreationCost: u.CacheCreationCost, CacheReadCost: u.CacheReadCost,
		TotalCost: u.TotalCost, ActualCost: u.ActualCost, RateMultiplier: u.RateMultiplier, LongContextBillingApplied: u.LongContextBillingApplied,
		AccountRateMultiplier: u.AccountRateMultiplier, AccountStatsCost: u.AccountStatsCost,
		BillingType: u.BillingType, RequestType: u.RequestType, Stream: u.Stream, OpenAIWSMode: u.OpenAIWSMode, NativeCompactionV2: u.NativeCompactionV2,
		DurationMs: u.DurationMs, FirstTokenMs: u.FirstTokenMs, UpstreamRequestID: u.UpstreamRequestID, CacheTTLOverridden: u.CacheTTLOverridden,
		ImageCount: u.ImageCount, ImageSize: u.ImageSize, ImageInputSize: u.ImageInputSize, ImageOutputSize: u.ImageOutputSize,
		ImageSizeSource: u.ImageSizeSource, ImageSizeBreakdown: u.ImageSizeBreakdown, MediaType: u.MediaType, HasImageGeneration: u.HasImageGeneration,
		CreatedAt: u.CreatedAt,
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	v.ActualCost = QuantizeUsageBillingAmount(v.ActualCost)
	snapshot := &UsageSettlementSnapshot{Version: 1, Command: *cmd, Usage: v, DayStart: timezone.StartOfDay(v.CreatedAt), WeekStart: timezone.StartOfWeek(v.CreatedAt)}
	snapshot.Command.EffectiveAt = v.CreatedAt
	snapshot.Command.EffectiveDayStart = snapshot.DayStart
	if !p.IsSubscriptionBill && !p.SimpleModeKeyRateLimitOnly {
		snapshot.Platform = p.Platform
		snapshot.PlatformCost = QuantizeUsageBillingAmount(p.Cost.ActualCost)
		snapshot.Command.PlatformQuota = &UsageBillingPlatformQuota{Platform: snapshot.Platform, Cost: snapshot.PlatformCost, OccurredAt: v.CreatedAt, DayStart: snapshot.DayStart, WeekStart: snapshot.WeekStart}
	}
	return snapshot
}

func finishUsageSettlement(ctx context.Context, repo UsageSettlementRepository, cache *BillingCacheService, s *UsageSettlementSnapshot) error {
	if cache == nil {
		return errors.New("settlement cache invalidation unavailable")
	}
	// Invalidation is idempotent and retryable even after a commit-unknown result.
	// Never replay Redis increments or postUsageBilling's platform counter writes.
	if err := cache.InvalidateUserBalance(ctx, s.Command.UserID); err != nil {
		return err
	}
	if s.Usage.GroupID != nil && s.Command.SubscriptionID != nil {
		if err := cache.InvalidateSubscription(ctx, s.Command.UserID, *s.Usage.GroupID); err != nil {
			return err
		}
	}
	if err := cache.InvalidateAPIKeyRateLimit(ctx, s.Command.APIKeyID); err != nil {
		return err
	}
	if cache.cache != nil && s.Platform != "" {
		if err := cache.cache.DeleteUserPlatformQuotaCache(ctx, s.Command.UserID, s.Platform); err != nil {
			return err
		}
	}
	return repo.CompleteSettlement(ctx, UsageSettlementRef{s.Command.RequestID, s.Command.APIKeyID})
}

func applyDurableUsageSettlement(ctx context.Context, repo UsageSettlementRepository, cmd *UsageBillingCommand, u *UsageLog, p *postUsageBillingParams, deps *billingDeps) (bool, error) {
	if u == nil || cmd.RequestID != UsageSettlementFromContext(ctx) {
		return false, ErrUsageSettlementConflict
	}
	if p.Cost.CalculationError != nil {
		return false, fmt.Errorf("settlement pricing unavailable: %w", p.Cost.CalculationError)
	}
	u.classifyImageRequestType(ctx)
	snapshot := newUsageSettlementSnapshot(cmd, u, p)
	var journalPath string
	var journalErr error
	if deps.cfg != nil && deps.cfg.Database.ImageBillingReservationEnabled {
		journalPath, journalErr = saveImageSettlementFacts(deps.cfg, snapshot)
		if errors.Is(journalErr, ErrUsageSettlementConflict) {
			return false, journalErr
		}
		if journalErr != nil {
			logger.L().Error("image.settlement_journal_failed", zap.String("request_id", cmd.RequestID), zap.Error(journalErr), zap.Bool(logger.OpsSystemLogSkipField, true))
		}
	}
	if err := prepareUsageSettlementWithRetry(ctx, repo, snapshot); err != nil {
		return false, fmt.Errorf("prepare settlement: %w", errors.Join(err, journalErr))
	}
	if journalPath != "" {
		if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.L().Warn("image.settlement_journal_cleanup_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
		}
	}
	snapshot, result, err := repo.ApplySettlement(ctx, UsageSettlementRef{cmd.RequestID, cmd.APIKeyID}, nil)
	if err != nil {
		return false, err
	}
	if deps.deferredService != nil && u.ImageGeneration != nil {
		deps.deferredService.ScheduleSettlementImageDetail(repo, UsageSettlementRef{cmd.RequestID, cmd.APIKeyID}, u.ImageGeneration)
	}
	if result != nil && result.APIKeyQuotaExhausted && p.APIKey != nil {
		if invalidator, ok := p.APIKeyService.(apiKeyAuthCacheInvalidator); ok {
			invalidator.InvalidateAuthCacheByKey(ctx, p.APIKey.Key)
		}
	}
	if err = finishUsageSettlement(ctx, repo, deps.billingCacheService, snapshot); err != nil {
		return false, err
	}
	if deps.deferredService != nil {
		deps.deferredService.ScheduleLastUsedUpdate(cmd.AccountID)
	}
	if result != nil && result.Applied && deps.balanceNotifyService != nil {
		deps.balanceNotifyService.submitUsageNotifications(p, deps, result)
	}
	return result != nil && result.Applied, nil
}

// Image metadata can be completed after an n>1 batch without delaying the
// immutable financial snapshot or rewriting its amount/fingerprint.
func (s *GatewayService) CompleteGeminiImageSettlementDetail(ctx context.Context, keyID int64) error {
	repo, ok := s.usageBillingRepo.(UsageSettlementRepository)
	if !ok {
		return errors.New("durable usage settlement unavailable")
	}
	if s.deferredService != nil {
		s.deferredService.ScheduleSettlementImageDetail(repo, UsageSettlementRef{RequestID: UsageSettlementFromContext(ctx), APIKeyID: keyID}, ImageGenerationDetailFromContext(ctx))
	}
	return nil
}

func prepareUsageSettlementWithRetry(ctx context.Context, repo UsageSettlementRepository, s *UsageSettlementSnapshot) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = repo.PrepareSettlement(attemptCtx, s)
		cancel()
		if err == nil || errors.Is(err, ErrUsageSettlementConflict) || ctx.Err() != nil {
			return err
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}

type UsageSettlementWorker struct {
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func ProvideUsageSettlementWorker(billing UsageBillingRepository, cache *BillingCacheService, cfg *config.Config) (*UsageSettlementWorker, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	w := &UsageSettlementWorker{cancel: cancel, done: make(chan struct{})}
	repo, ok := billing.(UsageSettlementRepository)
	if !ok || !cfg.Database.DurableUsageQuota() {
		close(w.done)
		return w, w.Stop
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var lastLog time.Time
		var journalCursor string
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			workCtx, stop := context.WithTimeout(ctx, 15*time.Second)
			if err := replayImageSettlementFacts(workCtx, cfg, repo, &journalCursor); err != nil && time.Since(lastLog) >= time.Minute {
				logger.L().Error("image.settlement_journal_recovery_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
			}
			if releases, ok := billing.(interface {
				RetryImageSettlementRelease(context.Context, func(context.Context, int64) error) error
			}); ok {
				invalidate := func(ctx context.Context, userID int64) error {
					if cache == nil {
						return errors.New("reservation cache invalidation unavailable")
					}
					return cache.InvalidateUserBalance(ctx, userID)
				}
				if err := releases.RetryImageSettlementRelease(workCtx, invalidate); err != nil {
					logger.L().Warn("image.reservation_release_retry_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
				}
			}
			ref, err := repo.ClaimSettlement(workCtx)
			if err == nil && ref != nil {
				var snapshot *UsageSettlementSnapshot
				snapshot, _, err = repo.ApplySettlement(workCtx, *ref, nil)
				if err == nil {
					err = finishUsageSettlement(workCtx, repo, cache, snapshot)
				}
			}
			stop()
			if err != nil && ref != nil && ctx.Err() == nil {
				retryCtx, done := context.WithTimeout(ctx, 2*time.Second)
				permanent := errors.Is(err, ErrUsageBillingRequestConflict) || errors.Is(err, ErrUsageSettlementConflict) || errors.Is(err, ErrUsageSettlementNotReady)
				if failErr := repo.FailSettlement(retryCtx, *ref, permanent); failErr != nil {
					logger.L().Warn("usage.settlement_retry_record_failed", zap.Error(failErr), zap.Bool(logger.OpsSystemLogSkipField, true))
				}
				done()
			}
			if time.Since(lastLog) >= time.Minute {
				lastLog = time.Now()
				if err != nil {
					logger.L().Warn("usage.settlement_recovery_failed", zap.Error(err), zap.Bool(logger.OpsSystemLogSkipField, true))
				}
				checkCtx, done := context.WithTimeout(ctx, 2*time.Second)
				count, checkErr := repo.ReviewExpiredSettlements(checkCtx)
				done()
				if checkErr != nil || count > 0 {
					logger.L().Warn("usage.settlement_needs_review", zap.Int64("count", count), zap.Error(checkErr), zap.Bool(logger.OpsSystemLogSkipField, true))
				}
			}
		}
	}()
	return w, w.Stop
}
func (w *UsageSettlementWorker) Stop() {
	if w != nil {
		w.once.Do(func() { w.cancel(); <-w.done })
	}
}
