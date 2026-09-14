# 图片 URL 代理到 CF R2 功能设计方案

## 版本信息
- **功能名称**: Image URL Proxy to CF R2
- **版本**: v1.0
- **创建日期**: 2026-09-14
- **作者**: xiaoling
- **状态**: 设计阶段

---

## 一、背景与问题

### 1.1 问题描述

**现象**：用户使用图片编辑功能时，上传的参考图片托管在中国区域对象存储（如火山引擎 TOS），导致 OpenAI 服务器访问超时。

**具体案例**：
- 用户 ID: 149
- 请求 ID: `736ca46b-12a7-4e5f-9a33-81059f05932c`
- 错误信息: 
  ```
  temporary image reference failure: fetch image 
  https://onlyupusers.tos-cn-shanghai.volces.com/users/.../ref/xxx.png?X-Tos-Signature=...
  context deadline exceeded (Client.Timeout or context cancellation while reading body)
  ```
- 状态码: 408 Request Timeout
- 超时时间: ~30秒

### 1.2 根本原因

1. **跨境网络访问问题**：OpenAI 的海外服务器访问中国区域对象存储时网络延迟过高或不可达
2. **签名 URL 限制**：Presigned URL 带有时效性签名参数，但网络问题导致无法在有效期内完成下载
3. **防火墙策略**：可能存在防火墙或网络策略限制

### 1.3 影响范围

- **受影响功能**：
  - OpenAI 图片编辑 (`/v1/images/edits`)
  - Grok 图片编辑 (`/v1/images/edits`)
  - Grok 视频生成（图生视频）(`/v1/videos/generations`)
  
- **受影响存储服务**：
  - 火山引擎 TOS (`*.volces.com`)
  - 阿里云 OSS (`*.aliyuncs.com`)
  - 腾讯云 COS (`*.myqcloud.com`)
  - 华为云 OBS (`*.myhuaweicloud.com`)

---

## 二、解决方案

### 2.1 方案概述

**核心思路**：在请求发送给 OpenAI/Grok 之前，拦截用户提供的图片 URL，判断是否需要代理，若需要则下载图片并上传到 Cloudflare R2，然后将请求中的 URL 替换为 CF R2 的 URL。

**方案优势**：
- ✅ 完全复用现有 CF R2 基础设施
- ✅ 用户无感知，自动处理
- ✅ CF 全球 CDN 加速，OpenAI 访问稳定
- ✅ Redis 缓存避免重复下载
- ✅ R2 生命周期自动清理（1天）

### 2.2 数据流

```
用户请求（带 TOS URL + 签名参数）
    ↓
解析请求，提取图片 URL
    ↓
判断是否需要代理（中国区存储 / 带签名参数）
    ↓
【需要代理】
    ├─ 检查 Redis 缓存
    │   ├─ 命中 → 直接使用缓存的 CF URL (0.01s)
    │   └─ 未命中 → 下载图片（保留签名参数）
    │                ↓
    │              上传到 CF R2
    │                ↓
    │              【成功】缓存 URL 映射 (7天)
    │                ↓
    │              替换为 CF R2 URL
    │
    │              【失败】下载失败 / 上传失败 / 超时
    │                ↓
    │              去掉签名参数，降级为干净 URL
    │                ↓
    │              记录 warn 日志（fallback_to_clean_url）
    ↓
【不需要代理】直接透传
    ↓
替换请求体中的 URL
    ↓
调用 OpenAI/Grok（使用 CF R2 URL 或降级后的干净 URL）
    ↓
OpenAI 从 CF CDN 下载（秒开）或尝试访问干净 URL
```

---

## 三、架构设计

### 3.1 核心组件

```
┌─────────────────────────────────────────────────────────────┐
│                    ImageURLProxy                            │
│  - 判断是否需要代理                                          │
│  - 下载远程图片                                              │
│  - 上传到 CF R2                                              │
│  - 缓存 URL 映射                                             │
│  - 功能开关控制                                              │
└─────────────────────────────────────────────────────────────┘
                          ↓
              使用现有的基础设施
                          ↓
    ┌──────────────┬──────────────┬──────────────┐
    ↓              ↓              ↓              ↓
ImageStorage   Redis Cache   HTTPClient    SettingRepo
(已有)         (已有)        (已有)        (已有)
```

