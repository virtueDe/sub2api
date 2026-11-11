//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImageReservationGeminiStreamOutcome(t *testing.T) {
	for _, disconnected := range []bool{false, true} {
		for _, withImage := range []bool{false, true} {
			name := "upstream_error"
			if disconnected {
				name = "client_disconnect"
			}
			if withImage {
				name += "_after_image"
			}
			t.Run(name, func(t *testing.T) {
				body := []byte(`{"contents":[{"role":"user","parts":[{"text":"draw a cat"}]}]}`)
				chunk := geminiAbortUsageChunk
				if withImage {
					chunk = geminiAbortImageChunk
				}
				svc, c, _ := newGeminiAbortingStreamService(t, "/v1beta/models/gemini-3-pro-image:streamGenerateContent", body, chunk, disconnected)
				r := &imageReservationFake{}
				ctx, done, err := reserveImageBilling(c.Request.Context(), &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true }, &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}, nil, InflightEstimateRequest{})
				require.NoError(t, err)
				c.Request = c.Request.WithContext(ctx)
				require.NoError(t, BeginImageBillingDispatch(ctx))
				ObserveImageBillingAttempt(ctx)(&http.Response{StatusCode: 200}, nil)
				result, forwardErr := svc.ForwardNative(ctx, c, geminiVertexAPIKeyTestAccount(""), "gemini-3-pro-image", "streamGenerateContent", true, body)
				ObserveImageBillingResult(ctx, result != nil && result.ImageCount > 0, result != nil && result.ClientDisconnect, forwardErr)
				if result != nil {
					require.Equal(t, withImage, BeginImageBillingSettlement(ctx))
				}
				done()
				if withImage {
					require.Zero(t, r.released)
				} else {
					require.Equal(t, 1, r.released)
				}
			})
		}
	}
}

func TestImageReservationAntigravityPreservesObservedImage(t *testing.T) {
	r := &imageReservationFake{}
	ctx, done, err := reserveImageBilling(context.Background(), &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true }, &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}, nil, InflightEstimateRequest{})
	require.NoError(t, err)
	c := newGeminiImageTestContext(t)
	c.Request = c.Request.WithContext(ctx)
	beginGeminiImageOutputObservation(c)
	svc := &AntigravityGatewayService{}
	// The upstream can return a wrapper around a completed image before its
	// stream fails. A missing final ForwardResult must not discard that fact.
	payload := bytes.TrimSpace(bytes.TrimPrefix([]byte(geminiAbortImageChunk), []byte("data: ")))
	svc.observeAntigravityGeminiSSELine(c, `data: {"response":`+string(payload)+`}`)
	ObserveImageBillingResult(ctx, false, false, context.DeadlineExceeded)
	done()
	require.Zero(t, r.released)
}

type imageReservationFake struct {
	UsageBillingRepository
	reserved, released, dispatched      int
	amount                              float64
	sub                                 *int64
	reserveErr, dispatchErr, releaseErr error
}

func (r *imageReservationFake) ReserveImageSettlement(_ context.Context, _ UsageSettlementRef, _ int64, sub *int64, a float64) error {
	r.reserved++
	r.amount = a
	r.sub = sub
	return r.reserveErr
}
func (r *imageReservationFake) ReleaseImageSettlement(ctx context.Context, _ UsageSettlementRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.released++
	return r.releaseErr
}

func TestImageReservationFinalOutputPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		statuses                                []int
		images                                  []bool
		errors                                  []error
		settle, cancel, websocket, disconnected bool
		wantRelease                             int
	}{
		{name: "200_stream_error_without_image", statuses: []int{200}, images: []bool{false}, errors: []error{errors.New("stream failed")}, settle: true, wantRelease: 1},
		{name: "client_disconnect_with_tokens_but_no_image", statuses: []int{200}, images: []bool{false}, errors: []error{nil}, disconnected: true, settle: true, wantRelease: 1},
		{name: "client_disconnect_with_image", statuses: []int{200}, images: []bool{true}, errors: []error{nil}, disconnected: true, settle: true},
		{name: "200_client_cancel_without_image", statuses: []int{200}, images: []bool{false}, errors: []error{context.Canceled}, cancel: true, settle: true, wantRelease: 1},
		{name: "partial_image_then_disconnect", statuses: []int{200}, images: []bool{true}, errors: []error{context.Canceled}, settle: true},
		{name: "success_keeps_hold_even_before_billing", statuses: []int{200}, images: []bool{true}, errors: []error{nil}},
		{name: "500_without_image", statuses: []int{500}, images: []bool{false}, errors: []error{errors.New("upstream failed")}, wantRelease: 1},
		{name: "timeout_then_success", statuses: []int{524, 200}, images: []bool{false, true}, errors: []error{context.DeadlineExceeded, nil}, settle: true},
		{name: "200_error_then_451", statuses: []int{200, 451}, images: []bool{false, false}, errors: []error{errors.New("empty image"), errors.New("refused")}, wantRelease: 1},
		{name: "earlier_image_wins", statuses: []int{200, 502}, images: []bool{true, false}, errors: []error{errors.New("partial"), errors.New("gateway")}, settle: true},
		{name: "websocket_failed_without_image", images: []bool{false}, errors: []error{context.Canceled}, websocket: true, wantRelease: 1},
		{name: "websocket_partial_image", images: []bool{true}, errors: []error{context.Canceled}, websocket: true, settle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &imageReservationFake{}
			cfg := &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, done, err := reserveImageBilling(parent, cfg, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true }, &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}, nil, InflightEstimateRequest{})
			require.NoError(t, err)
			for i, hasImage := range tc.images {
				if tc.websocket {
					require.NoError(t, BeginImageBillingUnknownTransport(ctx))
				} else {
					require.NoError(t, BeginImageBillingDispatch(ctx))
					ObserveImageBillingAttempt(ctx)(&http.Response{StatusCode: tc.statuses[i]}, nil)
				}
				ObserveImageBillingResult(ctx, hasImage, tc.disconnected, tc.errors[i])
			}
			if tc.cancel {
				cancel()
			}
			if tc.settle {
				require.Equal(t, tc.wantRelease == 0, BeginImageBillingSettlement(ctx))
			}
			done()
			done()
			require.Equal(t, tc.wantRelease, r.released)
		})
	}
	// Text requests are unaffected by the image-only refund policy.
	ObserveImageBillingResult(context.Background(), false, false, context.Canceled)
	require.True(t, BeginImageBillingSettlement(context.Background()))
}

func TestImageReservationPendingAttemptAndBillingFailureKeepHold(t *testing.T) {
	for _, billing := range []bool{false, true} {
		r := &imageReservationFake{}
		ctx, done, err := reserveImageBilling(context.Background(), &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true }, &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}, nil, InflightEstimateRequest{})
		require.NoError(t, err)
		if billing {
			// Pricing, DB writes or a billing panic must never turn a success into a refund.
			require.True(t, BeginImageBillingSettlement(ctx))
		} else {
			require.NoError(t, BeginImageBillingDispatch(ctx))
			_ = ObserveImageBillingAttempt(ctx)
		}
		done()
		require.Zero(t, r.released)
	}
}

type imageReleaseRecoveryFake struct {
	UsageSettlementRepository
	imageReservationFake
}

func TestImageReservationReleaseJournalAfterDatabaseOutage(t *testing.T) {
	r := &imageReservationFake{releaseErr: context.DeadlineExceeded}
	cfg := &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}
	cfg.Pricing.DataDir = t.TempDir()
	ctx, done, err := reserveImageBilling(context.Background(), cfg, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true }, &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}, nil, InflightEstimateRequest{})
	require.NoError(t, err)
	require.NoError(t, BeginImageBillingDispatch(ctx))
	ObserveImageBillingAttempt(ctx)(&http.Response{StatusCode: 451}, nil)
	done()
	ref := UsageSettlementRef{RequestID: UsageSettlementFromContext(ctx), APIKeyID: 1}
	name, _ := imageReleaseJournalName(ref)
	path := filepath.Join(imageJournalDirectory(cfg), name)
	_, err = os.Stat(path)
	require.NoError(t, err)
	recovered := &imageReleaseRecoveryFake{imageReservationFake: imageReservationFake{releaseErr: context.DeadlineExceeded}}
	cursor := ""
	require.ErrorIs(t, replayImageSettlementFacts(context.Background(), cfg, recovered, &cursor), context.DeadlineExceeded)
	_, err = os.Stat(path)
	require.NoError(t, err)
	recovered.releaseErr = nil
	require.NoError(t, replayImageSettlementFacts(context.Background(), cfg, recovered, &cursor))
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
	require.Equal(t, 2, recovered.released)
}
func (r *imageReservationFake) MarkImageSettlementDispatched(context.Context, UsageSettlementRef) error {
	r.dispatched++
	return r.dispatchErr
}

func TestImageReservationOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		statuses    []int
		dispatchErr bool
		release     int
	}{
		{"before_upstream", nil, false, 1}, {"rejected", []int{429, 400}, false, 1},
		{"policy_refusal", []int{451}, false, 1},
		{"all_attempts_rejected", []int{429, 451}, false, 1},
		{"accepted_then_policy_refusal", []int{200, 451}, false, 0},
		{"timeout_then_policy_refusal", []int{0, 451}, false, 1},
		{"upstream_timeout_then_policy_refusal", []int{524, 451}, false, 1},
		{"transport_timeout", []int{0}, false, 1},
		{"upstream_failures", []int{499, 502, 524}, false, 1},
		{"accepted", []int{200}, false, 0}, {"server_error", []int{500}, false, 0},
		{"ambiguous_then_rejected", []int{500, 429}, false, 0},
		{"accepted_then_upstream_failure", []int{200, 502}, false, 0},
		{"dispatch_commit_unknown", []int{200}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &imageReservationFake{}
			if tc.dispatchErr {
				r.dispatchErr = context.DeadlineExceeded
			}
			cfg := &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}
			key := &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}
			ctx, done, err := reserveImageBilling(context.Background(), cfg, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1.25, true }, key, nil, InflightEstimateRequest{})
			require.NoError(t, err)
			require.NotEmpty(t, UsageSettlementFromContext(ctx))
			for _, status := range tc.statuses {
				err = BeginImageBillingDispatch(ctx)
				if tc.dispatchErr {
					require.Error(t, err)
					break
				}
				require.NoError(t, err)
				observe := ObserveImageBillingAttempt(ctx)
				if status == 0 {
					observe(nil, context.DeadlineExceeded)
				} else {
					observe(&http.Response{StatusCode: status}, nil)
				}
			}
			done()
			done()
			require.Equal(t, tc.release, r.released)
			if len(tc.statuses) > 0 {
				require.Equal(t, 1, r.dispatched)
			}
		})
	}
}
func TestImageReservationAdmission(t *testing.T) {
	cfg := &config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}}
	key := &APIKey{ID: 1, UserID: 2, User: &User{ID: 2}}
	for _, amount := range []float64{-1, math.NaN(), math.Inf(1)} {
		r := &imageReservationFake{}
		_, _, err := reserveImageBilling(context.Background(), cfg, r, nil, func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return amount, true }, key, nil, InflightEstimateRequest{})
		require.Error(t, err)
		require.Zero(t, r.reserved)
	}
	r := &imageReservationFake{}
	price := func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 0, true }
	ctx, done, err := reserveImageBilling(context.Background(), cfg, r, nil, price, key, nil, InflightEstimateRequest{})
	require.NoError(t, err)
	require.Zero(t, r.amount)
	reused, _, err := reserveImageBilling(ctx, cfg, r, nil, price, key, nil, InflightEstimateRequest{})
	require.NoError(t, err)
	require.Equal(t, UsageSettlementFromContext(ctx), UsageSettlementFromContext(reused))
	require.Equal(t, 1, r.reserved)
	done()
	key.Group = &Group{SubscriptionType: SubscriptionTypeSubscription}
	_, _, err = reserveImageBilling(context.Background(), cfg, r, nil, price, key, nil, InflightEstimateRequest{})
	require.ErrorIs(t, err, ErrSubscriptionNotFound)
	_, done, err = reserveImageBilling(context.Background(), cfg, r, nil, price, key, &UserSubscription{ID: 9}, InflightEstimateRequest{})
	require.NoError(t, err)
	require.Equal(t, int64(9), *r.sub)
	done()
	r.reserveErr = context.DeadlineExceeded
	_, _, err = reserveImageBilling(context.Background(), cfg, r, nil, price, key, &UserSubscription{ID: 9}, InflightEstimateRequest{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type imageJournalFake struct {
	UsageSettlementRepository
	err   error
	saved *UsageSettlementSnapshot
}

func (r *imageJournalFake) PrepareSettlement(_ context.Context, s *UsageSettlementSnapshot) error {
	r.saved = s
	return r.err
}
func TestImageSettlementJournalRecovery(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pricing.DataDir = t.TempDir()
	cmd := &UsageBillingCommand{RequestID: "settlement:journal", APIKeyID: 1, UserID: 2, BalanceCost: 1.25}
	cmd.Normalize()
	s := newUsageSettlementSnapshot(cmd, &UsageLog{RequestID: cmd.RequestID, APIKeyID: 1, UserID: 2, CreatedAt: time.Now(), APIKey: &APIKey{Key: "secret-must-not-persist"}, ImageGeneration: &ImageGenerationDetail{}}, &postUsageBillingParams{Cost: &CostBreakdown{ActualCost: 1.25}})
	path, err := saveImageSettlementFacts(cfg, s)
	require.NoError(t, err)
	again, err := saveImageSettlementFacts(cfg, s)
	require.NoError(t, err)
	require.Equal(t, path, again)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "secret-must-not-persist")
	altered := *s
	altered.Command.BalanceCost = 9
	_, err = saveImageSettlementFacts(cfg, &altered)
	require.ErrorIs(t, err, ErrUsageSettlementConflict)
	r := &imageJournalFake{err: context.DeadlineExceeded}
	cursor := ""
	require.Error(t, replayImageSettlementFacts(context.Background(), cfg, r, &cursor))
	_, err = os.Stat(path)
	require.NoError(t, err)
	r.err = nil
	require.NoError(t, replayImageSettlementFacts(context.Background(), cfg, r, &cursor))
	require.Equal(t, cmd.RequestFingerprint, r.saved.Command.RequestFingerprint)
	_, err = os.Stat(path)
	require.True(t, errors.Is(err, os.ErrNotExist))
}
