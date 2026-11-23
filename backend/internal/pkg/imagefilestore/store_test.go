package imagefilestore

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testPolicy() Policy {
	return Policy{BaseURL: "https://images.test", MaxBytes: 128, Retention: 12 * time.Hour, CleanupInterval: 2 * time.Hour, AlertThresholdBytes: 32}
}

func download(store *Store, imageURL string, method string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest(method, imageURL, nil))
	return w
}

func TestLocalImagesSignedDownloadExpiryAndRestart(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	now := time.Date(2026, 11, 24, 1, 0, 0, 0, time.UTC)
	store := New(directory, "stable-secret", nil, nil)
	store.now = func() time.Time { return now }
	storage := store.Storage(testPolicy())
	imageURL, err := storage.Save(ctx, "images/imgtask_abc-0.png", "image/png", []byte("image-content"))
	require.NoError(t, err)
	require.Contains(t, imageURL, DownloadPath+"images/imgtask_abc-0.png?")
	response := download(store, imageURL, http.MethodGet)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "image-content", response.Body.String())
	require.Contains(t, response.Header().Get("Cache-Control"), "no-store")
	require.Equal(t, "no-store", response.Header().Get("CDN-Cache-Control"))
	require.Equal(t, "image/png", response.Header().Get("Content-Type"))
	require.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	head := download(store, imageURL, http.MethodHead)
	require.Equal(t, http.StatusOK, head.Code)
	require.Empty(t, head.Body.String())
	rangeRequest := httptest.NewRequest(http.MethodGet, imageURL, nil)
	rangeRequest.Header.Set("Range", "bytes=0-4")
	rangeResponse := httptest.NewRecorder()
	store.ServeHTTP(rangeResponse, rangeRequest)
	require.Equal(t, http.StatusPartialContent, rangeResponse.Code)
	require.Equal(t, "image", rangeResponse.Body.String())
	restarted := New(directory, "stable-secret", nil, nil)
	restarted.now = store.now
	require.Equal(t, http.StatusOK, download(restarted, imageURL, http.MethodGet).Code)
	require.Equal(t, http.StatusNotFound, download(New(directory, "different-secret", nil, nil), imageURL, http.MethodGet).Code)
	require.Equal(t, http.StatusNotFound, download(store, strings.Replace(imageURL, "imgtask_abc", "imgtask_other", 1), http.MethodGet).Code)
	u, err := url.Parse(imageURL)
	require.NoError(t, err)
	u.RawQuery = ""
	require.Equal(t, http.StatusNotFound, download(store, u.String(), http.MethodGet).Code)
	now = now.Add(12 * time.Hour)
	require.Equal(t, http.StatusNotFound, download(store, imageURL, http.MethodGet).Code)
	_, err = os.Stat(filepath.Join(directory, "objects", "images", "imgtask_abc-0.png"))
	require.NoError(t, err, "access expires independently from cleanup")
	require.NoError(t, store.Cleanup(ctx, testPolicy()))
	_, err = os.Stat(filepath.Join(directory, "objects", "images", "imgtask_abc-0.png"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLocalImageQuotaConcurrentAndRecoveredFromDisk(t *testing.T) {
	directory := t.TempDir()
	store := New(directory, "secret", nil, nil)
	policy := testPolicy()
	policy.MaxBytes = 64
	storage := store.Storage(policy)
	var saved atomic.Int32
	var wg sync.WaitGroup
	for _, key := range []string{"one.png", "two.png", "three.png", "four.png"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := storage.Save(context.Background(), key, "image/png", make([]byte, 32))
			if err == nil {
				saved.Add(1)
			} else {
				require.ErrorIs(t, err, ErrCapacity)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 2, saved.Load())
	restarted := New(directory, "secret", nil, nil).Storage(policy)
	_, err := restarted.Save(context.Background(), "another.png", "image/png", []byte("x"))
	require.ErrorIs(t, err, ErrCapacity, "restart must recover existing disk usage")
}

func TestLocalImageSameKeyAndPressureCleanup(t *testing.T) {
	store := New(t.TempDir(), "secret", nil, nil)
	now := time.Now()
	store.now = func() time.Time { return now }
	policy := testPolicy()
	policy.MaxBytes = 8
	storage := store.Storage(policy)
	first, err := storage.Save(context.Background(), "same.png", "image/png", []byte("12345678"))
	require.NoError(t, err)
	_, err = storage.Save(context.Background(), "same.png", "image/png", []byte("12345678"))
	require.NoError(t, err, "replacing an object must not count it twice")
	_, err = storage.Save(context.Background(), "new.png", "image/png", []byte("x"))
	require.ErrorIs(t, err, ErrCapacity)
	require.Equal(t, http.StatusOK, download(store, first, http.MethodGet).Code, "pressure must not evict a valid object")
	now = now.Add(policy.Retention)
	_, err = storage.Save(context.Background(), "new.png", "image/png", []byte("12345678"))
	require.NoError(t, err, "pressure can reclaim expired files before the next scheduled pass")
}

func TestLocalImagePathsAndCacheValidity(t *testing.T) {
	store := New(t.TempDir(), "secret", nil, nil)
	storage := store.Storage(testPolicy())
	for _, key := range []string{"../outside.png", "/absolute.png", "a/../b.png", "a\\b.png", "C:/image.png", ".", ""} {
		_, err := storage.Save(context.Background(), key, "image/png", []byte("image"))
		require.Error(t, err, key)
	}
	imageURL, err := storage.Save(context.Background(), "images/cache.png", "image/png", []byte("image"))
	require.NoError(t, err)
	require.True(t, storage.ValidateCachedURL(context.Background(), imageURL))
	require.LessOrEqual(t, storage.CacheTTL(imageURL, 7*24*time.Hour), 12*time.Hour)
	require.False(t, storage.ValidateCachedURL(context.Background(), strings.Replace(imageURL, "images.test", "other.test", 1)))
	require.NoError(t, os.Remove(filepath.Join(store.directory, "objects", "images", "cache.png")))
	require.False(t, storage.ValidateCachedURL(context.Background(), imageURL))
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(store.directory, "objects", "escape")); err != nil {
		t.Skip("symlinks unavailable: " + err.Error())
	}
	_, err = storage.Save(context.Background(), "escape/file.png", "image/png", []byte("image"))
	require.Error(t, err)
	_, err = os.Stat(filepath.Join(outside, "file.png"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestLocalCapacityAlertRetryRestartAndRecovery(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	policy := testPolicy()
	policy.MaxBytes, policy.AlertThresholdBytes = 100, 20
	now := time.Now()
	var attempts int
	var episodes []string
	notify := func(_ context.Context, alert CapacityAlert) error {
		attempts++
		episodes = append(episodes, alert.Episode)
		if attempts == 1 {
			return errors.New("SMTP unavailable")
		}
		return nil
	}
	store := New(directory, "secret", nil, notify)
	store.now = func() time.Time { return now }
	_, err := store.Storage(policy).Save(ctx, "full.png", "image/png", make([]byte, 80))
	require.NoError(t, err)
	require.ErrorContains(t, store.checkCapacity(ctx, policy), "SMTP unavailable")
	require.NoError(t, store.checkCapacity(ctx, policy))
	require.Equal(t, 1, attempts, "failed email attempts must be throttled")
	now = now.Add(5 * time.Minute)
	require.NoError(t, store.checkCapacity(ctx, policy))
	require.Equal(t, 2, attempts)
	require.Equal(t, episodes[0], episodes[1], "retry must preserve delivery identity")
	restarted := New(directory, "secret", nil, notify)
	restarted.now = store.now
	require.NoError(t, restarted.checkCapacity(ctx, policy))
	require.Equal(t, 2, attempts, "restart must not resend a delivered episode")
	now = now.Add(policy.Retention)
	require.NoError(t, restarted.Cleanup(ctx, policy))
	require.NoError(t, restarted.checkCapacity(ctx, policy))
	_, err = restarted.Storage(policy).Save(ctx, "full-again.png", "image/png", make([]byte, 80))
	require.NoError(t, err)
	require.NoError(t, restarted.checkCapacity(ctx, policy))
	require.Equal(t, 3, attempts)
	require.NotEqual(t, episodes[1], episodes[2], "recovery must rearm a fresh episode")
}

func TestLocalImageProbeLeavesNoImage(t *testing.T) {
	directory := t.TempDir()
	storage := New(directory, "secret", nil, nil).Storage(testPolicy())
	require.NoError(t, storage.TestConnection(context.Background()))
	for _, name := range []string{"tmp", "objects"} {
		entries, err := os.ReadDir(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Empty(t, entries)
	}
	response := download(storage.store, "https://images.test/v1/image-files/../anything", http.MethodGet)
	require.Equal(t, http.StatusNotFound, response.Code)
}

func TestLocalImageInterruptedCommitRestoresPreviousObject(t *testing.T) {
	directory := t.TempDir()
	store := New(directory, "secret", nil, nil)
	imageURL, err := store.Storage(testPolicy()).Save(context.Background(), "same.png", "image/png", []byte("previous"))
	require.NoError(t, err)
	root, err := store.open(true)
	require.NoError(t, err)
	tx := transaction{Key: "same.png", ID: strings.Repeat("a", 32), HadObject: true, HadRecord: true}
	marker, err := json.Marshal(tx)
	require.NoError(t, err)
	require.NoError(t, writeAtomic(root, "transaction.json", marker))
	require.NoError(t, root.Rename("objects/same.png", "tmp/"+tx.ID+"-old-object"))
	require.NoError(t, root.Rename(recordPath("same.png"), "tmp/"+tx.ID+"-old-record"))
	require.NoError(t, root.WriteFile("objects/same.png", []byte("incomplete replacement"), 0600))
	require.NoError(t, root.Close())
	restarted := New(directory, "secret", nil, nil)
	response := download(restarted, imageURL, http.MethodGet)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, "previous", response.Body.String())
	_, err = os.Stat(filepath.Join(directory, "transaction.json"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
