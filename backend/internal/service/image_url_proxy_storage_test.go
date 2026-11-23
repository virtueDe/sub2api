package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/imagefilestore"
	"github.com/stretchr/testify/require"
)

type localImageProxyCache struct {
	values map[string]string
	ttl    time.Duration
}

func (c *localImageProxyCache) Get(_ context.Context, key string) (string, error) {
	return c.values[key], nil
}
func (c *localImageProxyCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	c.values[key], c.ttl = value, ttl
	return nil
}

type switchableImageStorage struct{ current ImageStorage }

func (s *switchableImageStorage) CurrentStorage() ImageStorage { return s.current }
func (s *switchableImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	return s.current.Save(ctx, key, contentType, data)
}

func TestLocalImageProxyTargetSwitchCacheLifetimeAndMissingFile(t *testing.T) {
	ctx := context.Background()
	data := []byte("source-image")
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first := imagefilestore.New(firstDir, "secret", nil, nil)
	policy := imagefilestore.Policy{BaseURL: "https://images.test", MaxBytes: 4096, Retention: 12 * time.Hour}
	firstStorage := first.Storage(policy)
	second := imagefilestore.New(secondDir, "secret", nil, nil)
	target := &switchableImageStorage{current: firstStorage}
	cache := &localImageProxyCache{values: map[string]string{}}
	proxy := NewImageURLProxy(&config.ImageURLProxyConfig{StorageKeyPrefix: "proxy/", CacheTTLHours: 168}, target, cache)
	var downloads int
	proxy.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		downloads++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data))), Header: http.Header{"Content-Type": {"image/png"}}}, nil
	})
	source := "https://bucket.volces.com/reference.png?X-Tos-Signature=source"
	imageURL, err := proxy.ProxyURL(ctx, source)
	require.NoError(t, err)
	require.Equal(t, 1, downloads)
	require.LessOrEqual(t, cache.ttl, 12*time.Hour)
	for key := range cache.values {
		require.Contains(t, key, "img_proxy:v2:")
	}
	repeated, err := proxy.ProxyURL(ctx, source)
	require.NoError(t, err)
	require.Equal(t, imageURL, repeated)
	require.Equal(t, 1, downloads)
	hash := sha256.Sum256(data)
	filename := hex.EncodeToString(hash[:]) + ".png"
	_, err = os.Stat(filepath.Join(firstDir, "objects", "proxy", filename))
	require.NoError(t, err, "proxy key naming is unchanged")
	target.current = second.Storage(policy)
	_, err = proxy.ProxyURL(ctx, source)
	require.NoError(t, err)
	require.Equal(t, 2, downloads, "a different target must not reuse the old cache")
	require.NoError(t, os.Remove(filepath.Join(secondDir, "objects", "proxy", filename)))
	_, err = proxy.ProxyURL(ctx, source)
	require.NoError(t, err)
	require.Equal(t, 3, downloads, "a missing local image must be rehosted")
	target.current = firstStorage
	require.Equal(t, imageURL, stripImageURLQueryPreservingStorage(ctx, proxy, imageURL))
	require.Equal(t, "https://bucket.test/reference.png", stripImageURLQueryPreservingStorage(ctx, proxy, "https://bucket.test/reference.png?secret=source"))
	s3URL := "https://s3.test/images/old.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20261124T010000Z&X-Amz-Expires=43200&X-Amz-Signature=old"
	require.Equal(t, s3URL, stripImageURLQueryPreservingStorage(ctx, proxy, s3URL), "old S3 signatures survive a backend switch")
}

func TestLocalImageStorageBindsProxyTargetDuringDownload(t *testing.T) {
	ctx := context.Background()
	firstDir, secondDir := t.TempDir(), t.TempDir()
	policy := imagefilestore.Policy{BaseURL: "https://images.test", MaxBytes: 4096, Retention: time.Hour}
	first := imagefilestore.New(firstDir, "secret", nil, nil)
	second := imagefilestore.New(secondDir, "secret", nil, nil)
	target := &switchableImageStorage{current: first.Storage(policy)}
	proxy := NewImageURLProxy(&config.ImageURLProxyConfig{}, target, nil)
	proxy.httpClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		target.current = second.Storage(policy)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("image")), Header: http.Header{"Content-Type": {"image/png"}}}, nil
	})
	imageURL, err := proxy.ProxyURL(ctx, "https://bucket.volces.com/reference.png")
	require.NoError(t, err)
	response := httptest.NewRecorder()
	first.ServeHTTP(response, httptest.NewRequest(http.MethodGet, imageURL, nil))
	require.Equal(t, 200, response.Code)
	_, err = os.Stat(filepath.Join(secondDir, "objects"))
	require.ErrorIs(t, err, os.ErrNotExist, "a mid-request config switch must not move the write")
}