### 3.2 代理判断逻辑

#### 需要代理的 URL

1. **中国区域对象存储**
   - `*.volces.com` (火山引擎 TOS)
   - `*.aliyuncs.com` (阿里云 OSS)
   - `*.myqcloud.com` (腾讯云 COS)
   - `*.myhuaweicloud.com` (华为云 OBS)

2. **带签名参数的 URL (Presigned URL)**
   - `X-Tos-Signature` (TOS)
   - `X-Amz-Signature` (AWS S3)
   - `OSSAccessKeyId` (阿里云)
   - `q-signature` (腾讯云)

#### 不需要代理的 URL

- `data:image/...` 格式 (内嵌 base64)
- 国际 CDN (Cloudflare、AWS CloudFront、Imgur 等)
- OpenAI/Grok 自己的域名

### 3.3 缓存策略

#### 缓存 Key 设计

```
原始 URL: https://tos.volces.com/users/xxx/ref/abc.png?X-Tos-Signature=...
                                                    ↓
去掉参数: https://tos.volces.com/users/xxx/ref/abc.png
                                                    ↓
SHA256:   sha256(clean_url) → hash
                                                    ↓
缓存 Key: "img_proxy:4a7f2e1b..."
                                                    ↓
缓存 Value: "https://r2.cloudflare.com/proxy/abc123.png"
```

**为什么去掉参数？**
- 同一张图片的签名参数会随时间变化（时间戳、过期时间）
- 去掉参数后可以命中缓存，避免重复下载同一张图片

#### 缓存时效

- **Redis TTL**: 7 天（与 CF R2 生命周期对齐）
- **R2 生命周期**: 1 天（已配置）

**注意**: Redis TTL > R2 TTL 是安全的，因为 R2 文件过期后访问会返回 404，下次请求时会自动重新代理上传。

---

## 四、配置设计

### 4.1 配置项

在 `config.yaml` 中新增配置：

```yaml
image_url_proxy:
  # 功能总开关（默认关闭，通过管理员系统设置开启）
  enabled: false
  
  # 超时配置（延长超时时间）
  download_timeout_seconds: 60     # 下载超时（原 30s → 60s）
  upload_timeout_seconds: 30       # 上传超时（原 10s → 30s）
  total_timeout_seconds: 100       # 总超时（原 45s → 100s）
  
  # 下载限制
  max_download_bytes: 20971520     # 单图最大 20MB
  
  # 缓存配置
  cache_ttl_hours: 168             # 缓存 7 天 (168小时)
  
  # 存储配置
  storage_key_prefix: "proxy/"     # R2 存储 key 前缀
  
  # 代理规则（白名单模式）
  whitelist_only: false            # 是否仅代理白名单域名
  domain_whitelist:                # 域名白名单
    - "*.volces.com"
    - "*.aliyuncs.com"
    - "*.myqcloud.com"
    - "*.myhuaweicloud.com"
```

### 4.2 管理员系统设置

在后台管理系统的"系统设置"模块中新增：

```json
{
  "key": "image_url_proxy_settings",
  "display_name": "图片 URL 代理设置",
  "category": "image_generation",
  "fields": {
    "enabled": {
      "type": "boolean",
      "label": "启用图片 URL 代理",
      "description": "开启后，系统会自动将中国区域对象存储的图片代理到 Cloudflare R2，解决 OpenAI 访问超时问题",
      "default": false
    },
    "download_timeout_seconds": {
      "type": "number",
      "label": "下载超时（秒）",
      "description": "从原始存储下载图片的超时时间",
      "default": 60,
      "min": 10,
      "max": 300
    },
    "upload_timeout_seconds": {
      "type": "number",
      "label": "上传超时（秒）",
      "description": "上传图片到 CF R2 的超时时间",
      "default": 30,
      "min": 5,
      "max": 120
    },
    "whitelist_only": {
      "type": "boolean",
      "label": "仅代理白名单域名",
      "description": "开启后只代理白名单中的域名，其他域名直接透传",
      "default": false
    }
  }
}
```

---

## 五、集成点设计

