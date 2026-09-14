package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// ImageURLProxy 图片 URL 代理服务
type ImageURLProxy struct {
	cfg          *config.ImageURLProxyConfig
	imageStorage ImageStorage
	redisCache   RedisCache
	httpClient   *http.Client
}

// NewImageURLProxy 创建图片 URL 代理服务
func NewImageURLProxy(
	cfg *config.ImageURLProxyConfig,
	imageStorage ImageStorage,
	redisCache RedisCache,
) *ImageURLProxy {
	if cfg == nil {
		return nil
	}

	downloadTimeout := time.Duration(cfg.DownloadTimeoutSeconds) * time.Second
	uploadTimeout := time.Duration(cfg.UploadTimeoutSeconds) * time.Second
	if downloadTimeout <= 0 {
		downloadTimeout = 60 * time.Second
	}
	if uploadTimeout <= 0 {
		uploadTimeout = 30 * time.Second
	}

	return &ImageURLProxy{
		cfg:          cfg,
		imageStorage: imageStorage,
		redisCache:   redisCache,
		httpClient: &http.Client{
			Timeout: downloadTimeout,
		},
	}
}

// IsEnabled 检查功能是否启用
func (p *ImageURLProxy) IsEnabled() bool {
	if p == nil || p.cfg == nil {
		return false
	}
	return p.cfg.Enabled
}

// ProxyURL 代理单个图片 URL
func (p *ImageURLProxy) ProxyURL(ctx context.Context, imageURL string) (string, error) {
	if !p.IsEnabled() {
		return imageURL, nil
	}

	if imageURL == "" {
		return imageURL, nil
	}

	// 判断是否需要代理
	needsProxy, reason := p.shouldProxy(imageURL)
	if !needsProxy {
		logger.L().Debug("image_url_proxy.skip",
			zap.String("url", imageURL),
			zap.String("reason", reason),
		)
		return imageURL, nil
	}

	logger.L().Debug("image_url_proxy.start",
		zap.String("url", imageURL),
		zap.String("reason", reason),
	)

	// 检查缓存
	cacheKey := p.buildCacheKey(imageURL)
	if p.redisCache != nil {
		cachedURL, err := p.redisCache.Get(ctx, cacheKey)
		if err == nil && cachedURL != "" {
			logger.L().Debug("image_url_proxy.cache_hit",
				zap.String("cache_key", cacheKey),
				zap.String("cached_url", cachedURL),
			)
			return cachedURL, nil
		}
		logger.L().Debug("image_url_proxy.cache_miss",
			zap.String("cache_key", cacheKey),
		)
	}

	// 下载图片
	downloadStart := time.Now()
	imageData, contentType, err := p.downloadImage(ctx, imageURL)
	downloadDuration := time.Since(downloadStart)
	if err != nil {
		logger.L().Warn("image_url_proxy.download_failed",
			zap.String("url", imageURL),
			zap.Error(err),
			zap.Duration("duration", downloadDuration),
		)
		// 降级：去掉签名参数后透传
		return p.fallbackToCleanURL(imageURL, "download_failed"), nil
	}

	logger.L().Debug("image_url_proxy.download_success",
		zap.String("url", imageURL),
		zap.Int("bytes", len(imageData)),
		zap.String("content_type", contentType),
		zap.Duration("duration", downloadDuration),
	)

	// 上传到 CF R2
	uploadStart := time.Now()
	cfURL, err := p.uploadToR2(ctx, imageData, contentType)
	uploadDuration := time.Since(uploadStart)
	if err != nil {
		logger.L().Warn("image_url_proxy.upload_failed",
			zap.String("url", imageURL),
			zap.Error(err),
			zap.Duration("duration", uploadDuration),
		)
		// 降级：去掉签名参数后透传
		return p.fallbackToCleanURL(imageURL, "upload_failed"), nil
	}

	logger.L().Info("image_url_proxy.success",
		zap.String("original_url", imageURL),
		zap.String("cf_url", cfURL),
		zap.Int("bytes", len(imageData)),
		zap.Duration("download_duration", downloadDuration),
		zap.Duration("upload_duration", uploadDuration),
		zap.Duration("total_duration", downloadDuration+uploadDuration),
	)

	// 写入缓存
	if p.redisCache != nil {
		cacheTTL := time.Duration(p.cfg.CacheTTLHours) * time.Hour
		if cacheTTL <= 0 {
			cacheTTL = 168 * time.Hour // 默认 7 天
		}
		if err := p.redisCache.Set(ctx, cacheKey, cfURL, cacheTTL); err != nil {
			logger.L().Warn("image_url_proxy.cache_write_failed",
				zap.String("cache_key", cacheKey),
				zap.Error(err),
			)
		}
	}

	return cfURL, nil
}

// ProxyURLs 批量代理图片 URL
func (p *ImageURLProxy) ProxyURLs(ctx context.Context, imageURLs []string) ([]string, error) {
	if len(imageURLs) == 0 {
		return imageURLs, nil
	}

	result := make([]string, len(imageURLs))
	for i, imageURL := range imageURLs {
		proxiedURL, err := p.ProxyURL(ctx, imageURL)
		if err != nil {
			return nil, fmt.Errorf("proxy url[%d]: %w", i, err)
		}
		result[i] = proxiedURL
	}
	return result, nil
}

