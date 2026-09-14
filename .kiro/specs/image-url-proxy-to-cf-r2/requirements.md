# 图片 URL 代理到 CF R2 - 需求文档

## 版本信息
- **功能名称**: Image URL Proxy to CF R2
- **版本**: v1.0
- **创建日期**: 2026-09-14
- **作者**: xiaoling
- **状态**: 需求确认

---

## 一、背景与问题

### 1.1 业务背景

用户使用图片编辑功能（OpenAI Images Edit / Grok Images Edit）时，需要上传参考图片。这些图片通常托管在中国区域的对象存储服务（如火山引擎 TOS、阿里云 OSS）。

### 1.2 核心问题

**问题描述**：OpenAI/Grok 的海外服务器访问中国区域对象存储时出现超时。

**具体案例**：
- 用户 ID: 149
- 请求 ID: `736ca46b-12a7-4e5f-9a33-81059f05932c`
- 错误信息: `context deadline exceeded (Client.Timeout or context cancellation while reading body)`
- 状态码: 408 Request Timeout
- 超时时间: ~30秒

**根本原因**：
1. 跨境网络访问延迟过高或不可达
2. Presigned URL 带有时效性签名参数，但网络问题导致无法在有效期内完成下载
3. 可能存在防火墙或网络策略限制

### 1.3 影响范围

**受影响功能**：
- OpenAI 图片编辑 (`/v1/images/edits`)
- Grok 图片编辑 (`/v1/images/edits`)
- Grok 视频生成（图生视频）(`/v1/videos/generations`)

**受影响存储服务**：
- 火山引擎 TOS (`*.volces.com`)
- 阿里云 OSS (`*.aliyuncs.com`)
- 腾讯云 COS (`*.myqcloud.com`)
- 华为云 OBS (`*.myhuaweicloud.com`)

---

## 二、解决方案需求

### 2.1 核心需求

**自动代理机制**：
- 判断用户提供的图片 URL 是否需要代理（中国区存储/带签名参数）
- 下载图片到我方服务器
- 上传到 Cloudflare R2（全球 CDN）
- 将请求中的原始 URL 替换为 CF R2 URL
- 对用户透明，无需改造客户端代码

### 2.2 性能需求

**超时配置**：
- 下载超时：60秒（应对慢速跨境网络）
- 上传超时：30秒（应对大文件上传）
- 总超时：100秒（包含缓冲时间）

**缓存需求**：
- Redis 缓存 URL 映射，避免重复下载
- 缓存有效期：7天
- 缓存 Key：基于去掉签名参数后的 URL 的 SHA256 hash

**大小限制**：
- 单图最大下载：20MB

### 2.3 功能需求

**判断逻辑**：
- 需要代理的 URL：
  - 中国区域对象存储域名（`*.volces.com`, `*.aliyuncs.com`, `*.myqcloud.com`, `*.myhuaweicloud.com`）
  - 带签名参数的 URL（`X-Tos-Signature`, `X-Amz-Signature`, `OSSAccessKeyId`, `q-signature`）
- 不需要代理的 URL：
  - `data:image/...` 格式（内嵌 base64）
  - 国际 CDN（Cloudflare、AWS CloudFront、Imgur 等）
  - OpenAI/Grok 自己的域名

**降级策略**：
- 如果 CF R2 下载失败或上传失败，去掉签名参数，将干净 URL 传给上游
- 记录降级日志，便于监控和分析

**配置管理**：
- 功能总开关（默认关闭）
- 管理员可在系统设置中开启/关闭
- 超时参数可配置
- 支持域名白名单模式

### 2.4 非功能需求

**可观测性**：
- 完整的结构化日志
- 关键步骤记录（缓存命中/未命中、下载开始/成功/失败、上传开始/成功/失败、降级透传）
- Prometheus 监控指标（成功率、缓存命中率、延迟、降级次数）

**容错性**：
- 功能未启用时，直接透传原 URL
- ImageStorage 未配置时，直接透传原 URL
- Redis 故障时，降级到无缓存模式
- CF R2 故障时，降级到干净 URL 透传

**成本控制**：
- 复用现有 CF R2 基础设施
- R2 生命周期：1天自动清理
- 预期月成本：基本免费（~$0.03）

---

## 三、技术约束

### 3.1 现有基础设施