### 5.1 集成位置

**在 Service 层集成（推荐）**

位置：
- `backend/internal/service/openai_images.go` - OpenAI 图片请求
- `backend/internal/service/grok_media.go` - Grok 图片/视频请求

**集成时机**：在解析请求后、构建上游请求前

```
现有流程：
  ParseOpenAIImagesRequest(body)  → 解析出 InputImageURLs
                                        ↓
  BuildUpstreamRequest()          → 构建上游请求
                                        ↓
  SendToOpenAI()                  → 发送（超时！）

改进流程：
  ParseOpenAIImagesRequest(body)  → 解析出 InputImageURLs
                                        ↓
  【新增】ProxyImageURLs()        → 代理 URL 到 CF R2
                                        ↓
  BuildUpstreamRequest()          → 构建上游请求（使用 CF URL）
                                        ↓
  SendToOpenAI()                  → 发送（秒开！）
```

### 5.2 代码集成伪代码

#### OpenAI Images

```go
// openai_images.go
func (s *OpenAIGatewayService) forwardOpenAIImagesAPIKey(...) {
    // 1. 解析请求
    parsed, err := s.ParseOpenAIImagesRequest(c, body)
    if err != nil {
        return err
    }
    
    // 2. 【新增】代理图片 URL
    if s.imageURLProxy != nil && s.imageURLProxy.IsEnabled() {
        // 代理输入图片 URLs
        if len(parsed.InputImageURLs) > 0 {
            parsed.InputImageURLs, err = s.imageURLProxy.ProxyURLs(ctx, parsed.InputImageURLs)
            if err != nil {
                return fmt.Errorf("proxy input image urls: %w", err)
            }
        }
        
        // 代理 mask 图片 URL
        if parsed.MaskImageURL != "" {
            parsed.MaskImageURL, err = s.imageURLProxy.ProxyURL(ctx, parsed.MaskImageURL)
            if err != nil {
                return fmt.Errorf("proxy mask image url: %w", err)
            }
        }
    }
    
    // 3. 重建请求体（使用代理后的 URL）
    newBody, err := rebuildOpenAIImagesRequestBody(parsed)
    if err != nil {
        return err
    }
    
    // 4. 发送请求
    resp, err := s.sendToOpenAI(ctx, newBody)
    ...
}
```

#### Grok Media

```go
// grok_media.go
func (s *OpenAIGatewayService) forwardGrokMedia(...) {
    // 1. 解析请求
    info := ParseGrokMediaRequest(contentType, body)
    
    // 2. 【新增】代理图片 URL
    if s.imageURLProxy != nil && s.imageURLProxy.IsEnabled() {
        // 代理输入图片 URLs
        if len(info.InputImageURLs) > 0 {
            info.InputImageURLs, err = s.imageURLProxy.ProxyURLs(ctx, info.InputImageURLs)
            if err != nil {
                return fmt.Errorf("proxy input image urls: %w", err)
            }
        }
        
        // 代理 mask 图片 URL
        if info.MaskImageURL != "" {
            info.MaskImageURL, err = s.imageURLProxy.ProxyURL(ctx, info.MaskImageURL)
            if err != nil {
                return fmt.Errorf("proxy mask image url: %w", err)
            }
        }
    }
    
    // 3. 重建请求体（使用代理后的 URL）
    newBody, err := rebuildGrokMediaRequestBody(info, contentType, body)
    if err != nil {
        return err
    }
    
    // 4. 发送请求
    resp, err := s.sendToGrok(ctx, newBody)
    ...
}
```

---

## 六、降级与容错策略

### 6.1 功能开关

- **配置开关**: `image_url_proxy.enabled` (默认 `false`)
- **动态开关**: 管理员系统设置中可实时开关
- **降级策略**: 开关关闭时直接透传原 URL

### 6.2 容错策略（更新：CF 失败降级）

