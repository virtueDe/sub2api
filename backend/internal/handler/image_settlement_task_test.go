package handler

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

type imageAdmissionFake struct {
	countingEstimator
	calls int
}

func (f *imageAdmissionFake) ReserveImageBilling(ctx context.Context, _ *service.APIKey, _ *service.UserSubscription, _ service.InflightEstimateRequest) (context.Context, func(), error) {
	f.calls++
	return service.WithUsageSettlement(ctx, fmt.Sprintf("settlement:%d", f.calls)), func() {}, nil
}
func TestImageReservationEntrypointClassification(t *testing.T) {
	for _, tc := range []struct {
		path, model, body string
		want              int
	}{
		{"/v1/images/generations", "gpt-image-1", `{"model":"gpt-image-1"}`, 1},
		{"/v1/responses", "gpt-4.1", `{"model":"gpt-4.1","tools":[{"type":"image_generation"}]}`, 1},
		{"/v1/chat/completions", "gemini-2.5-flash-image", `{"model":"gemini-2.5-flash-image"}`, 1},
		{"/v1/chat/completions", "gpt-4.1", `{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image"}}]}]}`, 0},
		{"/v1beta/models/gemini-2.5-flash-image:countTokens", "gemini-2.5-flash-image", `{"model":"gemini-2.5-flash-image"}`, 0},
	} {
		t.Run(tc.path+tc.model, func(t *testing.T) {
			c := newInflightTestGinContext()
			c.Request = httptest.NewRequest("POST", tc.path, nil)
			defer beginImageGenerationCapture(c, []byte(tc.body))()
			f := &imageAdmissionFake{}
			done, err := reserveInflightBalance(c, nil, f, &service.APIKey{}, nil, tokenInflightEstimate(tc.model, []byte(tc.body)))
			require.NoError(t, err)
			done()
			require.Equal(t, tc.want, f.calls)
		})
	}
}
func TestImageFinancialTaskRunsBeforeDetailsQueue(t *testing.T) {
	parent, cancel := context.WithCancel(service.WithUsageSettlement(context.Background(), "settlement:sync"))
	cancel()
	q := &imageGenerationUsageQueue{}
	parent = context.WithValue(parent, imageGenerationUsageQueueKey{}, q)
	count := 0
	h := &GatewayHandler{}
	h.submitUsageRecordTask(parent, func(ctx context.Context) {
		require.NoError(t, ctx.Err())
		require.Equal(t, "settlement:sync", service.UsageSettlementFromContext(ctx))
		count++
	})
	require.Equal(t, 1, count)
	require.Empty(t, q.tasks)
	require.True(t, runImageSettlementTask(parent, func(context.Context) { count++; panic("test") }))
	require.Equal(t, 2, count)
}
func TestImageWebSocketTurnIdentity(t *testing.T) {
	var turns imageSettlementTurns
	f := &imageAdmissionFake{}
	ctx := context.Background()
	body := []byte(`{"model":"gpt-4.1","tools":[{"type":"image_generation"}]}`)
	require.NoError(t, turns.reserve(ctx, f, &service.APIKey{}, nil, 1, "gpt-4.1", "openai", body))
	first := service.UsageSettlementFromContext(turns.context(ctx, 1))
	require.NoError(t, turns.reserve(ctx, f, &service.APIKey{}, nil, 1, "gpt-4.1", "openai", body))
	require.Equal(t, 1, f.calls)
	require.NoError(t, turns.reserve(ctx, f, &service.APIKey{}, nil, 2, "gpt-4.1", "openai", body))
	require.NotEqual(t, first, service.UsageSettlementFromContext(turns.context(ctx, 2)))
	second := service.UsageSettlementFromContext(turns.context(ctx, 2))
	turns.retryCurrentTurn()
	require.Equal(t, second, service.UsageSettlementFromContext(turns.context(ctx, 1)))
	require.NoError(t, turns.reserve(ctx, f, &service.APIKey{}, nil, 1, "gpt-4.1", "openai", body))
	require.Equal(t, 2, f.calls, "account failover must reuse the current hold")
	require.NoError(t, turns.reserve(ctx, f, &service.APIKey{}, nil, 2, "gpt-4.1", "openai", body))
	require.Equal(t, 3, f.calls)
	require.NotEqual(t, second, service.UsageSettlementFromContext(turns.context(ctx, 2)))
	turns.close()
}
