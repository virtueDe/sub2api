package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type blockingAggregationRepository struct {
	DashboardAggregationRepository
	started, finished chan struct{}
}

func (r *blockingAggregationRepository) RecomputeRange(ctx context.Context, _, _ time.Time) error {
	close(r.started)
	<-ctx.Done()
	close(r.finished)
	return ctx.Err()
}

func TestDashboardAggregationStopCancelsAndDrainsJobs(t *testing.T) {
	repo := &blockingAggregationRepository{started: make(chan struct{}), finished: make(chan struct{})}
	s := NewDashboardAggregationService(repo, nil, &config.Config{DashboardAgg: config.DashboardAggregationConfig{Enabled: true}})
	require.NoError(t, s.TriggerRecomputeRange(time.Now().Add(-time.Hour), time.Now()))
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel job")
	}
	select {
	case <-repo.finished:
	default:
		t.Fatal("shutdown returned before job exited")
	}
	require.ErrorContains(t, s.TriggerRecomputeRange(time.Now().Add(-time.Hour), time.Now()), "stopped")
	s.Stop()
}