| 失败场景 | 处理策略 | 降级行为 |
|---------|---------|---------|
| **功能未启用** | 直接透传原 URL | 保持现状 |
| **ImageStorage 未配置** | 直接透传原 URL | 保持现状 |
| **下载失败**（TOS 超时/403） | 去掉签名参数后降级透传 | ✅ 降级到干净 URL |
| **上传失败**（CF R2 故障） | 去掉签名参数后降级透传 | ✅ 降级到干净 URL |
| **Redis 读取失败** | 跳过缓存，直接下载+上传 | ⚠️ 降级到无缓存模式 |
| **Redis 写入失败** | 记录警告日志，继续流程 | ⚠️ 降级到无缓存模式 |
| **超时** | 去掉签名参数后降级透传 | ✅ 降级到干净 URL |

#### 降级逻辑说明

**为什么去掉签名参数？**
1. 签名参数可能导致 OpenAI 解析失败
2. 如果对象存储设置了公开读，去掉参数后 OpenAI 仍可能访问成功
3. 即使失败，也给 OpenAI 一次尝试的机会

**降级 URL 处理**：
```
原始 URL: https://tos.volces.com/xxx.png?X-Tos-Signature=...&X-Tos-Date=...
                                    ↓
去掉参数: https://tos.volces.com/xxx.png
                                    ↓
传给 OpenAI: 让 OpenAI 尝试访问（可能成功，可能失败）
```

### 6.3 超时配置（延长版）

```go
const (
    // 下载超时：60秒（延长，应对慢速网络）
    defaultProxyDownloadTimeout = 60 * time.Second
    
    // 上传超时：30秒（延长，应对大文件）
    defaultProxyUploadTimeout = 30 * time.Second
    
    // 总超时：100秒（下载 + 上传 + 缓冲）
    defaultProxyTotalTimeout = 100 * time.Second
    
    // 单图最大下载：20MB
    defaultProxyMaxDownloadBytes = 20 << 20
)
```

---

## 七、性能优化

### 7.1 首次请求性能

**时间分解**：
```
┌─────────────────────────────────────────────────────────┐
│  检查缓存: 0.01s                                         │
│  未命中 ↓                                                │
│  下载 TOS 图片: 5-10s (视网络而定)                       │
│  上传 CF R2: 2-5s (视图片大小)                           │
│  写缓存: 0.01s                                           │
│  ─────────────────────────────────                      │
│  总计: 7-15s                                             │
└─────────────────────────────────────────────────────────┘
```

### 7.2 缓存命中性能

**时间分解**：
```
┌─────────────────────────────────────────────────────────┐
│  检查缓存: 0.01s                                         │
│  命中 ↓                                                  │
│  返回 CF URL: 0.001s                                     │
│  ─────────────────────────────────                      │
│  总计: 0.01s                                             │
└─────────────────────────────────────────────────────────┘
```

### 7.3 缓存命中率预估

假设：
- 用户平均每张图片编辑 3 次
- 缓存有效期 7 天
- 用户在 7 天内重复使用同一张图片

**预期缓存命中率**: 60-70%

---

## 八、成本分析

### 8.1 流量成本

**假设**：
- 平均图片大小：3MB
- 每天 1000 次图片编辑请求
- 缓存命中率：60%

**每月流量**：
- 下载 TOS → 你的服务器：400 次 × 3MB × 30 天 = 36GB（入站流量，通常免费）
- 上传到 CF R2：400 次 × 3MB × 30 天 = 36GB（R2 无入站费用，免费）
- OpenAI 访问 CF R2：1000 次 × 3MB × 30 天 = 90GB（前 10GB 免费，超出 $0.36/TB ≈ $0.03/月）

**月成本**: 基本免费（~$0.03）

### 8.2 存储成本

**假设**：
- R2 生命周期：1 天自动清理
- 平均每天上传 400 张图片
- 平均图片大小：3MB

**存储占用**：400 张 × 3MB = 1.2GB

**月成本**: 免费（前 10GB 免费）

### 8.3 Redis 成本

**假设**：
- 每个 URL 映射：200 字节
- 缓存 1 万个 URL

**内存占用**：10,000 × 200B = 2MB

**月成本**: 忽略不计

---

## 九、监控与日志

### 9.1 关键指标

