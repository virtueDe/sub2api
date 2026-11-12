//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type streamImageSettlementRepo struct {
	imageReservationFake
	UsageSettlementRepository
	prepared           *UsageSettlementSnapshot
	applied, completed int
}

func (r *streamImageSettlementRepo) PrepareSettlement(_ context.Context, s *UsageSettlementSnapshot) error {
	r.prepared = s
	return nil
}

func (r *streamImageSettlementRepo) ApplySettlement(context.Context, UsageSettlementRef, *ImageGenerationDetail) (*UsageSettlementSnapshot, *UsageBillingApplyResult, error) {
	r.applied++
	return r.prepared, &UsageBillingApplyResult{Applied: true}, nil
}

func (r *streamImageSettlementRepo) CompleteSettlement(context.Context, UsageSettlementRef) error {
	r.completed++
	return nil
}

func TestImagesStreamReservationSettlesObservedOutput(t *testing.T) {
	complete := "event: image_generation.completed\ndata: {\"b64_json\":\"AA==\",\"usage\":{\"input_tokens\":10,\"output_tokens\":18,\"output_tokens_details\":{\"image_tokens\":8}}}\n\n"
	failure := "event: error\ndata: {\"error\":{\"code\":\"server_error\",\"message\":\"upstream failed\"}}\n\n"
	for _, mode := range []string{"image", "token"} {
		for _, tc := range []struct {
			name, wire                    string
			hasImage, wantErr, disconnect bool
		}{
			{name: "completed", wire: complete, hasImage: true},
			{name: "completed_then_error", wire: complete + failure, hasImage: true, wantErr: true},
			{name: "client_disconnect", wire: complete, hasImage: true, disconnect: true},
			{name: "empty", wantErr: true},
			{name: "upstream_error", wire: failure, wantErr: true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				repo := &streamImageSettlementRepo{}
				userRepo := &openAIRecordUsageUserRepoStub{}
				svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(&openAIRecordUsageLogRepoStub{}, repo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
				svc.cfg.Database.ImageBillingReservationEnabled = true
				svc.cfg.Pricing.DataDir = t.TempDir()
				groupID := int64(14)
				svc.resolver = newOpenAIImageChannelPricingResolverForTest(t, groupID, "gpt-image-2", .25)
				wantCost := .25
				if mode == "token" {
					svc.resolver = newOpenAITokenImageChannelPricingResolverForTest(t, groupID, "gpt-image-2")
					wantCost = 10*3e-6 + 18*15e-6
				}
				key := &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, RateMultiplier: 1}}
				ctx, done, err := reserveImageBilling(context.Background(), svc.cfg, repo, svc.billingCacheService,
					func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true }, key, nil, InflightEstimateRequest{})
				require.NoError(t, err)
				defer done()
				body := []byte(`{"model":"gpt-image-2","prompt":"test","stream":true,"response_format":"b64_json"}`)
				c, _ := newOpenAIImagesTestContext(t, body)
				c.Request = c.Request.WithContext(ctx)
				if tc.disconnect {
					c.Writer = &imageLimitDisconnectedWriter{ResponseWriter: c.Writer}
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(tc.wire))}
				svc.httpUpstream = &httpUpstreamRecorder{resp: resp}
				require.NoError(t, BeginImageBillingDispatch(ctx))
				ObserveImageBillingAttempt(ctx)(resp, nil)
				parsed, err := svc.ParseOpenAIImagesRequest(c, body)
				require.NoError(t, err)
				account := newOpenAIImagesAPIKeyAccount()
				result, forwardErr := svc.ForwardImages(ctx, c, account, body, parsed, "")
				if tc.wantErr {
					require.Error(t, forwardErr)
				} else {
					require.NoError(t, forwardErr)
				}
				ObserveImageBillingResult(ctx, result != nil && result.ImageCount > 0, tc.disconnect, forwardErr)
				require.Equal(t, tc.hasImage, BeginImageBillingSettlement(ctx))
				if tc.hasImage {
					require.NotNil(t, result)
					require.Equal(t, 1, result.ImageCount)
					require.NoError(t, svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: key, User: key.User, Account: account}))
					require.NotNil(t, repo.prepared)
					require.InDelta(t, wantCost, repo.prepared.Command.BalanceCost, 1e-8)
					require.Equal(t, repo.prepared.Command.BalanceCost, repo.prepared.Usage.ActualCost)
					require.Equal(t, 1, repo.applied)
					require.Equal(t, 1, repo.completed)
				} else {
					require.Nil(t, result)
					require.Nil(t, repo.prepared)
					require.Zero(t, repo.applied)
				}
				done()
				done()
				require.Equal(t, 1, repo.reserved)
				require.Equal(t, 1, repo.dispatched)
				require.Zero(t, userRepo.deductCalls)
				if tc.hasImage {
					require.Zero(t, repo.released)
				} else {
					require.Equal(t, 1, repo.released)
				}
			})
		}
	}
}
