package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAsyncImageExecutionSurvivesUsageQueue(t *testing.T) {
	ctx := service.WithAsyncImageTaskExecution(context.Background())
	for _, submit := range []func(context.Context, service.UsageRecordTask){
		(&GatewayHandler{}).submitUsageRecordTask,
		(&OpenAIGatewayHandler{}).submitUsageRecordTask,
	} {
		called := false
		submit(ctx, func(billingCtx context.Context) {
			called = true
			require.True(t, service.IsAsyncImageTaskExecution(billingCtx))
		})
		require.True(t, called)
	}
	task, abandon := wrapUsageRecordTaskContext(ctx, func(billingCtx context.Context) {
		require.True(t, service.IsAsyncImageTaskExecution(billingCtx))
	})
	defer abandon()
	task(context.Background())
}

func TestSubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "client-request-123", gotClientRequestID)
	require.Equal(t, "request-456", gotRequestID)
}

func TestOpenAISubmitUsageRecordTaskCopiesRequestContext(t *testing.T) {
	parent := context.WithValue(context.Background(), ctxkey.ClientRequestID, "openai-client-request-123")
	parent = context.WithValue(parent, ctxkey.RequestID, "openai-request-456")

	var gotClientRequestID string
	var gotRequestID string
	h := &OpenAIGatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		gotClientRequestID, _ = ctx.Value(ctxkey.ClientRequestID).(string)
		gotRequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	})

	require.Equal(t, "openai-client-request-123", gotClientRequestID)
	require.Equal(t, "openai-request-456", gotRequestID)
}
