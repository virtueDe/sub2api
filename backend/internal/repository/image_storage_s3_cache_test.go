package repository

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestImageStorageS3CacheRespectsSignatureLifetime(t *testing.T) {
	s := &S3ImageStorage{}
	query := url.Values{"X-Amz-Date": {time.Now().UTC().Format("20060102T150405Z")}, "X-Amz-Expires": {"43200"}}
	imageURL := "https://s3.test/bucket/image.png?" + query.Encode()
	require.True(t, s.ValidateCachedURL(context.Background(), imageURL))
	require.InDelta(t, (12 * time.Hour).Seconds(), s.CacheTTL(imageURL, 7*24*time.Hour).Seconds(), 2)
	query.Set("X-Amz-Date", time.Now().Add(-13*time.Hour).UTC().Format("20060102T150405Z"))
	require.False(t, s.ValidateCachedURL(context.Background(), "https://s3.test/bucket/image.png?"+query.Encode()))
	require.Zero(t, s.CacheTTL("https://s3.test/bucket/image.png", time.Hour))
}
