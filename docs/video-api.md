# 视频平台接入

采用独立平台和共享异步任务处理：账号管理可选择 **Seedance / Ark、Wan / DashScope、Kling、Sora**，客户端统一使用 `/v1/videos`。Seedance、Wan、Kling 同时提供各自的原生端点。设计参考 [new-api 视频适配器](https://github.com/QuantumNous/new-api/tree/c59f6fd553921702cb47dacadc9731fa01548e96/plugins/tasks)，沿用 Sub2API 的分组、调度、权限及用量记录。

## 配置账号和价格

| 平台 | 凭据 | 默认 Base URL | 内置模型示例 |
| --- | --- | --- | --- |
| Seedance / Ark | Ark API Key | `https://ark.cn-beijing.volces.com/api/v3` | `doubao-seedance-1-5-pro-251215` |
| Wan / DashScope | DashScope API Key | `https://dashscope.aliyuncs.com/api/v1` | `wan2.6-t2v`、`wan2.6-i2v`、`wan2.2-kf2v-flash` |
| Kling | Access Key + Secret Key | `https://api.klingai.com/v1` | `kling-v2-6`、`kling-v2-5-turbo` |
| Sora | OpenAI API Key | `https://api.openai.com/v1` | `sora-2`、`sora-2-pro` |

模型列表是预设，并非账号实际权限清单；可手动添加厂商已开通的模型和模型映射。区域不同的服务可修改 Base URL。四个平台仅支持 API Key 账号；Kling 按请求生成签名 JWT，编辑时密钥留空会保留原值。

1. 创建对应平台账号，填写凭据，绑定同平台分组。组合分组可添加显式模型路由，端点选择「任意端点（any）」，分别选择目标平台和上游模型。
2. 启用分组的「允许图片生成」媒体权限；新建视频平台分组默认开启。
3. 四个平台均支持**按次**：在分组或渠道的**模型定价**中添加对应模型，选择 **按次（per_request）**，填写**默认单次价格**。例如填 `0.50` 表示每条成功生成的视频任务基础价格为 **0.50 美元**；配置分辨率层级后，匹配的层级单价优先于默认单价。分组模型价格优先于渠道价格。这是运营者配置的对客价格，不自动同步厂商费用。
4. 其他计费方式继续可用：Wan、Kling、Sora 可选择 **Video** 模式，设置每秒单价，并用 `720p`、`1080p` 等标签配置分辨率档位；未配置按次或 Video 价格时提交会返回 `video_pricing_required`。Seedance 可选择 **Token** 模式，根据上游 `usage.completion_tokens`（缺失或为零时使用 `total_tokens`）结算，需配置输出 token 价格；不使用 Grok 按秒价格。
5. 为客户端创建绑定分组的 Sub2API API Key。客户端只使用这个 Key；厂商密钥保存在账号配置中。管理员的账号测试会实际提交视频任务，可能产生上游费用，但不扣客户端余额。

JSON 账号导入仍使用现有导入格式，单个账号的相关字段如下。以下为占位符，实际凭据不要写入版本控制：

```json
{
  "name": "Kling video",
  "platform": "kling",
  "type": "apikey",
  "credentials": {
    "base_url": "https://api.klingai.com/v1",
    "kling_access_key": "YOUR_ACCESS_KEY",
    "kling_secret_key": "YOUR_SECRET_KEY"
  }
}
```

其他三个平台使用 `credentials.api_key`。Kling 导入也接受 `api_key: "AccessKey|SecretKey"` 或兼容服务的 Bearer Key。

## 一个公开模型对应多个分辨率模型

在**账号管理 → 新建/编辑视频平台账号 → 按分辨率映射视频模型**中，添加公开模型 `sd-video`，分别填写上游目标：

| 客户分辨率 | 上游模型 |
| --- | --- |
| `480p` | `sd-480p` |
| `720p` | `sd-720p` |
| `1080p` | `sd-1080p` |

默认分辨率可以选择 `720p`。Seedance、Wan、Kling、Sora 均支持这项账号配置；每个上游账号可配置不同的目标模型。客户端只需提交：

```json
{
  "model": "sd-video",
  "prompt": "海浪轻轻拍打沙滩",
  "resolution": "1080p",
  "seconds": 5
}
```

- `/v1/models` 会列出 `sd-video`，该账号的三个目标模型会隐藏；直接请求隐藏的目标模型不会通过账号模型检查。其他普通白名单和模型映射继续有效。如需分组只对外提供一个模型，分组内其他账号也应配置相同的公开模型范围。
- 不传 `resolution` 和 `size` 时使用账号配置的默认档位；传 `size`（如 `1080x1920`）时按短边判断分辨率并保留指定方向。仅传分辨率或使用默认档位时，统一接口采用横屏 `16:9`。同时传两个字段时必须一致。不支持的分辨率返回 `400`，不会静默降级。
- 调度会跳过不支持请求分辨率的账号；如使用组合分组，组合路由的目标填写账号配置中的公开模型 `sd-video`。同一公开模型的各账号建议使用一致的默认分辨率。
- 在分组或渠道的**模型定价**中，对 `sd-video` 选择**按次**；可用 `480p`、`720p`、`1080p` 层级标签分别设置每条视频价格。没有匹配层级时使用默认单次价格，明确填写 `0` 的层级保持免费。无需对三个私有模型重复配置对客价格。
- 任务记录保存创建时的公开模型、上游模型和分辨率。之后修改账号映射不改变既有任务的查询账号和模型归属。价格沿用现有结算时定价规则，不冻结创建时单价。

JSON 导入也可在 `credentials` 中添加以下配置，不需要数据库迁移：

```json
"video_model_routes": {
  "sd-video": {
    "default_resolution": "720p",
    "resolutions": {
      "480p": "sd-480p",
      "720p": "sd-720p",
      "1080p": "sd-1080p"
    }
  }
}
```

路由使用精确模型名称；同一个公开模型不能同时配置指向其他模型的普通映射。目标应填写上游真实支持的模型 ID。

## 统一接口

以下命令使用 Bash 环境变量。`SUB2API_BASE_URL` 为网关地址，`SUB2API_KEY` 为客户端 Key。

```bash
curl "$SUB2API_BASE_URL/v1/videos" \
  -H "Authorization: Bearer $SUB2API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "wan2.6-t2v",
    "prompt": "海浪轻轻拍打沙滩，固定镜头",
    "seconds": 5,
    "size": "1280x720"
  }'

# VIDEO_ID 使用创建响应中的完整 id
curl "$SUB2API_BASE_URL/v1/videos/$VIDEO_ID" \
  -H "Authorization: Bearer $SUB2API_KEY"

# 等待 status=completed 后下载，支持 Range 请求和 HEAD
curl "$SUB2API_BASE_URL/v1/videos/$VIDEO_ID/content" \
  -H "Authorization: Bearer $SUB2API_KEY" \
  -o video.mp4
```

返回统一的 `id`、`object: "video"`、`status`、`progress` 等字段。状态为 `queued`、`in_progress`、`completed` 或 `failed`；进度是阶段值，不是厂商实际百分比。`id` 为网关包装后的不透明 ID，Sora 客户端也应原样保存并回传，不应替换为厂商原始 ID。完成后 `url` 指向需要同一 Sub2API Key 的下载接口。

图生视频在 JSON 中增加 `input_reference` 图片 URL；Seedance、Wan 使用 URL 引用，Kling、Sora 另支持 multipart 文件。Sora 的文件提交示例：

```bash
curl "$SUB2API_BASE_URL/v1/videos" \
  -H "Authorization: Bearer $SUB2API_KEY" \
  -F model=sora-2 -F prompt='海浪缓缓运动' \
  -F seconds=4 -F size=1280x720 \
  -F input_reference=@first-frame.png
```

Seedance、Wan、Kling 的厂商扩展参数放入 `metadata` 对象；例如 Kling 使用 `"metadata":{"mode":"pro","negative_prompt":"模糊"}`，Seedance 使用 `"metadata":{"generate_audio":true}`。模型字段由路由和账号模型映射决定，不能通过 metadata 绕过。Sora 请求字段按其原生协议透传。实际时长、尺寸、图片格式与模型限制由厂商验证。

## 原生端点

| 厂商 | 提交 | 查询 |
| --- | --- | --- |
| Seedance | `POST /api/v3/contents/generations/tasks` | `GET /api/v3/contents/generations/tasks/{id}` |
| Wan | `POST /api/v1/services/aigc/video-generation/video-synthesis` | `GET /api/v1/tasks/{id}` |
| Wan 首尾帧 / 语音驱动 | `POST /api/v1/services/aigc/image2video/video-synthesis` | 同上 |
| Kling 文生视频 | `POST /v1/videos/text2video` | `GET /v1/videos/text2video/{id}` |
| Kling 图生视频 | `POST /v1/videos/image2video` | `GET /v1/videos/image2video/{id}` |

上述端点保留原生请求和响应格式，查询使用原生响应中的任务 ID。Wan 自动补充异步请求头，Kling 自动签名。Seedance 另支持原生 DELETE 和历史 OpenAI 账号端点能力配置，详见 [Seedance 文档](seedance-api.md)。Sora 与统一接口共用 `/v1/videos`、状态及下载路径，使用网关包装后的 ID。

## 任务与计费边界

- 任务只能由创建它的同一用户、API Key 和分组访问；查询与下载始终使用原提交账号，不会换账号查询。
- 成功查询或成功下载时，发现任务生成成功后结算，排队、运行中、取消和失败的任务不收费。下载报错时，该次不触发结算，可再次查询状态。原生查询、统一查询和下载共用同一任务计费标识，重复访问不重复扣费。
- **按次模式**以成功任务为单位：一次生成一条视频收一次单价，不再乘视频秒数或 token 数；即使上游未返回时长、分辨率或 token 用量也能结算。提交请求和查询请求本身不分别收费。
- **Video 按秒模式**下，Wan、Kling、Sora 优先使用返回的实际时长，未返回时使用创建时快照；不会套用 Grok 的 15 秒上限。Wan 返回输入视频时长时计入总时长。Seedance 的 Token 模式仍需上游返回有效用量才结算，缺失用量时等待后续查询。
- 沿用 Redis 保存任务归属和计费参数快照，默认有效期 **24 小时**，没有新增数据库表或后台轮询。客户端必须在有效期内轮询到终态；清空 Redis、任务过期或只提交不查询会影响任务访问与结算。
- 上游创建失败不自动重试，避免重复收费。上游已接受但本地任务保存失败时返回 `503 video_task_storage_error`，应由管理员检查日志中的任务 ID，不要自动重新提交。
- 当前范围是任务创建、查询、下载及 Seedance 原生删除；不包含跨用户任务列表、回调、remix、草稿续作、Kling v4 Omni 或厂商资产管理。不接受回调字段，避免回调中的原始任务 ID 与网关归属、结算脱节。
- 本地自动化测试使用模拟上游；真实生成需要可用厂商凭据和对应模型权限。
