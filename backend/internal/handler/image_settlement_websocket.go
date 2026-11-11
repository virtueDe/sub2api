package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"sync"
)

type imageSettlementTurn struct {
	ctx  context.Context
	done func()
}
type imageSettlementTurns struct {
	mu           sync.Mutex
	turns        map[int]imageSettlementTurn
	base, latest int
}

// An upstream retry retains the same identity; each response.create gets its own.
func (s *imageSettlementTurns) reserve(ctx context.Context, reserver imageBillingReserver, key *service.APIKey, sub *service.UserSubscription, turn int, model, platform string, body []byte, mappedModels ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn += s.base
	if turn > s.latest {
		s.latest = turn
	}
	if _, ok := s.turns[turn]; ok {
		return nil
	}
	imageIntent := service.IsImageGenerationIntentForPlatform("/v1/responses", model, body, platform) || service.IsGeminiImageGenerationIntent(body, model)
	for _, mapped := range mappedModels {
		imageIntent = imageIntent || service.IsImageGenerationIntentForPlatform("/v1/responses", mapped, body, platform) || service.IsGeminiImageGenerationIntent(body, mapped)
	}
	if !imageIntent {
		return nil
	}
	req := tokenInflightEstimate(model, body)
	req.Kind = service.InflightEstimateImage
	reserved, done, err := reserver.ReserveImageBilling(ctx, key, sub, req)
	if err != nil {
		return err
	}
	if service.UsageSettlementFromContext(reserved) == "" {
		return nil
	}
	if err := service.BeginImageBillingUnknownTransport(reserved); err != nil {
		done()
		return err
	}
	if s.turns == nil {
		s.turns = make(map[int]imageSettlementTurn)
	}
	// Finished turns release confirmed failures; unknown outcomes retain the hold.
	for previous, v := range s.turns {
		if previous < turn-1 {
			v.done()
			delete(s.turns, previous)
		}
	}
	s.turns[turn] = imageSettlementTurn{reserved, done}
	return nil
}
func (s *imageSettlementTurns) context(ctx context.Context, turn int) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.turns[s.base+turn]; ok {
		return v.ctx
	}
	return ctx
}

// A new upstream session numbers its replayed current frame as turn 1.
// Keep that frame's financial identity, then advance later frames normally.
func (s *imageSettlementTurns) retryCurrentTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest > 0 {
		s.base = s.latest - 1
	}
}
func (s *imageSettlementTurns) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.turns {
		v.done()
	}
	s.turns = nil
}