```yaml
指标名称:
  # 代理成功率
  - image_proxy_success_rate
    标签: [endpoint, domain]
  
  # 代理失败率
  - image_proxy_error_rate
    标签: [endpoint, domain, error_type]
  
  # 缓存命中率
  - image_proxy_cache_hit_rate
  
  # 平均下载时间
  - image_proxy_download_duration_seconds
    标签: [domain]
  
  # 平均上传时间
  - image_proxy_upload_duration_seconds
  
  # 代理的图片大小分布
  - image_proxy_size_bytes_histogram
  
  # 按域名统计代理次数
  - image_proxy_by_domain_total
    标签: [domain]
  
  # 超时次数
  - image_proxy_timeout_total
    标签: [phase] # download/upload
  
  # 降级次数
  - image_proxy_fallback_total
    标签: [reason] # download_failed/upload_failed/timeout
```

### 9.2 日志规范

#### 日志级别分类

| 级别 | 使用场景 |
|-----|---------|
| **debug** | 缓存命中、跳过代理、开关未启用 |
| **info** | 代理成功、降级透传 |
| **warn** | Redis 故障、部分失败 |
| **error** | 下载失败、上传失败、超时 |

#### 日志字段规范

所有代理相关日志必须包含以下基础字段：

```json
{
  "request_id": "uuid",
  "user_id": 123,
  "original_url": "https://...",
  "original_host": "tos-cn-shanghai.volces.com"
}
```

### 9.3 日志格式

### 9.3 日志格式

#### 功能开关未启用

```json
{
  "level": "debug",
  "msg": "image_url_proxy.disabled",
  "request_id": "xxx"
}
```

#### 跳过代理（不需要代理的 URL）

```json
{
  "level": "debug",
  "msg": "image_url_proxy.skipped",
  "request_id": "xxx",
  "original_url": "https://example.com/image.png",
  "reason": "not_china_region" // or "no_signature" or "data_uri"
}
```

#### 缓存命中日志

```json
{
  "level": "debug",
  "msg": "image_url_proxy.cache_hit",
  "request_id": "xxx",
  "cache_key": "img_proxy:4a7f...",
  "cf_url": "https://r2.../proxy/4a7f...png",
  "duration_ms": 10
}
```

#### 缓存未命中，开始代理

```json
{
  "level": "info",
  "msg": "image_url_proxy.cache_miss_start_proxy",
  "request_id": "xxx",
  "user_id": 149,
  "original_host": "tos-cn-shanghai.volces.com",
  "original_url": "https://tos...png?X-Tos-Signature=..."
}
```

#### 下载开始

```json
{
  "level": "debug",
  "msg": "image_url_proxy.download_start",
  "request_id": "xxx",
  "original_url": "https://tos...png?X-Tos-Signature=...",
  "timeout_seconds": 60
}
```

#### 下载成功

```json
{
  "level": "info",
  "msg": "image_url_proxy.download_success",
  "request_id": "xxx",
  "original_host": "tos-cn-shanghai.volces.com",
  "bytes": 3245678,
  "duration_ms": 5200,
  "content_type": "image/png"
}
```

#### 下载失败（触发降级）

```json
{
  "level": "error",
  "msg": "image_url_proxy.download_failed",
  "request_id": "xxx",
  "user_id": 149,
  "original_url": "https://tos.../xxx.png?X-Tos-Signature=...",
  "original_host": "tos-cn-shanghai.volces.com",
  "error": "context deadline exceeded",
  "error_type": "timeout",
  "duration_ms": 60000
}
```

#### 上传开始

```json
{
  "level": "debug",
  "msg": "image_url_proxy.upload_start",
  "request_id": "xxx",
  "bytes": 3245678,
  "timeout_seconds": 30
}
```

#### 上传成功

```json
{
  "level": "info",
  "msg": "image_url_proxy.upload_success",
  "request_id": "xxx",
  "cf_key": "proxy/4a7f...png",
  "cf_url": "https://r2.../proxy/4a7f...png",
  "bytes": 3245678,
  "duration_ms": 2800
}
```

#### 上传失败（触发降级）

```json
{
  "level": "error",
  "msg": "image_url_proxy.upload_failed",
  "request_id": "xxx",
  "user_id": 149,
  "original_host": "tos-cn-shanghai.volces.com",
  "cf_key": "proxy/4a7f...png",
  "bytes": 3245678,
  "error": "connection reset by peer",
  "error_type": "network",
  "duration_ms": 15000
}
```

