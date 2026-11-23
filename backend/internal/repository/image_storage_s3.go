package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/imagetiming"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// S3ImageStorage 用 S3 兼容对象存储实现 service.ImageStorage。
type S3ImageStorage struct {
	client         *s3.Client
	bucket         string
	publicBaseURL  string
	presignExpiry  time.Duration
	cacheNamespace string
}

var _ service.ImageStorage = (*S3ImageStorage)(nil)

// NewS3ImageStorage 依据配置构造 S3 图片存储（调用方应先确认 cfg.Active()）。
func NewS3ImageStorage(ctx context.Context, cfg *config.ImageStorageConfig) (*S3ImageStorage, error) {
	client, err := newS3Client(ctx, s3ClientParams{
		Endpoint:        cfg.Endpoint,
		Region:          cfg.Region,
		AccessKeyID:     cfg.AccessKeyID,
		SecretAccessKey: cfg.SecretAccessKey,
		ForcePathStyle:  cfg.ForcePathStyle,
	})
	if err != nil {
		return nil, err
	}

	expiry := time.Duration(cfg.PresignExpiry) * time.Hour
	if expiry <= 0 {
		expiry = 24 * time.Hour
	}
	fingerprint := sha256.Sum256([]byte(strings.Join([]string{cfg.Endpoint, cfg.Region, cfg.Bucket, cfg.PublicBaseURL, cfg.AccessKeyID, cfg.SecretAccessKey, cfg.Prefix, strconv.Itoa(cfg.PresignExpiry)}, "\x00")))

	return &S3ImageStorage{
		client:         client,
		bucket:         cfg.Bucket,
		publicBaseURL:  strings.TrimRight(cfg.PublicBaseURL, "/"),
		presignExpiry:  expiry,
		cacheNamespace: hex.EncodeToString(fingerprint[:]),
	}, nil
}

func (s *S3ImageStorage) CacheNamespace() string { return s.cacheNamespace }

func (s *S3ImageStorage) CacheTTL(imageURL string, requested time.Duration) time.Duration {
	if s.publicBaseURL != "" {
		return requested
	}
	u, err := url.Parse(imageURL)
	if err != nil {
		return 0
	}
	created, err := time.Parse("20060102T150405Z", u.Query().Get("X-Amz-Date"))
	if err != nil {
		return 0
	}
	seconds, err := strconv.ParseInt(u.Query().Get("X-Amz-Expires"), 10, 64)
	if err != nil || seconds < 1 || seconds > 604800 {
		return 0
	}
	return min(requested, time.Until(created.Add(time.Duration(seconds)*time.Second)))
}

func (s *S3ImageStorage) ValidateCachedURL(_ context.Context, imageURL string) bool {
	if s.publicBaseURL != "" {
		return strings.HasPrefix(imageURL, s.publicBaseURL+"/")
	}
	return s.CacheTTL(imageURL, time.Minute) >= time.Minute
}

// Save 上传图片字节，返回可访问 URL：配了 public_base_url 则返回公开直链，否则返回 presigned 临时链接。
func (s *S3ImageStorage) Save(ctx context.Context, key, contentType string, data []byte) (string, error) {
	phase := imagetiming.Start(ctx, "storage_put_object", zap.Int("body_bytes", len(data)))
	finish := servertiming.ObserveDependency(ctx, "s3")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        bytes.NewReader(data),
		ContentType: &contentType,
	})
	finish()
	phase.End(err)
	if err != nil {
		return "", fmt.Errorf("S3 PutObject: %w", err)
	}

	if s.publicBaseURL != "" {
		return s.publicBaseURL + "/" + strings.TrimLeft(key, "/"), nil
	}

	presignClient := s3.NewPresignClient(s.client)
	presignTiming := imagetiming.Start(ctx, "storage_presign")
	result, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(s.presignExpiry))
	presignTiming.End(err)
	if err != nil {
		return "", fmt.Errorf("presign url: %w", err)
	}
	return result.URL, nil
}
