package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type videoBillingReservationFake struct {
	UsageBillingRepository
	released, dispatched int
}

func (*videoBillingReservationFake) ReserveImageSettlement(context.Context, UsageSettlementRef, int64, *int64, float64) error {
	return nil
}

func (r *videoBillingReservationFake) ReleaseImageSettlement(context.Context, UsageSettlementRef) error {
	r.released++
	return nil
}

func (r *videoBillingReservationFake) MarkImageSettlementDispatched(context.Context, UsageSettlementRef) error {
	r.dispatched++
	return nil
}

func TestVideoBillingReservationLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		statuses     []int
		transportErr bool
		accepted     bool
		wantRelease  int
	}{
		{name: "not dispatched", wantRelease: 1},
		{name: "accepted async task", statuses: []int{http.StatusOK}, accepted: true},
		{name: "definitely rejected", statuses: []int{http.StatusUnprocessableEntity}, wantRelease: 1},
		{name: "uncertain transport result", transportErr: true},
		{name: "uncertain attempt then rejection", statuses: []int{http.StatusBadGateway, http.StatusUnprocessableEntity}},
		{name: "unconfirmed submission", statuses: []int{http.StatusConflict}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &videoBillingReservationFake{}
			ctx, done, err := reserveVideoBilling(
				context.Background(),
				&config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}},
				repo,
				nil,
				func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1.25, true },
				&APIKey{ID: 1, UserID: 2, User: &User{ID: 2}},
				nil,
				InflightEstimateRequest{Kind: InflightEstimateVideo},
			)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(UsageSettlementFromContext(ctx), "settlement:video:"))

			for _, status := range tc.statuses {
				require.NoError(t, BeginVideoBillingDispatch(ctx))
				observe := ObserveVideoBillingAttempt(ctx)
				observe(&http.Response{StatusCode: status}, nil)
			}
			if tc.transportErr {
				require.NoError(t, BeginVideoBillingDispatch(ctx))
				ObserveVideoBillingAttempt(ctx)(nil, errors.New("connection reset"))
			}
			if tc.accepted {
				CommitVideoBillingReservation(ctx)
			}

			done()
			done()
			require.Equal(t, tc.wantRelease, repo.released)
			if len(tc.statuses) > 0 || tc.transportErr {
				require.Equal(t, 1, repo.dispatched)
			}
		})
	}
}

func TestVideoBillingReservationRejectDoesNotClearUncertainAttempt(t *testing.T) {
	repo := &videoBillingReservationFake{}
	ctx, done, err := reserveVideoBilling(
		context.Background(),
		&config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}},
		repo,
		nil,
		func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true },
		&APIKey{ID: 1, UserID: 2, User: &User{ID: 2}},
		nil,
		InflightEstimateRequest{Kind: InflightEstimateVideo},
	)
	require.NoError(t, err)
	require.NoError(t, BeginVideoBillingDispatch(ctx))
	ObserveVideoBillingAttempt(ctx)(nil, context.DeadlineExceeded)
	require.NoError(t, BeginVideoBillingDispatch(ctx))
	ObserveVideoBillingAttempt(ctx)(&http.Response{StatusCode: http.StatusOK}, nil)
	RejectVideoBillingReservation(ctx)
	done()
	require.Zero(t, repo.released)
}

func TestVideoBillingReservationCanReleaseRecognizedProviderRejection(t *testing.T) {
	repo := &videoBillingReservationFake{}
	ctx, done, err := reserveVideoBilling(
		context.Background(),
		&config.Config{Database: config.DatabaseConfig{ImageBillingReservationEnabled: true}},
		repo,
		nil,
		func(context.Context, *APIKey, InflightEstimateRequest) (float64, bool) { return 1, true },
		&APIKey{ID: 1, UserID: 2, User: &User{ID: 2}},
		nil,
		InflightEstimateRequest{Kind: InflightEstimateVideo},
	)
	require.NoError(t, err)
	require.NoError(t, BeginVideoBillingDispatch(ctx))
	ObserveVideoBillingAttempt(ctx)(&http.Response{StatusCode: http.StatusOK}, nil)
	RejectVideoBillingReservation(ctx)
	done()
	require.Equal(t, 1, repo.released)
}
