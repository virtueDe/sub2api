package securityaudit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/stretchr/testify/require"
)

type lazyLegacyCapture struct{ bodies chan []byte }

func (e *lazyLegacyCapture) Check(_ context.Context, req Request) (*LegacyDecision, error) {
	e.bodies <- req.BuildBody(true)
	return &LegacyDecision{Allowed: true}, nil
}

type waitingPromptRepository struct {
	fakeJobRepository
	entered chan struct{}
	resume  chan struct{}
}

func (r *waitingPromptRepository) CreateStagingWithCapacity(ctx context.Context, snapshot PromptSnapshot, version int64, attempts, capacity int) (*Job, error) {
	close(r.entered)
	select {
	case <-r.resume:
		return r.fakeJobRepository.CreateStagingWithCapacity(ctx, snapshot, version, attempts, capacity)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestPromptServiceLazyAsyncRetainsBudgetUntilEnqueued(t *testing.T) {
	cfg := &fakeConfigStore{active: true, cfg: ActiveConfig{
		Enabled: true, RiskControlEnabled: true, AllGroups: true,
		Endpoints: []ActiveEndpoint{{ID: "guard", Enabled: true}},
	}}
	repo := &waitingPromptRepository{entered: make(chan struct{}), resume: make(chan struct{})}
	prompt := &PromptService{config: cfg, enqueuer: NewEnqueuer(cfg, repo, &fakePayloadStore{}),
		background: context.Background(), enqueueSlots: make(chan struct{}, 1)}
	budget := httputil.NewBodyMemoryBudget(1 << 20)
	ctx, release := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
	defer release()
	var calls atomic.Int64
	err := prompt.Enqueue(ctx, Request{Protocol: "openai_images", BuildBody: func(images bool) []byte {
		calls.Add(1)
		require.False(t, images)
		return []byte(`{"prompt":"original prompt"}`)
	}})
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load(), "builder must be consumed before Enqueue returns")
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("enqueue did not start")
	}
	release()
	probe, closeProbe := httputil.WithRequestBodyMemoryBudget(context.Background(), budget)
	defer closeProbe()
	require.ErrorIs(t, httputil.ReserveRequestBodyMemory(probe, 1<<20), httputil.ErrRequestBodyMemoryBudget)
	close(repo.resume)
	prompt.enqueueWG.Wait()
	require.Equal(t, "original prompt", repo.createdSnapshot.FullPrompt)
	require.NoError(t, httputil.ReserveRequestBodyMemory(probe, 1<<20))
}

func TestCoordinatorLazyBodySkipsExcludedPromptGroup(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		cfg := &fakeConfigStore{active: true, cfg: ActiveConfig{
			Enabled: true, RiskControlEnabled: true, BlockingEnabled: blocking, GroupIDs: []int64{7},
		}}
		t.Run(string(cfg.EffectiveMode()), func(t *testing.T) {
			prompt := NewPromptService(cfg, nil, nil, nil, nil)
			var builds atomic.Int64
			groupID := int64(8)
			decision := NewCoordinator(nil, prompt).Check(context.Background(), Request{
				GroupID: &groupID, BuildBody: func(bool) []byte {
					builds.Add(1)
					return []byte(`{"prompt":"excluded"}`)
				},
			})
			require.True(t, decision.AllowNextStage)
			require.Zero(t, builds.Load(), "out-of-scope requests must not build or reserve audit bodies")
		})
	}
}

type lazyPromptCapture struct {
	mode     Mode
	requests chan Request
}

func (e *lazyPromptCapture) EffectiveMode() Mode { return e.mode }
func (e *lazyPromptCapture) Enqueue(_ context.Context, r Request) error {
	e.requests <- r.promptRequest()
	return nil
}
func (e *lazyPromptCapture) Evaluate(_ context.Context, r Request) (*PromptDecision, error) {
	e.requests <- r.promptRequest()
	return &PromptDecision{Kind: DecisionAllow, AllowNextStage: true}, nil
}

func TestCoordinatorLazyBodyKeepsImagesOutOfPromptAudit(t *testing.T) {
	for _, mode := range []Mode{ModeAsync, ModeBlocking} {
		t.Run(string(mode), func(t *testing.T) {
			full := []byte(`{"prompt":"original prompt","images":[{"image_url":"data:image/png;base64,YWJj"}]}`)
			text := []byte(`{"prompt":"original prompt"}`)
			legacy := &lazyLegacyCapture{bodies: make(chan []byte, 1)}
			prompt := &lazyPromptCapture{mode: mode, requests: make(chan Request, 1)}
			request := Request{Protocol: "openai_images", BuildBody: func(images bool) []byte {
				if images {
					return full
				}
				return text
			}}
			decision := NewCoordinator(legacy, prompt).Check(context.Background(), request)
			require.True(t, decision.AllowNextStage)
			require.Equal(t, full, <-legacy.bodies)
			got := <-prompt.requests
			require.Nil(t, got.BuildBody)
			require.Equal(t, text, got.Body)
			old, err := ExtractPromptSnapshot(Request{Protocol: request.Protocol, Body: full})
			require.NoError(t, err)
			current, err := ExtractPromptSnapshot(got)
			require.NoError(t, err)
			require.Equal(t, old.PromptHash, current.PromptHash)
		})
	}
}
