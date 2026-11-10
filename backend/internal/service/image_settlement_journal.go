package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// Financial snapshots only: no prompt, image payload, credentials or related
// user/account objects. Pending files survive a database outage and restart.
// Use the application's persistent data volume, never the temporary directory.
var imageJournalLocks [64]sync.Mutex

func imageJournalDirectory(cfg *config.Config) string {
	base := "./data"
	if cfg != nil && strings.TrimSpace(cfg.Pricing.DataDir) != "" {
		base = cfg.Pricing.DataDir
	}
	return filepath.Join(base, "image-settlement-facts")
}
func imageJournalName(s *UsageSettlementSnapshot) (string, int) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", s.Command.RequestID, s.Command.APIKeyID)))
	return hex.EncodeToString(sum[:]) + ".pending", int(sum[0]) % len(imageJournalLocks)
}
func saveImageSettlementFacts(cfg *config.Config, s *UsageSettlementSnapshot) (string, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	if len(data) > 60000 {
		return "", ErrUsageSettlementConflict
	}
	name, slot := imageJournalName(s)
	imageJournalLocks[slot].Lock()
	defer imageJournalLocks[slot].Unlock()
	dir := imageJournalDirectory(cfg)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if old, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(old, data) {
			return "", ErrUsageSettlementConflict
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) >= 10000 {
		return "", errors.New("image settlement journal full; retained facts must be recovered")
	}
	f, err := os.CreateTemp(dir, ".facts-")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// The worker advances a cursor so one broken/conflicting file cannot starve
// later bills. Failures remain on disk and visible until explicitly resolved.
func replayImageSettlementFacts(ctx context.Context, cfg *config.Config, repo UsageSettlementRepository, cursor *string) error {
	dir := imageJournalDirectory(cfg)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".pending") {
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil
	}
	name := names[0]
	for _, candidate := range names {
		if candidate > *cursor {
			name = candidate
			break
		}
	}
	*cursor = name
	path := filepath.Join(dir, name)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, 60001))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(data) > 60000 {
		return ErrUsageSettlementConflict
	}
	var snapshot UsageSettlementSnapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	expected, _ := imageJournalName(&snapshot)
	if expected != name {
		return ErrUsageSettlementConflict
	}
	if err = repo.PrepareSettlement(ctx, &snapshot); err != nil {
		return err
	}
	// The durable DB snapshot is now authoritative; retrying deletion is safe.
	if err = os.Remove(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