#### 代理成功（完整流程）

```json
{
  "level": "info",
  "msg": "image_url_proxy.success",
  "request_id": "736ca46b-12a7-4e5f-9a33-81059f05932c",
  "user_id": 149,
  "original_host": "tos-cn-shanghai.volces.com",
  "original_url": "https://tos...png?X-Tos-Signature=...",
  "cf_url": "https://r2.../proxy/4a7f...png",
  "bytes": 3245678,
  "download_ms": 5200,
  "upload_ms": 2800,
  "cache_hit": false,
  "total_ms": 8050
}
```

#### 降级透传（去掉参数）

```json
{
  "level": "warn",
  "msg": "image_url_proxy.fallback_to_clean_url",
  "request_id": "xxx",
  "user_id": 149,
  "original_url": "https://tos...png?X-Tos-Signature=...",
  "clean_url": "https://tos...png",
  "reason": "download_failed", // or "upload_failed" or "timeout"
  "original_error": "context deadline exceeded"
}
```

#### Redis 缓存写入失败

```json
{
  "level": "warn",
  "msg": "image_url_proxy.cache_write_failed",
  "request_id": "xxx",
  "cache_key": "img_proxy:4a7f...",
  "error": "redis connection timeout"
}
```

#### Redis 缓存读取失败

```json
{
  "level": "warn",
  "msg": "image_url_proxy.cache_read_failed",
  "request_id": "xxx",
  "cache_key": "img_proxy:4a7f...",
  "error": "redis connection refused"
}
```

#### 总超时

```json
{
  "level": "error",
  "msg": "image_url_proxy.total_timeout",
  "request_id": "xxx",
  "user_id": 149,
  "original_host": "tos-cn-shanghai.volces.com",
  "timeout_seconds": 100,
  "elapsed_ms": 100500,
  "phase": "upload" // or "download"
}
```

#### 图片超过大小限制

```json
{
  "level": "error",
  "msg": "image_url_proxy.size_limit_exceeded",
  "request_id": "xxx",
  "original_host": "tos-cn-shanghai.volces.com",
  "bytes": 25000000,
  "limit_bytes": 20971520
}
```

---

## 十、测试计划

### 10.1 单元测试

```
测试项:
  1. ✅ shouldProxy() - 判断逻辑
     - 中国区域存储域名
     - 带签名参数的 URL
     - 不需要代理的 URL (data:, 国际 CDN)
  
  2. ✅ downloadImage() - 下载逻辑
     - 成功下载
     - 超时
     - 403 错误
     - 超过大小限制
  
  3. ✅ uploadToR2() - 上传逻辑
     - 成功上传
     - 上传失败
  
  4. ✅ buildCacheKey() - 缓存 key 生成
     - 去掉查询参数
     - hash 稳定性
  
  5. ✅ ProxyURL() - 完整流程
     - 缓存命中
     - 缓存未命中
     - 功能开关关闭
```

### 10.2 集成测试

```
测试项:
  1. ✅ OpenAI Images Edit - 真实 TOS URL
     - 单张图片
     - 多张图片
     - 带 mask 的图片
  
  2. ✅ Grok Images Edit - 真实 TOS URL
     - 单张图片
     - 多张图片
  
  3. ✅ Grok Video Generation - 图生视频
     - 参考图片代理
  
  4. ✅ 缓存验证
     - 第一次请求未命中
     - 第二次请求命中
  
  5. ✅ 错误处理
     - 下载超时
     - 上传失败
     - Redis 故障降级
```

### 10.3 性能测试

```
测试项:
  1. ✅ 首次代理耗时
     - 3MB 图片: < 15s
     - 10MB 图片: < 30s
  
  2. ✅ 缓存命中耗时
     - < 50ms
  
  3. ✅ 并发性能
     - 10 个并发请求
     - 50 个并发请求
```

---

## 十一、实施计划

### 阶段 1: 核心功能开发 (5-7 小时)

**任务**：
1. ✅ 创建 `ImageURLProxy` 服务 (1.5h)
   - 判断逻辑实现
   - 下载+上传逻辑
   - 缓存集成
   - **降级逻辑（去掉参数）**
   - **完整日志埋点**

