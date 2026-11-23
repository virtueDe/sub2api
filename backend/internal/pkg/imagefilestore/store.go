// Package imagefilestore stores temporary images below a dedicated, rooted directory.
package imagefilestore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DownloadPath = "/v1/image-files/"

var ErrCapacity = errors.New("local image storage capacity exceeded")

type Policy struct {
	BaseURL             string
	MaxBytes            int64
	Retention           time.Duration
	CleanupInterval     time.Duration
	AlertThresholdBytes int64
}

type CapacityAlert struct {
	Episode        string `json:"episode"`
	Sent           bool   `json:"sent"`
	UsedBytes      int64  `json:"used_bytes"`
	MaxBytes       int64  `json:"max_bytes"`
	ThresholdBytes int64  `json:"threshold_bytes"`
}

type record struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	ExpiresAt   int64  `json:"expires_at"`
}

type transaction struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	HadObject bool   `json:"had_object"`
	HadRecord bool   `json:"had_record"`
}

// Store owns one directory and one cleanup worker, independently of the upload backend.
// A directory must be owned by a single application process.
type Store struct {
	directory        string
	secret           []byte
	mu               sync.Mutex
	used             int64
	indexed          bool
	now              func() time.Time
	cancel           context.CancelFunc
	done             chan struct{}
	wake             chan struct{}
	policy           func(context.Context) (Policy, error)
	notify           func(context.Context, CapacityAlert) error
	lastAlertAttempt time.Time
}

func New(directory, secret string, policy func(context.Context) (Policy, error), notify func(context.Context, CapacityAlert) error) *Store {
	key := sha256.Sum256([]byte("sub2api/local-image-storage/v1\x00" + secret))
	return &Store{directory: directory, secret: key[:], now: time.Now, wake: make(chan struct{}, 1), policy: policy, notify: notify}
}

func (s *Store) Storage(policy Policy) *Storage { return &Storage{store: s, policy: policy} }

type Storage struct {
	store  *Store
	policy Policy
}

