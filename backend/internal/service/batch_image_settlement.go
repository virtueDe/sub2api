package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	batchImageSettlementRequestPrefix = "batch_image_settlement:"
	batchImageSettlementRetryDelay    = time.Minute
	batchImageSettlementMaxRetries    = 5
	batchImageCostEpsilon             = 0.00000001
)

type BatchImagePricingResolver interface {
	BatchImageUnitPrice(ctx context.Context, job *BatchImageJob) (float64, error)
}

type BatchImageModelPricingResolver struct {
	Resolver *ModelPricingResolver
}

func (r *BatchImageModelPricingResolver) BatchImageUnitPrice(ctx context.Context, job *BatchImageJob) (float64, error) {
	if r == nil || r.Resolver == nil || job == nil || strings.TrimSpace(job.Model) == "" {
		return 0, ErrBatchImageSettlementPricingMissing
	}
	resolved := r.Resolver.Resolve(ctx, PricingInput{Model: job.Model})
	if resolved == nil {
		return 0, ErrBatchImageSettlementPricingMissing
	}
	switch resolved.Mode {
	case BillingModeImage, BillingModePerRequest:
		if resolved.DefaultPerRequestPrice > 0 {
			return resolved.DefaultPerRequestPrice, nil
		}
		if len(resolved.RequestTiers) == 1 && resolved.RequestTiers[0].PerRequestPrice != nil && *resolved.RequestTiers[0].PerRequestPrice >= 0 {
			return *resolved.RequestTiers[0].PerRequestPrice, nil
		}
	case BillingModeToken:
		if resolved.BasePricing != nil && (resolved.BasePricing.ImageOutputPriceExplicit || resolved.BasePricing.ImageOutputPricePerToken > 0) {
			return resolved.BasePricing.ImageOutputPricePerToken, nil
		}
	}
	return 0, ErrBatchImageSettlementPricingMissing
}

type BatchImageAtomicSettlementRepository interface {
	SettleBatchImage(context.Context, *BatchImageJob, *BatchImageBalanceHoldCommand, *UsageLog, MarkBatchImageJobSettledParams) error
}

type BatchImageSettlementService struct {
	Repo         BatchImageRepository
	BillingRepo  UsageBillingRepository
	UsageLogRepo UsageLogRepository
	Pricing      BatchImagePricingResolver
	AuthCache    APIKeyAuthCacheInvalidator
	Config       *config.Config
}

type BatchImageSettlementResult struct {
	BatchID        string
	SuccessCount   int
	FailCount      int
	ActualCost     float64
	ManifestHash   string
	RequestID      string
	AlreadySettled bool
}