2. ✅ 配置管理 (1h)
   - 添加配置项到 `config.go`
   - 实现系统设置接口
   - 功能开关控制

3. ✅ 集成到网关 (2h)
   - OpenAI Images 集成
   - Grok Media 集成
   - 请求体重建逻辑

4. ✅ 单元测试 (1.5h)
   - 测试降级逻辑
   - 测试日志输出

### 阶段 2: 管理后台界面 (2 小时)

**任务**：
1. ✅ 后台设置页面 (1h)
   - 功能开关 UI
   - 超时配置 UI
   - 白名单配置 UI

2. ✅ 设置持久化 (0.5h)
   - 数据库表设计
   - API 接口

3. ✅ 测试连接功能 (0.5h)
   - 测试下载
   - 测试上传
   - 测试 Redis

### 阶段 3: 监控与日志 (2 小时)

**任务**：
1. ✅ 日志集成 (1h)
   - 结构化日志
   - 关键步骤记录（缓存命中/未命中、下载开始/成功/失败、上传开始/成功/失败、降级透传）
   - 错误分类（timeout/network/permission/size_limit）
   - 日志级别分类（debug/info/warn/error）

2. ✅ 监控指标 (0.5h)
   - Prometheus 指标
   - 降级计数指标（image_proxy_fallback_total）
   - Grafana 面板

3. ✅ 告警规则 (0.5h)
   - 失败率告警
   - 超时告警
   - 降级率告警

### 阶段 4: 集成测试与部署 (2 小时)

**任务**：
1. ✅ 集成测试 (1h)
   - 真实 TOS URL 测试
   - 多场景验证

2. ✅ 灰度发布 (0.5h)
   - 小流量测试
   - 监控观察

3. ✅ 文档更新 (0.5h)
   - 用户文档
   - 运维文档

**总计**: 10-13 小时

---

## 十二、风险与挑战

### 12.1 技术风险

| 风险 | 概率 | 影响 | 缓解措施 |
|-----|------|------|---------|
| **CF R2 故障** | 低 | 高 | 功能开关快速关闭，降级到原 URL |
| **下载超时** | 中 | 中 | 延长超时时间到 60s，优化重试逻辑 |
| **Redis 故障** | 低 | 低 | 降级到无缓存模式，直接代理 |
| **内存占用过高** | 低 | 中 | 限制并发数，流式处理大文件 |
| **代理成本超预期** | 低 | 低 | 监控用量，设置告警阈值 |

### 12.2 业务风险

| 风险 | 概率 | 影响 | 缓解措施 |
|-----|------|------|---------|
| **首次延迟影响用户体验** | 高 | 中 | 缓存命中率优化，告知用户首次较慢 |
| **误代理不需要代理的 URL** | 低 | 低 | 白名单模式，精确匹配规则 |
| **缓存污染** | 低 | 低 | 缓存 key 设计合理，TTL 控制 |

---

## 十三、Q&A

### Q1: 是否需要支持 multipart 请求？

**A**: 需要！

**理由**：
- 用户可能通过 multipart 上传文件，但同时也可能传 URL
- multipart 中的 `file` 字段：不代理（已经是文件内容）
- multipart 中的 `image`/`mask` URL 字段：需要代理

### Q2: 如果用户传了 base64，需要代理吗？

**A**: 不需要！

**理由**：
- base64 直接内嵌在请求中，OpenAI 不需要下载
- 代理 base64 反而会浪费资源

### Q3: 代理失败是否降级到原 URL？

**A**: 降级！（已更新）

**理由**：
- 如果直接返回错误，用户体验差
- 降级到去掉参数的干净 URL，给 OpenAI 一次尝试机会
- 如果对象存储设置了公开读，OpenAI 可能访问成功
- 即使仍然超时，也比直接失败更友好

**降级策略**：
```
下载失败/上传失败/超时
        ↓
去掉签名参数
        ↓
https://tos.volces.com/xxx.png（干净 URL）
        ↓
传给 OpenAI/Grok
        ↓
记录 warn 日志（image_url_proxy.fallback_to_clean_url）
```

### Q4: 是否需要支持视频 URL 代理？