**必须复用**：
- `ImageStorage` (S3ImageStorage) - 已有 CF R2 配置
- Redis Cache - 用于 URL 映射缓存
- HTTPClient - 用于下载图片

**现有配置**：
- CF R2 生命周期：1天自动清理

### 3.2 集成点

**Service 层集成**：
- `backend/internal/service/openai_images.go` - OpenAI 图片请求处理
- `backend/internal/service/grok_media.go` - Grok 图片/视频请求处理

**集成时机**：
- 在解析请求后、构建上游请求前

### 3.3 配置要求

**配置文件** (`config.yaml`)：
```yaml
image_url_proxy:
  enabled: false                  # 默认关闭
  download_timeout_seconds: 60
  upload_timeout_seconds: 30
  total_timeout_seconds: 100
  max_download_bytes: 20971520    # 20MB
  cache_ttl_hours: 168            # 7天
  storage_key_prefix: "proxy/"
  whitelist_only: false
  domain_whitelist:
    - "*.volces.com"
    - "*.aliyuncs.com"
    - "*.myqcloud.com"
    - "*.myhuaweicloud.com"
```

**管理员系统设置**：
- 功能开关：启用/禁用图片 URL 代理
- 超时配置：下载超时、上传超时
- 白名单模式：仅代理白名单域名

---

## 四、验收标准

### 4.1 功能验收

- [ ] 能够正确判断 URL 是否需要代理
- [ ] 能够成功下载中国区存储的图片（带签名参数）
- [ ] 能够成功上传到 CF R2
- [ ] 能够正确替换请求体中的 URL
- [ ] OpenAI/Grok 能够成功访问代理后的图片
- [ ] 缓存命中时能够直接返回 CF R2 URL
- [ ] 下载/上传失败时能够正确降级到干净 URL

### 4.2 性能验收

- [ ] 缓存命中耗时 < 50ms
- [ ] 3MB 图片首次代理耗时 < 15s
- [ ] 10MB 图片首次代理耗时 < 30s
- [ ] 缓存命中率 > 60%（假设用户平均每张图片编辑3次）

### 4.3 可观测性验收

- [ ] 所有关键步骤都有结构化日志
- [ ] 日志级别分类正确（debug/info/warn/error）
- [ ] Prometheus 指标正常上报
- [ ] Grafana 面板能够展示关键指标

### 4.4 容错性验收

- [ ] 功能关闭时不影响现有流程
- [ ] Redis 故障时能够降级到无缓存模式
- [ ] CF R2 故障时能够降级到干净 URL
- [ ] 降级时记录正确的日志

---

## 五、风险与限制

### 5.1 技术风险

| 风险 | 影响 | 缓解措施 |
|-----|------|---------|
| CF R2 故障 | 高 | 功能开关快速关闭，降级到干净 URL |
| 下载超时 | 中 | 延长超时时间，优化重试逻辑 |
| Redis 故障 | 低 | 降级到无缓存模式 |
| 代理成本超预期 | 低 | 监控用量，设置告警阈值 |

### 5.2 业务风险

| 风险 | 影响 | 缓解措施 |
|-----|------|---------|
| 首次延迟影响用户体验 | 中 | 缓存优化，告知用户首次较慢 |
| 误代理不需要代理的 URL | 低 | 白名单模式，精确匹配规则 |

### 5.3 限制条件

- 只支持图片类型（暂不支持视频代理，但 Grok 视频生成的参考图片需要代理）
- 单图最大 20MB
- 缓存有效期固定 7 天
- R2 文件生命周期固定 1 天

---

## 六、里程碑

| 阶段 | 里程碑 | 预计时间 |
|-----|--------|---------|
| 阶段 1 | 核心功能开发完成 | 5-7小时 |
| 阶段 2 | 管理后台界面完成 | 2小时 |
| 阶段 3 | 监控与日志完成 | 2小时 |
| 阶段 4 | 集成测试与部署完成 | 2小时 |

**总计**：10-13小时

---

## 七、参考资料

- 设计文档：`docs/design/image-url-proxy-to-cf-r2.md`
- Cloudflare R2 文档：https://developers.cloudflare.com/r2/
- AWS S3 Presigned URL：https://docs.aws.amazon.com/AmazonS3/latest/userguide/PresignedUrlUploadObject.html
- 火山引擎 TOS 文档：https://www.volcengine.com/docs/6349/74841
