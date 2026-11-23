package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/imagefilestore"
)

func LocalImageStoragePolicy(cfg *config.ImageStorageConfig) (imagefilestore.Policy, error) {
	settings := settingsFromConfig(*cfg)
	if err := validateImageStorageSettings(*settings); err != nil {
		return imagefilestore.Policy{}, err
	}
	return imagefilestore.Policy{
		BaseURL:             settings.LocalPublicBaseURL,
		MaxBytes:            int64(settings.LocalMaxSizeGB) << 30,
		Retention:           time.Duration(settings.LocalRetentionHours) * time.Hour,
		CleanupInterval:     time.Duration(settings.LocalCleanupIntervalHours) * time.Hour,
		AlertThresholdBytes: int64(settings.LocalAlertThresholdGB) << 30,
	}, nil
}

// ProvideLocalImageStore keeps downloads, cleanup and alerts alive after switching to S3.
// SettingRepository depends on initialized Ent, so JWT bootstrap has completed here.
func ProvideLocalImageStore(cfg *config.Config, settings SettingRepository, notifications *NotificationEmailService) (*imagefilestore.Store, func()) {
	directory := imagefilestore.ResolveDirectory(cfg.ImageStorage.LocalDirectory)
	identity := sha256.Sum256([]byte(directory))
	policy := func(ctx context.Context) (imagefilestore.Policy, error) {
		current := settingsFromConfig(cfg.ImageStorage)
		raw, err := settings.GetValue(ctx, settingKeyImageStorageConfig)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return imagefilestore.Policy{}, err
		}
		if strings.TrimSpace(raw) != "" {
			if err := json.Unmarshal([]byte(raw), current); err != nil {
				return imagefilestore.Policy{}, err
			}
		}
		normalizeImageStorageSettings(current)
		// Maintenance does not require a currently enabled upload backend or public URL.
		current.Backend, current.Enabled = "s3", false
		return LocalImageStoragePolicy(&config.ImageStorageConfig{
			LocalMaxSizeGB:            current.LocalMaxSizeGB,
			LocalRetentionHours:       current.LocalRetentionHours,
			LocalCleanupIntervalHours: current.LocalCleanupIntervalHours,
			LocalAlertThresholdGB:     current.LocalAlertThresholdGB,
		})
	}
	notify := func(ctx context.Context, alert imagefilestore.CapacityAlert) error {
		return sendLocalImageCapacityAlert(ctx, settings, notifications, hex.EncodeToString(identity[:]), alert)
	}
	store := imagefilestore.New(directory, cfg.JWT.Secret, policy, notify)
	store.Start()
	return store, store.Stop
}

func localImageAlertRecipients(ctx context.Context, settings SettingRepository) ([]string, error) {
	raw, err := settings.GetValue(ctx, SettingKeyOpsEmailNotificationConfig)
	if err != nil && !errors.Is(err, ErrSettingNotFound) {
		return nil, err
	}
	var ops OpsEmailNotificationConfig
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &ops); err != nil {
			return nil, err
		}
	}
	var recipients []string
	if ops.Alert.Enabled {
		recipients = ops.Alert.Recipients
	}
	if len(recipients) == 0 {
		raw, err = settings.GetValue(ctx, SettingKeyAccountQuotaNotifyEmails)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return nil, err
		}
		recipients = filterVerifiedEmails(ParseNotifyEmails(raw))
	}
	seen := make(map[string]bool)
	unique := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		recipient = strings.TrimSpace(recipient)
		key := strings.ToLower(recipient)
		if recipient != "" && !seen[key] {
			seen[key] = true
			unique = append(unique, recipient)
		}
	}
	return unique, nil
}

func sendLocalImageCapacityAlert(ctx context.Context, settings SettingRepository, notifications *NotificationEmailService, identity string, alert imagefilestore.CapacityAlert) error {
	recipients, err := localImageAlertRecipients(ctx, settings)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return errors.New("local image capacity is low but no notification recipients are configured")
	}
	const gib = float64(1 << 30)
	remaining := alert.MaxBytes - alert.UsedBytes
	if remaining < 0 {
		remaining = 0
	}
	var failures []error
	for _, recipient := range recipients {
		err := notifications.Send(ctx, NotificationEmailSendInput{
			Event:          NotificationEmailEventOpsAlert,
			Locale:         "zh",
			RecipientEmail: recipient,
			SourceType:     "local-image-storage",
			SourceID:       identity,
			ReminderKey:    alert.Episode,
			Variables: map[string]string{
				"rule_name":         "本地图片存储容量预警",
				"severity":          "warning",
				"alert_status":      "firing",
				"metric_type":       "local_image_remaining_gib",
				"operator":          "<=",
				"metric_value":      fmt.Sprintf("%.2f GiB", float64(remaining)/gib),
				"threshold_value":   fmt.Sprintf("%.2f GiB", float64(alert.ThresholdBytes)/gib),
				"triggered_at":      time.Now().UTC().Format(time.RFC3339),
				"alert_description": fmt.Sprintf("本地图片已占用 %.2f / %.2f GiB，剩余 %.2f GiB。请检查过期清理任务，或调整本地图片容量。", float64(alert.UsedBytes)/gib, float64(alert.MaxBytes)/gib, float64(remaining)/gib),
			},
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