// shouldProxy 判断 URL 是否需要代理
func (p *ImageURLProxy) shouldProxy(imageURL string) (bool, string) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return false, "empty_url"
	}

	// data:image/... 不需要代理
	if strings.HasPrefix(strings.ToLower(imageURL), "data:image/") {
		return false, "data_uri"
	}

	parsedURL, err := url.Parse(imageURL)
	if err != nil {
		return false, "invalid_url"
	}

	host := strings.ToLower(parsedURL.Host)

	// 检查是否是中国区域对象存储域名
	chinaRegionDomains := []string{
		".volces.com",      // 火山引擎 TOS
		".aliyuncs.com",    // 阿里云 OSS
		".myqcloud.com",    // 腾讯云 COS
		".myhuaweicloud.com", // 华为云 OBS
	}

	for _, domain := range chinaRegionDomains {
		if strings.Contains(host, domain) {
			return true, "china_region_storage"
		}
	}

	// 检查是否带签名参数
	query := parsedURL.Query()
	signatureParams := []string{
		"X-Tos-Signature",   // TOS
		"X-Amz-Signature",   // AWS S3
		"OSSAccessKeyId",    // 阿里云 OSS
		"q-signature",       // 腾讯云 COS
	}

	for _, param := range signatureParams {
		if query.Get(param) != "" {
			return true, "presigned_url"
		}
	}

	// 白名单模式检查
	if p.cfg.WhitelistOnly {
		for _, pattern := range p.cfg.DomainWhitelist {
			if matchDomainPattern(host, pattern) {
				return true, "whitelist_match"
			}
		}
		return false, "not_in_whitelist"
	}

	return false, "no_proxy_needed"
}

// buildCacheKey 构建缓存 Key
func (p *ImageURLProxy) buildCacheKey(imageURL string) string {
	// 去掉查询参数，只对路径部分计算 hash
	cleanURL := imageURL
	if idx := strings.Index(imageURL, "?"); idx != -1 {
		cleanURL = imageURL[:idx]
	}

	hash := sha256.Sum256([]byte(cleanURL))
	return "img_proxy:" + hex.EncodeToString(hash[:])
}

// downloadImage 下载图片
func (p *ImageURLProxy) downloadImage(ctx context.Context, imageURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// 限制最大下载大小
	maxBytes := int64(p.cfg.MaxDownloadBytes)
	if maxBytes <= 0 {
		maxBytes = 20 << 20 // 默认 20MB
	}

	limitedReader := io.LimitReader(resp.Body, maxBytes+1)
	imageData, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, "", fmt.Errorf("read response body: %w", err)
	}

	if int64(len(imageData)) > maxBytes {
		return nil, "", fmt.Errorf("image size exceeds limit: %d > %d", len(imageData), maxBytes)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return imageData, contentType, nil
}

// uploadToR2 上传图片到 CF R2
func (p *ImageURLProxy) uploadToR2(ctx context.Context, imageData []byte, contentType string) (string, error) {
	if p.imageStorage == nil {
		return "", fmt.Errorf("image storage not configured")
	}

	// 生成唯一的存储 key
	hash := sha256.Sum256(imageData)
	filename := hex.EncodeToString(hash[:]) + guessFileExtension(contentType)
	storageKey := p.cfg.StorageKeyPrefix + filename

	// 上传到 R2
	uploadCtx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.UploadTimeoutSeconds)*time.Second)
	defer cancel()

	cfURL, err := p.imageStorage.Upload(uploadCtx, storageKey, imageData, contentType)
	if err != nil {
		return "", fmt.Errorf("upload to r2: %w", err)
	}

	return cfURL, nil
}

// fallbackToCleanURL 降级到干净的 URL（去掉签名参数）
func (p *ImageURLProxy) fallbackToCleanURL(imageURL string, reason string) string {
	cleanURL := imageURL
	if idx := strings.Index(imageURL, "?"); idx != -1 {
		cleanURL = imageURL[:idx]
	}

	logger.L().Warn("image_url_proxy.fallback_to_clean_url",
		zap.String("original_url", imageURL),
		zap.String("clean_url", cleanURL),
		zap.String("reason", reason),
	)

	return cleanURL
}

// matchDomainPattern 匹配域名模式（支持通配符）
func matchDomainPattern(host string, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))

	if pattern == host {
		return true
	}

	// 支持 *.example.com 格式
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // 包含前导点，如 ".example.com"
		return strings.HasSuffix(host, suffix)
	}

	return false
}

// guessFileExtension 根据 Content-Type 猜测文件扩展名
func guessFileExtension(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	contentType = strings.Split(contentType, ";")[0] // 去掉参数部分

	switch contentType {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "image/bmp":
		return ".bmp"
	default:
		return ".bin"
	}
}
