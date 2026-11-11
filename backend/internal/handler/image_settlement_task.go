package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
	"time"
)

// Save financial facts before returning, ahead of any droppable worker/detail queue.
func runImageSettlementTask(parent context.Context, task service.UsageRecordTask) (handled bool) {
	if service.UsageSettlementFromContext(parent) == "" {
		return false
	}
	handled = true // A recovered panic must never resubmit the financial task.
	if task == nil {
		return true
	}
	if !service.BeginImageBillingSettlement(parent) {
		return true
	}
	wrapped, _ := wrapUsageRecordTaskContext(parent, task)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer func() {
		if v := recover(); v != nil {
			logger.L().Error("image.settlement_task_panic", zap.String("request_id", service.UsageSettlementFromContext(parent)), zap.Any("panic", v), zap.Bool(logger.OpsSystemLogSkipField, true))
		}
	}()
	wrapped(ctx)
	return true
}