func (s *Store) open(create bool) (*os.Root, error) {
	if create {
		if err := os.MkdirAll(s.directory, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(s.directory)
	if err != nil {
		return nil, err
	}
	if create {
		for _, name := range []string{"objects", "records", "tmp"} {
			if err := root.MkdirAll(name, 0700); err != nil {
				_ = root.Close()
				return nil, err
			}
		}
	}
	if err := s.recoverTransaction(root); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

// A durable rollback marker protects the two-file object/expiry commit, including restart.
func (s *Store) recoverTransaction(root *os.Root) error {
	var tx transaction
	if err := readJSON(root, "transaction.json", &tx); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !validKey(tx.Key) || len(tx.ID) != 32 {
		return errors.New("invalid local image transaction")
	}
	if _, err := hex.DecodeString(tx.ID); err != nil {
		return err
	}
	for _, item := range []struct {
		name, backup string
		existed      bool
	}{
		{"objects/" + tx.Key, "tmp/" + tx.ID + "-old-object", tx.HadObject},
		{recordPath(tx.Key), "tmp/" + tx.ID + "-old-record", tx.HadRecord},
	} {
		_, err := root.Lstat(item.backup)
		if err == nil {
			if err := root.Remove(item.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := root.Rename(item.backup, item.name); err != nil {
				return err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		} else if !item.existed {
			if err := root.Remove(item.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	s.indexed = false
	return root.Remove("transaction.json")
}

func (s *Store) commitObject(root *os.Root, key string, data, metadata []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tx := transaction{Key: key, ID: hex.EncodeToString(nonce[:])}
	object, recordName := "objects/"+key, recordPath(key)
	for _, item := range []struct {
		name    string
		existed *bool
	}{{object, &tx.HadObject}, {recordName, &tx.HadRecord}} {
		info, err := root.Lstat(item.name)
		if err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("image transaction target is not a regular file")
			}
			*item.existed = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	stagedObject, stagedRecord := "tmp/"+tx.ID+"-new-object", "tmp/"+tx.ID+"-new-record"
	defer func() { _ = root.Remove(stagedObject); _ = root.Remove(stagedRecord) }()
	if err := writeAtomic(root, stagedObject, data); err != nil {
		return err
	}
	if err := writeAtomic(root, stagedRecord, metadata); err != nil {
		return err
	}
	marker, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	if err := writeAtomic(root, "transaction.json", marker); err != nil {
		return err
	}
	rollback := func(commitErr error) error { return errors.Join(commitErr, s.recoverTransaction(root)) }
	if tx.HadObject {
		if err := root.Rename(object, "tmp/"+tx.ID+"-old-object"); err != nil {
			return rollback(err)
		}
	}
	if tx.HadRecord {
		if err := root.Rename(recordName, "tmp/"+tx.ID+"-old-record"); err != nil {
			return rollback(err)
		}
	}
	if err := root.Rename(stagedObject, object); err != nil {
		return rollback(err)
	}
	if err := root.Rename(stagedRecord, recordName); err != nil {
		return rollback(err)
	}
	if err := root.Remove("transaction.json"); err != nil {
		return rollback(err)
	}
	for _, backup := range []string{"tmp/" + tx.ID + "-old-object", "tmp/" + tx.ID + "-old-record"} {
		if err := root.Remove(backup); err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("local image old version cleanup failed", "error", err)
		}
	}
	return nil
}

func validKey(key string) bool {
	return key != "" && key != "." && path.Clean(key) == key && !strings.HasPrefix(key, "/") &&
		!strings.HasPrefix(key, "../") && !strings.ContainsAny(key, "\\\x00:")
}

func recordPath(key string) string {
	hash := sha256.Sum256([]byte(key))
	return "records/" + hex.EncodeToString(hash[:]) + ".json"
}

func readJSON(root *os.Root, name string, value any) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("image metadata is not a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewDecoder(io.LimitReader(f, 65536)).Decode(value)
}

func writeAtomic(root *os.Root, name string, data []byte) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := "tmp/" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmp) }()
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func (s *Store) index(root *os.Root) error {
	var used int64
	err := fs.WalkDir(root.FS(), "objects", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular image object: %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		used += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err == nil {
		s.used, s.indexed = used, true
	}
	return err
}

func (s *Storage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	if !validKey(key) {
		return "", errors.New("invalid local image key")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	store := s.store
	store.mu.Lock()
	defer store.mu.Unlock()
	root, err := store.open(true)
	if err != nil {
		return "", fmt.Errorf("open local image directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if !store.indexed {
		if err := store.index(root); err != nil {
			return "", err
		}
	}
	name := "objects/" + key
	oldSize := int64(0)
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("image target is not a regular file")
		}
		oldSize = info.Size()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	delta := int64(len(data)) - oldSize
	if delta > 0 && store.used+delta > s.policy.MaxBytes {
		store.signal()
		if err := store.cleanupLocked(ctx, root, s.policy); err != nil {
			return "", err
		}
		// Cleanup may have removed an expired previous object with the same key.
		if _, err := root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			delta = int64(len(data))
		}
		if store.used+delta > s.policy.MaxBytes {
			return "", ErrCapacity
		}
	}
	if err := root.MkdirAll(path.Dir(name), 0700); err != nil {
		return "", err
	}
	expires := store.now().Add(s.policy.Retention).Unix()
	objectExpires := expires
	var previous record
	if err := readJSON(root, recordPath(key), &previous); err == nil {
		if previous.Key != key {
			return "", errors.New("local image metadata key mismatch")
		}
		// Rehosting a content-addressed proxy image must not revoke older, longer links.
		if previous.ExpiresAt > objectExpires {
			objectExpires = previous.ExpiresAt
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	metadata, err := json.Marshal(record{Key: key, ContentType: contentType, ExpiresAt: objectExpires})
	if err != nil {
		return "", err
	}
	if err := store.commitObject(root, key, data, metadata); err != nil {
		store.indexed = false
		return "", fmt.Errorf("commit local image: %w", err)
	}
	store.used += delta
	store.signal()
	return s.signedURL(key, expires), nil
}

func (s *Storage) signedURL(key string, expires int64) string {
	u, _ := url.Parse(s.policy.BaseURL)
	u.Path = DownloadPath + key
	q := url.Values{"expires": {strconv.FormatInt(expires, 10)}, "signature": {s.store.signature(key, expires)}}
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Store) signature(key string, expires int64) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = fmt.Fprintf(mac, "v1\x00%s\x00%d", key, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) authorize(key string, query url.Values) bool {
	if !validKey(key) || len(query["expires"]) != 1 || len(query["signature"]) != 1 {
		return false
	}
	expires, err := strconv.ParseInt(query.Get("expires"), 10, 64)
	if err != nil || expires <= s.now().Unix() {
		return false
	}
	actual, err := hex.DecodeString(query.Get("signature"))
	if err != nil {
		return false
	}
	expected, _ := hex.DecodeString(s.signature(key, expires))
	return hmac.Equal(actual, expected)
}

func (s *Store) openImage(key string, query url.Values) (*os.File, record, error) {
	var metadata record
	if !s.authorize(key, query) {
		return nil, metadata, fs.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.open(false)
	if err != nil {
		return nil, metadata, err
	}
	defer func() { _ = root.Close() }()
	if err := readJSON(root, recordPath(key), &metadata); err != nil {
		return nil, metadata, err
	}
	expires, _ := strconv.ParseInt(query.Get("expires"), 10, 64)
	if metadata.Key != key || metadata.ExpiresAt <= s.now().Unix() || expires > metadata.ExpiresAt {
		return nil, metadata, fs.ErrNotExist
	}
	info, err := root.Lstat("objects/" + key)
	if err != nil {
		return nil, metadata, err
	}
	if !info.Mode().IsRegular() {
		return nil, metadata, fs.ErrNotExist
	}
	f, err := root.Open("objects/" + key)
	return f, metadata, err
}

func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	key, ok := strings.CutPrefix(r.URL.Path, DownloadPath)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, metadata, err := s.openImage(key, r.URL.Query())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("local image download failed", "error", err)
		}
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	mediaType, _, err := mime.ParseMediaType(metadata.ContentType)
	if err != nil {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	if mediaType == "image/svg+xml" || !strings.HasPrefix(mediaType, "image/") {
		w.Header().Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, path.Base(key), time.Time{}, f)
}

func (s *Storage) CacheNamespace() string {
	hash := sha256.Sum256([]byte("local\x00" + s.store.directory + "\x00" + s.policy.BaseURL + "\x00" + s.policy.Retention.String()))
	return hex.EncodeToString(hash[:])
}

func (s *Storage) ValidateCachedURL(_ context.Context, imageURL string) bool {
	u, err := url.Parse(imageURL)
	if err != nil {
		return false
	}
	base, err := url.Parse(s.policy.BaseURL)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host {
		return false
	}
	key, ok := strings.CutPrefix(u.Path, DownloadPath)
	if !ok || s.CacheTTL(imageURL, time.Minute) < time.Minute {
		return false
	}
	f, _, err := s.store.openImage(key, u.Query())
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func (s *Storage) CacheTTL(imageURL string, requested time.Duration) time.Duration {
	u, err := url.Parse(imageURL)
	if err != nil {
		return 0
	}
	expires, err := strconv.ParseInt(u.Query().Get("expires"), 10, 64)
	if err != nil {
		return 0
	}
	return min(requested, time.Unix(expires, 0).Sub(s.store.now()))
}

func (s *Storage) TestConnection(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	root, err := s.store.open(true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := "tmp/probe-" + hex.EncodeToString(nonce[:])
	if err := root.WriteFile(name, []byte("image-storage-probe"), 0600); err != nil {
		return err
	}
	data, readErr := root.ReadFile(name)
	removeErr := root.Remove(name)
	if readErr == nil && string(data) != "image-storage-probe" {
		readErr = errors.New("local storage probe read mismatch")
	}
	return errors.Join(readErr, removeErr)
}

func (s *Store) Cleanup(ctx context.Context, policy Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.open(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return s.cleanupLocked(ctx, root, policy)
}

func (s *Store) cleanupLocked(ctx context.Context, root *os.Root, policy Policy) error {
	now := s.now()
	var failures []error
	err := fs.WalkDir(root.FS(), "objects", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular image object: %s", name)
		}
		key := strings.TrimPrefix(name, "objects/")
		var metadata record
		err := readJSON(root, recordPath(key), &metadata)
		expired := err == nil && metadata.Key == key && metadata.ExpiresAt <= now.Unix()
		if errors.Is(err, fs.ErrNotExist) {
			info, statErr := entry.Info()
			if statErr != nil {
				return statErr
			}
			expired = !info.ModTime().Add(policy.Retention).After(now)
		} else if err != nil {
			failures = append(failures, fmt.Errorf("read image metadata: %w", err))
			return nil
		}
		if !expired {
			return nil
		}
		if err := root.Remove(name); err != nil {
			failures = append(failures, err)
			return nil
		}
		if err := root.Remove(recordPath(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}
		return nil
	})
	if err != nil {
		failures = append(failures, err)
	}
	// Interrupted writes only leave private temporary files; never remove active writes.
	if entries, err := fs.ReadDir(root.FS(), "tmp"); err == nil {
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if info.Mode().IsRegular() && !info.ModTime().Add(time.Hour).After(now) {
				if err := root.Remove("tmp/" + entry.Name()); err != nil {
					failures = append(failures, err)
				}
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		failures = append(failures, err)
	}
	if err := s.index(root); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (s *Store) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var lastCleanup time.Time
		for {
			checkCtx, checkCancel := context.WithTimeout(ctx, time.Minute)
			policy, err := s.policy(checkCtx)
			if err == nil && (lastCleanup.IsZero() || s.now().Sub(lastCleanup) >= policy.CleanupInterval) {
				err = s.Cleanup(checkCtx, policy)
				if err == nil {
					lastCleanup = s.now()
				}
			}
			if err == nil {
				err = s.checkCapacity(checkCtx, policy)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("local image maintenance failed", "error", err)
			}
			checkCancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
}

func (s *Store) Stop() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}

func (s *Store) checkCapacity(ctx context.Context, policy Policy) error {
	if s.notify == nil {
		return nil
	}
	s.mu.Lock()
	root, err := s.open(false)
	if err != nil {
		s.mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	if !s.indexed {
		err = s.index(root)
	}
	if err != nil {
		s.mu.Unlock()
		return err
	}
	if policy.MaxBytes-s.used > policy.AlertThresholdBytes {
		s.lastAlertAttempt = time.Time{}
		err = root.Remove("capacity-alert.json")
		s.mu.Unlock()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var alert CapacityAlert
	err = readJSON(root, "capacity-alert.json", &alert)
	if errors.Is(err, fs.ErrNotExist) {
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err == nil {
			alert.Episode = hex.EncodeToString(nonce[:])
			data, _ := json.Marshal(alert)
			err = writeAtomic(root, "capacity-alert.json", data)
		}
	}
	alert.UsedBytes, alert.MaxBytes, alert.ThresholdBytes = s.used, policy.MaxBytes, policy.AlertThresholdBytes
	s.mu.Unlock()
	if err != nil || alert.Sent {
		return err
	}
	if !s.lastAlertAttempt.IsZero() && s.now().Sub(s.lastAlertAttempt) < 5*time.Minute {
		return nil
	}
	s.lastAlertAttempt = s.now()
	if err := s.notify(ctx, alert); err != nil {
		return err
	}
	alert.Sent = true
	data, err := json.Marshal(alert)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(root, "capacity-alert.json", data)
}

// ResolveDirectory follows the deployment DATA_DIR convention without importing setup.
func ResolveDirectory(configured string) string {
	if configured != "" {
		return configured
	}
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		if info, err := os.Stat("/app/data"); err == nil && info.IsDir() {
			dataDir = "/app/data"
		} else {
			dataDir = "./data"
		}
	}
	return filepath.Join(dataDir, "image-storage")
}