**A**: 需要！

**理由**：
- Grok 支持视频生成（图生视频）
- 视频 URL 也可能是中国区存储
- 使用同一套逻辑，只是文件更大，注意超时配置

### Q5: 如果 CF R2 配置未启用怎么办？

**A**: 直接透传原 URL

**理由**：
- 功能未配置时保持现有行为
- 避免影响其他功能

### Q6: 是否需要支持代理失败重试？

**A**: 暂不支持，直接返回错误

**理由**：
- 下载/上传失败通常是网络问题或权限问题，重试成功率低
- 重试会增加延迟
- 用户可以手动重试整个请求

### Q7: 缓存 key 冲突怎么办？

**A**: 使用 SHA256 hash，冲突概率极低

**理由**：
- SHA256 冲突概率 < 1 / 2^256
- 即使冲突，也只是返回了另一张图片的 URL，不会影响系统稳定性

---

## 十四、总结

### 14.1 方案优势

1. ✅ **完全复用现有基础设施**：无需引入新的依赖或服务
2. ✅ **用户无感知**：自动判断并代理，无需用户改代码
3. ✅ **性能优化**：Redis 缓存 + CF CDN 加速
4. ✅ **成本可控**：CF R2 前 10GB 免费，1 天自动清理
5. ✅ **可观测性强**：完善的日志和监控
6. ✅ **灵活可控**：功能开关、超时配置、白名单模式

### 14.2 预期效果

- **成功率提升**：从 ~70% 提升到 ~95%（消除超时问题）
- **平均延迟**：
  - 首次请求：+7-15s（下载+上传）
  - 缓存命中：+0.01s（几乎无感知）
- **缓存命中率**：60-70%
- **成本增加**：~$0.03/月（几乎可忽略）

### 14.3 后续优化方向

1. **智能缓存预热**：分析高频图片，提前代理
2. **并发优化**：支持多图片并发代理
3. **压缩优化**：上传前压缩图片（可选）
4. **多区域支持**：根据用户地理位置选择最近的 R2 区域
5. **统计分析**：提供代理使用报表，辅助决策
6. **降级监控**：分析降级成功率，评估是否需要调整策略

---

## 附录

### A. 变更记录

| 版本 | 日期 | 作者 | 变更内容 |
|-----|------|------|---------|
| v1.0 | 2026-09-14 | xiaoling | 初始设计 |
| v1.1 | 2026-09-14 | xiaoling | 新增：降级策略（CF 失败时去掉参数透传）+ 详细日志规范 |

### B. 参考资料

- [Cloudflare R2 文档](https://developers.cloudflare.com/r2/)
- [AWS S3 Presigned URL](https://docs.aws.amazon.com/AmazonS3/latest/userguide/PresignedUrlUploadObject.html)
- [火山引擎 TOS 文档](https://www.volcengine.com/docs/6349/74841)

### C. 相关文件

- 配置文件: `backend/internal/config/config.go`
- 核心实现: `backend/internal/service/image_url_proxy.go`
- OpenAI 集成: `backend/internal/service/openai_images.go`
- Grok 集成: `backend/internal/service/grok_media.go`
- 系统设置: `backend/internal/handler/admin_setting.go`

### D. 关键决策记录

| 决策点 | 选择方案 | 理由 |
|-------|---------|------|
| **缓存 Key 设计** | 使用去掉参数的 URL 的 SHA256 | 签名参数会随时间变化，去掉后可复用缓存 |
| **CF 失败处理** | 降级到去掉参数的干净 URL | 给 OpenAI 一次尝试机会，可能成功访问公开存储 |
| **Redis 故障处理** | 降级到无缓存模式 | 不影响核心功能，仅性能下降 |
| **超时配置** | 下载60s/上传30s/总100s | 考虑跨境网络延迟和大文件传输 |
| **缓存 TTL** | Redis 7天 > R2 1天 | Redis 失效后自动重新代理，安全且简单 |
- 系统设置: `backend/internal/handler/admin_setting.go`

### C. 变更记录

| 版本 | 日期 | 作者 | 变更内容 |
|-----|------|------|---------|
| v1.0 | 2026-09-14 | xiaoling | 初始设计 |
