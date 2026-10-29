package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/robfig/cron/v3"
)

const imagePayloadSettingsKey = "image_payload_cleanup"
const imagePayloadCron = "0 2 * * *"

type ImagePayloadCleanupSettings struct {
	Enabled    bool `json:"enabled"`
	RetainDays int  `json:"retain_days"`
	// Read-only: the same location is used by cron and the retention cutoff.
	Timezone string `json:"timezone"`
}

type ImagePayloadCleanupService struct {
	settings  SettingRepository
	db        *sql.DB
	location  *time.Location
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	scheduler *cron.Cron
}

func NewImagePayloadCleanupService(settings SettingRepository, db *sql.DB, cfg *config.Config) (*ImagePayloadCleanupService, error) {
	loc := time.Local
	if cfg != nil && cfg.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("image payload cleanup timezone: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ImagePayloadCleanupService{settings: settings, db: db, location: loc, ctx: ctx, cancel: cancel}, nil
}

func validateImagePayloadSettings(cfg ImagePayloadCleanupSettings) error {
	if cfg.RetainDays < 1 || cfg.RetainDays > 365 {
		return infraerrors.BadRequest("INVALID_IMAGE_PAYLOAD_RETENTION", "image output retention must be between 1 and 365 days")
	}
	return nil
}

func (s *ImagePayloadCleanupService) GetSettings(ctx context.Context) (*ImagePayloadCleanupSettings, error) {
	raw, err := s.settings.GetValue(ctx, imagePayloadSettingsKey)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	return parseImagePayloadSettings(raw, s.location)
}

func parseImagePayloadSettings(raw string, location *time.Location) (*ImagePayloadCleanupSettings, error) {
	cfg := ImagePayloadCleanupSettings{RetainDays: 3}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return nil, fmt.Errorf("decode image payload settings: %w", err)
		}
	}
	if err := validateImagePayloadSettings(cfg); err != nil {
		return nil, err
	}
	cfg.Timezone = location.String()
	return &cfg, nil
}

func (s *ImagePayloadCleanupService) UpdateSettings(ctx context.Context, cfg ImagePayloadCleanupSettings) (*ImagePayloadCleanupSettings, error) {
	if err := validateImagePayloadSettings(cfg); err != nil {
		return nil, err
	}
	cfg.Timezone = s.location.String()
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.settings.Set(ctx, imagePayloadSettingsKey, string(raw)); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (s *ImagePayloadCleanupService) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scheduler != nil || s.ctx.Err() != nil {
		return
	}
	s.scheduler = cron.New(cron.WithLocation(s.location), cron.WithChain(cron.SkipIfStillRunning(cron.DefaultLogger)))
	// Fixed schedule: every instance reads the shared switch at run time. Saving
	// settings never starts a destructive job, and no startup catch-up is run.
	_, err := s.scheduler.AddFunc(imagePayloadCron, func() {
		if err := s.run(s.ctx, time.Now()); err != nil {
			logger.LegacyPrintf("service.image_payload_cleanup", "cleanup failed: %v", err)
		}
	})
	if err != nil {
		panic(err)
	} // compile-time constant schedule
	s.scheduler.Start()
}

func (s *ImagePayloadCleanupService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	if s.scheduler != nil {
		<-s.scheduler.Stop().Done()
	}
}

func imagePayloadCutoff(now time.Time, days int, loc *time.Location) time.Time {
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1-days)
}
