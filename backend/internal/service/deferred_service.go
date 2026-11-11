package service

import (
	"context"
	"log"
	"sync"
	"time"
)

// DeferredService provides deferred batch update functionality
type DeferredService struct {
	accountRepo AccountRepository
	timingWheel *TimingWheelService
	interval    time.Duration

	lastUsedUpdates sync.Map
	imageDetails    chan settlementImageDetailTask
	imageDetailsMu  sync.Mutex
}

type settlementImageDetailTask struct {
	repo   UsageSettlementRepository
	ref    UsageSettlementRef
	detail *ImageGenerationDetail
}

// NewDeferredService creates a new DeferredService instance
func NewDeferredService(accountRepo AccountRepository, timingWheel *TimingWheelService, interval time.Duration) *DeferredService {
	return &DeferredService{
		accountRepo:  accountRepo,
		timingWheel:  timingWheel,
		interval:     interval,
		imageDetails: make(chan settlementImageDetailTask, 128),
	}
}

// Start starts the deferred service
func (s *DeferredService) Start() {
	s.timingWheel.ScheduleRecurring("deferred:last_used", s.interval, s.flushLastUsed)
	s.timingWheel.ScheduleRecurring("deferred:image_details", time.Second, s.flushSettlementImageDetails)
	log.Printf("[DeferredService] Started (interval: %v)", s.interval)
}

// Stop stops the deferred service
func (s *DeferredService) Stop() {
	s.timingWheel.Cancel("deferred:last_used")
	s.timingWheel.Cancel("deferred:image_details")
	s.flushLastUsed()
	log.Printf("[DeferredService] Service stopped")
}

func (s *DeferredService) ScheduleLastUsedUpdate(accountID int64) {
	s.lastUsedUpdates.Store(accountID, time.Now())
}

// Bounded, best-effort display enrichment. No synchronous fallback on overflow.
func (s *DeferredService) ScheduleSettlementImageDetail(repo UsageSettlementRepository, ref UsageSettlementRef, detail *ImageGenerationDetail) {
	if s == nil || repo == nil || detail == nil {
		return
	}
	copy := cloneImageDetail(detail)
	if copy == nil {
		return
	}
	select {
	case s.imageDetails <- settlementImageDetailTask{repo, ref, copy}:
	default:
		log.Printf("[DeferredService] image detail queue full; bill retained request_id=%s", ref.RequestID)
	}
}
func (s *DeferredService) flushSettlementImageDetails() {
	if !s.imageDetailsMu.TryLock() {
		return
	}
	defer s.imageDetailsMu.Unlock()
	for i := 0; i < 8; i++ {
		select {
		case task := <-s.imageDetails:
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			err := task.repo.UpdateSettlementImageDetail(ctx, task.ref, task.detail)
			cancel()
			if err != nil {
				log.Printf("[DeferredService] image detail failed request_id=%s: %v", task.ref.RequestID, err)
			}
		default:
			return
		}
	}
}

func (s *DeferredService) flushLastUsed() {
	updates := make(map[int64]time.Time)
	s.lastUsedUpdates.Range(func(key, value any) bool {
		id, ok := key.(int64)
		if !ok {
			return true
		}
		ts, ok := value.(time.Time)
		if !ok {
			return true
		}
		updates[id] = ts
		s.lastUsedUpdates.Delete(key)
		return true
	})

	if len(updates) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.accountRepo.BatchUpdateLastUsed(ctx, updates); err != nil {
		log.Printf("[DeferredService] BatchUpdateLastUsed failed (%d accounts): %v", len(updates), err)
		for id, ts := range updates {
			s.lastUsedUpdates.Store(id, ts)
		}
	} else {
		log.Printf("[DeferredService] BatchUpdateLastUsed flushed %d accounts", len(updates))
	}
}