func (s *BatchImageSettlementService) Settle(ctx context.Context, batchID string) (*BatchImageSettlementResult, error) {
	if s == nil || s.Repo == nil || s.BillingRepo == nil || s.Pricing == nil {
		return nil, ErrBatchImageSettlementBillingFailed.WithCause(errors.New("batch image settlement service is not configured"))
	}
	job, err := s.Repo.GetBatchImageJobByBatchID(ctx, batchID)
	if err != nil {
		return nil, err
	}

	manifestHash := BuildBatchImageSettlementManifestHash(job)
	result := &BatchImageSettlementResult{
		BatchID:      job.BatchID,
		SuccessCount: job.SuccessCount,
		FailCount:    job.FailCount,
		ManifestHash: manifestHash,
		RequestID:    BatchImageCaptureRequestID(job.BatchID),
	}
	if job.ActualCost != nil {
		result.ActualCost = *job.ActualCost
	}
	if job.Status == BatchImageJobStatusCompleted {
		result.AlreadySettled = true
		return result, nil
	}
	if job.Status != BatchImageJobStatusSettling {
		return nil, ErrBatchImageSettlementInvalidStatus
	}
	if job.APIKeyID == nil || *job.APIKeyID <= 0 {
		return nil, ErrBatchImageSettlementMissingAPIKeyID
	}
	if job.AccountID == nil || *job.AccountID <= 0 {
		return nil, ErrBatchImageSettlementMissingAccountID
	}
	if job.SuccessCount < 0 || job.FailCount < 0 || job.ItemCount < 0 || job.SuccessCount+job.FailCount > job.ItemCount {
		if failErr := s.recordSettlementFailure(ctx, job, "SETTLEMENT_INVALID_COUNTS",
			fmt.Sprintf("success=%d fail=%d item_count=%d", job.SuccessCount, job.FailCount, job.ItemCount)); failErr != nil {
			return nil, failErr
		}
		return nil, ErrBatchImageSettlementInvalidCounts
	}
	if strings.TrimSpace(batchImageDerefString(job.ManifestHash)) != "" && batchImageDerefString(job.ManifestHash) != manifestHash {
		if failErr := s.recordSettlementFailure(ctx, job, "SETTLEMENT_MANIFEST_CONFLICT", "manifest hash conflict"); failErr != nil {
			return nil, failErr
		}
		return nil, ErrBatchImageSettlementManifestConflict
	}

	unitPrice, err := s.settlementUnitPrice(ctx, job)
	if err == nil && unitPrice < 0 {
		err = ErrBatchImageSettlementPricingMissing
	}
	if err != nil {
		if failErr := s.recordSettlementFailure(ctx, job, "SETTLEMENT_PRICING_MISSING", err.Error()); failErr != nil {
			return nil, failErr
		}
		return nil, err
	}
	actualCost := float64(job.SuccessCount) * unitPrice
	result.ActualCost = actualCost
	holdAmount := job.EstimatedCost
	if job.HoldAmount != nil {
		holdAmount = *job.HoldAmount
	}
	if actualCost-holdAmount > batchImageCostEpsilon {
		msg := fmt.Sprintf("actual cost %.10f exceeds held amount %.10f", actualCost, holdAmount)
		if failErr := s.recordSettlementFailure(ctx, job, "SETTLEMENT_COST_EXCEEDS_HOLD", msg); failErr != nil {
			return nil, failErr
		}
		return nil, ErrBatchImageSettlementCostExceedsHold
	}

	atomicRepo, ok := s.BillingRepo.(BatchImageAtomicSettlementRepository)
	if !ok {
		return nil, ErrBatchImageSettlementBillingFailed.WithCause(errors.New("atomic batch image settlement unavailable"))
	}
	now := time.Now()
	outputExpiresAt := now.Add(s.outputRetentionAfterTerminal())
	cmd, err := buildBatchImageHoldCommand(job, result.RequestID, actualCost, manifestHash)
	if err != nil {
		return nil, err
	}
	cmd.Normalize() // retain legacy fingerprint before monetary quantization
	actualCost = QuantizeUsageBillingAmount(actualCost)
	cmd.ActualAmount = actualCost
	cmd.HoldAmount = QuantizeUsageBillingAmount(cmd.HoldAmount)
	result.ActualCost = actualCost
	usage := batchImageMinimalUsage(job, actualCost, result.RequestID, now)
	if err := atomicRepo.SettleBatchImage(ctx, job, cmd, usage, MarkBatchImageJobSettledParams{BatchID: job.BatchID, ActualCost: actualCost, ManifestHash: manifestHash, Now: &now, OutputExpiresAt: &outputExpiresAt}); err != nil {
		if failErr := s.recordSettlementFailure(ctx, job, "SETTLEMENT_BILLING_FAILED", err.Error()); failErr != nil {
			return nil, failErr
		}
		return nil, ErrBatchImageSettlementBillingFailed.WithCause(err)
	}
	s.invalidateAuthCache(ctx, job.UserID)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := s.Repo.AppendBatchImageEvent(auditCtx, job.BatchID, "settlement_completed", map[string]any{"request_id": result.RequestID, "actual_cost": actualCost}); err != nil {
		logger.L().Warn("batch_image.settlement_audit_failed", zap.String("batch_id", job.BatchID), zap.Error(err))
	}
	return result, nil
}

// Successful generation retains its hold even after repeated settlement errors.
// The worker retries transient failures; conflicting facts require investigation.
func (s *BatchImageSettlementService) recordSettlementFailure(ctx context.Context, job *BatchImageJob, code, message string) error {
	retryCount, err := s.Repo.SetBatchImageJobSettlementFailed(ctx, job.BatchID, code, truncateBatchImageMessage(message, batchImageMaxErrorMessageLength))
	if err != nil {
		return ErrBatchImageSettlementBillingFailed.WithCause(err)
	}
	if retryCount >= batchImageSettlementMaxRetries {
		logger.L().Error("batch_image.settlement_requires_attention", zap.String("batch_id", job.BatchID), zap.String("code", code), zap.Int("attempts", retryCount))
	}
	return nil
}

