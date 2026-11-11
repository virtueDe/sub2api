package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const maxImageDiagnosticUsers = 50

type cachedImageDiagnosticPolicy struct {
	users   map[int64]struct{}
	expires time.Time
}

func validateImageDiagnosticUserIDs(ids []int64) ([]int64, error) {
	if len(ids) > maxImageDiagnosticUsers {
		return nil, infraerrors.BadRequest("INVALID_IMAGE_DIAGNOSTIC_USERS", "Select at most 50 users for image diagnostics")
	}
	for _, id := range ids {
		if id <= 0 || id > 9007199254740991 {
			return nil, infraerrors.BadRequest("INVALID_IMAGE_DIAGNOSTIC_USERS", "Image diagnostic user IDs must be positive safe integers")
		}
	}
	return normalizeSettingInt64List(ids), nil
}

func (s *SettingService) invalidateImageDiagnosticPolicy() {
	s.imageDiagnosticMu.Lock()
	defer s.imageDiagnosticMu.Unlock()
	s.imageDiagnosticCache.Store(&cachedImageDiagnosticPolicy{})
	s.imageDiagnosticSF.Forget("image_diagnostics")
}

// IsImageDiagnosticUserEnabled is fail-closed and cached per service instance.
// Updates invalidate locally; other replicas observe changes within 30 seconds.
// A diagnostics lookup cannot wait indefinitely behind a database incident.
func (s *SettingService) IsImageDiagnosticUserEnabled(ctx context.Context, userID int64) bool {
	if s == nil || s.settingRepo == nil || userID <= 0 {
		return false
	}
	if cached, _ := s.imageDiagnosticCache.Load().(*cachedImageDiagnosticPolicy); cached != nil && time.Now().Before(cached.expires) {
		_, ok := cached.users[userID]
		return ok
	}
	result := s.imageDiagnosticSF.DoChan("image_diagnostics", func() (any, error) {
		s.imageDiagnosticMu.Lock()
		defer s.imageDiagnosticMu.Unlock()
		if cached, _ := s.imageDiagnosticCache.Load().(*cachedImageDiagnosticPolicy); cached != nil && time.Now().Before(cached.expires) {
			return cached, nil
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(readCtx, []string{SettingKeyImageDiagnosticEnabled, SettingKeyImageDiagnosticUserIDs})
		cached := &cachedImageDiagnosticPolicy{expires: time.Now().Add(30 * time.Second)}
		if err == nil && values[SettingKeyImageDiagnosticEnabled] == "true" {
			var ids []int64
			err = json.Unmarshal([]byte(values[SettingKeyImageDiagnosticUserIDs]), &ids)
			if err == nil {
				ids, err = validateImageDiagnosticUserIDs(ids)
			}
			if err == nil {
				cached.users = make(map[int64]struct{}, len(ids))
				for _, id := range ids {
					cached.users[id] = struct{}{}
				}
			}
		}
		if err != nil {
			cached.expires = time.Now().Add(5 * time.Second)
			slog.Warn("image diagnostics policy unavailable; capture disabled")
		}
		s.imageDiagnosticCache.Store(cached)
		return cached, nil
	})
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	case loaded := <-result:
		cached, _ := loaded.Val.(*cachedImageDiagnosticPolicy)
		if cached == nil {
			return false
		}
		_, ok := cached.users[userID]
		return ok
	}
}

type imageDiagnosticSettingsKey struct{}

// WithImageDiagnosticSettings only supplies policy; it does not authorize a user.
func WithImageDiagnosticSettings(ctx context.Context, settings *SettingService) context.Context {
	return context.WithValue(ctx, imageDiagnosticSettingsKey{}, settings)
}

// NewImageGenerationCaptureForUser is called only with authenticated user IDs.
// The empty marker remains present when recording is disabled, for billing.
func NewImageGenerationCaptureForUser(ctx context.Context, userID int64, contentType string, body []byte) *ImageGenerationCapture {
	settings, _ := ctx.Value(imageDiagnosticSettingsKey{}).(*SettingService)
	if !settings.IsImageDiagnosticUserEnabled(ctx, userID) {
		return NewImageGenerationCapture(contentType, body)
	}
	return newAuthorizedImageCapture(userID, contentType, body)
}