func batchImageMinimalUsage(job *BatchImageJob, actualCost float64, requestID string, createdAt time.Time) *UsageLog {
	billingMode := string(BillingModeImage)
	accountRateMultiplier := job.AccountRateMultiplier
	inboundEndpoint := "/v1/images/batches"
	upstreamEndpoint := "vertex:batchPredictionJobs"
	imageSize := "1K"
	usageLog := &UsageLog{
		UserID:                job.UserID,
		APIKeyID:              *job.APIKeyID,
		AccountID:             *job.AccountID,
		RequestID:             strings.TrimSpace(requestID),
		Model:                 job.Model,
		RequestedModel:        job.Model,
		InboundEndpoint:       &inboundEndpoint,
		UpstreamEndpoint:      &upstreamEndpoint,
		ImageCount:            job.SuccessCount,
		ImageOutputCost:       actualCost,
		TotalCost:             actualCost,
		ActualCost:            actualCost,
		RateMultiplier:        job.GroupRateMultiplier * job.BatchDiscountMultiplier,
		AccountRateMultiplier: &accountRateMultiplier,
		BillingType:           BillingTypeBalance,
		RequestType:           RequestTypeImageAsync,
		BillingMode:           &billingMode,
		ImageSize:             &imageSize,
		SessionID:             job.SessionID,
		CreatedAt:             createdAt,
	}
	return usageLog
}

func (s *BatchImageSettlementService) invalidateAuthCache(ctx context.Context, userID int64) {
	if s != nil && s.AuthCache != nil && userID > 0 {
		s.AuthCache.InvalidateAuthCacheByUserID(ctx, userID)
	}
}

func (s *BatchImageSettlementService) settlementUnitPrice(ctx context.Context, job *BatchImageJob) (float64, error) {
	if job != nil && job.PricingSnapshotVersion >= 1 {
		if job.BillableUnitPrice < 0 {
			return 0, ErrBatchImageSettlementPricingMissing
		}
		return job.BillableUnitPrice, nil
	}
	unitPrice, err := s.Pricing.BatchImageUnitPrice(ctx, job)
	if err != nil {
		return 0, err
	}
	return unitPrice, nil
}

func (s *BatchImageSettlementService) outputRetentionAfterTerminal() time.Duration {
	if s != nil && s.Config != nil && s.Config.BatchImage.OutputRetentionAfterTerminalHours > 0 {
		return time.Duration(s.Config.BatchImage.OutputRetentionAfterTerminalHours) * time.Hour
	}
	return 72 * time.Hour
}

func BatchImageSettlementRequestID(batchID string) string {
	return batchImageSettlementRequestPrefix + strings.TrimSpace(batchID)
}

func BuildBatchImageSettlementManifestHash(job *BatchImageJob) string {
	if job == nil {
		return ""
	}
	parts := []string{
		strings.TrimSpace(job.BatchID),
		strings.TrimSpace(job.Provider),
		strings.TrimSpace(job.Model),
		batchImageDerefString(job.ProviderJobName),
		batchImageDerefString(job.ProviderOutputRef),
		strconv.Itoa(job.SuccessCount),
		strconv.Itoa(job.FailCount),
		strconv.Itoa(job.ItemCount),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

type BatchImagePipelineProcessor struct {
	ProviderProcessor *BatchImageProviderProcessor
	SettlementService *BatchImageSettlementService
	RetryDelay        time.Duration
}

func (p *BatchImagePipelineProcessor) Process(ctx context.Context, batchID string) (BatchImageProcessResult, error) {
	if p == nil || p.ProviderProcessor == nil {
		return BatchImageProcessResult{}, errors.New("batch image pipeline processor is not configured")
	}
	job, err := p.ProviderProcessor.Repo.GetBatchImageJobByBatchID(ctx, batchID)
	if err != nil {
		return BatchImageProcessResult{}, err
	}
	if job.Status == BatchImageJobStatusSettling {
		if p.SettlementService == nil {
			return BatchImageProcessResult{Terminal: true}, nil
		}
		_, err := p.SettlementService.Settle(ctx, batchID)
		if err != nil {
			if errors.Is(err, ErrBatchImageSettlementBillingFailed) {
				updated, getErr := p.ProviderProcessor.Repo.GetBatchImageJobByBatchID(ctx, batchID)
				if getErr == nil && IsTerminalBatchImageJobStatus(updated.Status) {
					return BatchImageProcessResult{Terminal: true}, nil
				}
				delay := p.RetryDelay
				if delay <= 0 {
					delay = batchImageSettlementRetryDelay
				}
				return BatchImageProcessResult{RequeueAfter: delay}, nil
			}
			return BatchImageProcessResult{}, err
		}
		return BatchImageProcessResult{Terminal: true}, nil
	}
	return p.ProviderProcessor.Process(ctx, batchID)
}

func (r *BatchImageSettlementResult) String() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("batch_id=%s success=%d fail=%d actual_cost=%0.10f already_settled=%t",
		r.BatchID, r.SuccessCount, r.FailCount, r.ActualCost, r.AlreadySettled)
}
