---
public: true
slug: image-api
title: Duomi 生图 API
description: 通过 HTTPS 调用文生图和图像编辑接口
---

# Duomi 生图 API

版本：1.0

Duomi 生图 API 通过 HTTPS 提供文生图和图像编辑能力，请使用本文档列出的公网接口。

## 公网地址

```text
https://imgapi.duomi.cloud
```

本文档中的接口路径均以该地址为基础地址。

## 身份认证

每个接口请求都必须携带 API Key：

```http
Authorization: Bearer <API_KEY>
```

请访问 [https://subapi.duomi.cloud/](https://subapi.duomi.cloud/)，创建 API Key 并选择已开启生图权限的分组。请仅在受信任的服务端保存和使用 API Key，不要暴露在浏览器代码、移动端应用、代码仓库或日志中。

## 接口列表

公网稳定路径如下：

| 操作 | 方法 | 路径 | Content-Type |
| --- | --- | --- | --- |
| 文生图 | `POST` | `/v1/generate` | `application/json` |
| 图像编辑 | `POST` | `/v1/edit` | `multipart/form-data` 或 JSON 图片 URL |
| 提交文生图任务 | `POST` | `/v1/generate/async` | `application/json` |
| 提交图像编辑任务 | `POST` | `/v1/edit/async` | `multipart/form-data` 或 JSON 图片 URL |
| 查询异步任务 | `GET` | `/v1/jobs/{task_id}` | - |

公网客户端请直接使用上表中的完整路径和请求示例。

## 文生图

### 请求格式

```http
POST /v1/generate
Content-Type: application/json
Authorization: Bearer <API_KEY>
```

```json
{
  "model": "gpt-image-2",
  "prompt": "一只在雪地森林中的红狐，电影级光影",
  "size": "1024x1024",
  "quality": "high",
  "n": 1,
  "response_format": "url"
}
```

### 请求参数

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `model` | string | 否 | 图片模型，默认 `gpt-image-2`。模型必须是当前 API Key 可用的模型。 |
| `prompt` | string | 是 | 描述需要生成的图片内容。 |
| `size` | string | 否 | 图片尺寸，例如 `1024x1024`、`1536x1024`。可用尺寸取决于模型。 |
| `quality` | string | 否 | 模型支持的质量选项。 |
| `n` | integer | 否 | 生成图片数量，必须为正整数，默认 `1`。 |
| `response_format` | string | 否 | `url` 或 `b64_json`，是否支持取决于模型。 |
| `background` | string | 否 | 模型支持的背景选项。 |
| `output_format` | string | 否 | 模型支持的输出格式选项。 |
| `style` | string | 否 | 模型支持的风格选项。 |
| `moderation` | string | 否 | 模型支持的内容审核选项。 |

模型不支持的参数可能会被服务拒绝。

### curl 示例

```bash
curl -X POST "https://imgapi.duomi.cloud/v1/generate" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一只在雪地森林中的红狐，电影级光影",
    "size": "1024x1024",
    "quality": "high",
    "n": 1,
    "response_format": "url"
  }'
```

### 响应格式

```json
{
  "created": 1784092923,
  "data": [
    {
      "url": "https://example.com/generated-image.png"
    }
  ]
}
```

根据 `response_format` 和所选模型，`data` 中的图片可能通过 `url` 或 `b64_json` 返回。调用方应兼容这两种格式。

## 图像编辑

图像编辑请求必须包含至少一张源图片和一个编辑提示词。处理本地文件时，推荐使用 multipart 表单。

### multipart 请求

使用重复的 `image[]` 字段传入一张或多张源图片。不要手动设置 multipart boundary，应由 curl 或 SDK 自动生成。

```bash
curl -X POST "https://imgapi.duomi.cloud/v1/edit" \
  -H "Authorization: Bearer ${API_KEY}" \
  -F "model=gpt-image-2" \
  -F "prompt=将背景替换为日落海滩，保留主体和构图" \
  -F "size=1024x1024" \
  -F "image[]=@./input.png"
```

传入多张源图片时重复 `image[]` 字段：

```bash
  -F "image[]=@./reference-a.png" \
  -F "image[]=@./reference-b.png"
```

multipart 表单支持以下字段：`model`、`prompt`、`size`、`n`、`quality`、`background`、`output_format`、`response_format`、`style`、`moderation`、`input_fidelity`、`output_compression` 和 `partial_images`。

### JSON 图片 URL 请求

源图片已经托管在网络上时，可以使用 `images[].image_url`：

```http
POST /v1/edit
Content-Type: application/json
Authorization: Bearer <API_KEY>
```

```json
{
  "model": "gpt-image-2",
  "prompt": "将背景替换为雪山",
  "images": [
    {"image_url": "https://example.com/input.png"}
  ]
}
```

当前不支持 `images[].file_id` 和 `mask.file_id`。需要蒙版时，可使用 multipart 的 `mask` 文件字段，或在 JSON 中使用 `mask.image_url`。

图像编辑的响应格式与文生图一致：

```json
{
  "created": 1784092923,
  "data": [
    {
      "url": "https://example.com/edited-image.png"
    }
  ]
}
```

## 异步任务

当生成时间可能超过调用方或 CDN 的 HTTP 超时时间时，建议使用异步接口。

### 提交任务

```bash
curl -i -X POST "https://imgapi.duomi.cloud/v1/generate/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一只在雪地森林中的红狐",
    "size": "1024x1024"
  }'
```

成功提交返回 HTTP `202 Accepted`：

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1784092800,
  "expires_at": 1784179200
}
```

响应头包含 `Location` 和 `Retry-After`。请以 `Retry-After` 作为最小轮询间隔。异步图像编辑使用与同步编辑相同的 multipart 或 JSON 请求格式。

异步任务不支持 `stream: true`。

### 查询任务

必须使用提交任务时的同一个 API Key：

```bash
curl "https://imgapi.duomi.cloud/v1/jobs/imgtask_0123456789abcdef" \
  -H "Authorization: Bearer ${API_KEY}"
```

任务处理中：

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1784092800,
  "expires_at": 1784179200
}
```

任务成功：

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "completed",
  "http_status": 200,
  "result": {
    "created": 1784092923,
    "data": [{"url": "https://storage.example.com/images/result.png"}]
  },
  "created_at": 1784092800,
  "completed_at": 1784092923,
  "expires_at": 1784179323
}
```

如果存在 `image_url` 字段，它表示第一张结果图片的地址。图片 URL 有效期为 1 天，请及时下载保存。

任务失败时，`status` 为 `failed`，`error` 中包含错误对象：

```json
{
  "id": "imgtask_0123456789abcdef",
  "task_id": "imgtask_0123456789abcdef",
  "object": "image.generation.task",
  "status": "failed",
  "http_status": 502,
  "error": {
    "type": "api_error",
    "message": "image generation failed"
  }
}
```

异步任务仅对创建任务的 API Key 可见，其他 API Key 无法查询该任务。

## 错误响应

接口通常使用以下错误结构：

```json
{
  "error": {
    "type": "invalid_request_error",
    "message": "..."
  }
}
```

| HTTP 状态码 | 含义 | 处理建议 |
| --- | --- | --- |
| `400` | JSON、multipart、模型、图片字段或参数无效 | 修正请求后重试。 |
| `401` | API Key 缺失或无效 | 添加 `Authorization: Bearer <API_KEY>`。 |
| `403` | API Key 没有生图权限，或请求被内容策略拒绝 | 检查 API Key 所属分组是否已开启生图权限，并确认请求内容符合要求。 |
| `404` | 路径不存在、模型不支持或任务已过期 | 检查请求路径和任务 ID。 |
| `413` | 请求体或上传图片过大 | 压缩图片或减少输入图片数量。 |
| `429` | 速率、并发或余额限制 | 遵循 `Retry-After`，采用退避重试。 |
| `5xx` | 服务临时故障 | 采用退避重试，并记录请求 ID。 |

如果响应包含 `x-request-id`，请在反馈问题时一并提供该值。

## Node.js 示例

Node.js 20 及以上版本可以直接使用内置 `fetch`：

```js
const response = await fetch('https://imgapi.duomi.cloud/v1/generate', {
  method: 'POST',
  headers: {
    Authorization: `Bearer ${process.env.DUOMI_IMAGE_API_KEY}`,
    'Content-Type': 'application/json',
  },
  body: JSON.stringify({
    model: 'gpt-image-2',
    prompt: '一只在雪地森林中的红狐，电影级光影',
    size: '1024x1024',
    quality: 'high',
    n: 1,
  }),
})

const body = await response.json()
if (!response.ok) {
  throw new Error(JSON.stringify(body))
}

console.log(body.data)
```

## 接入建议

- API Key 只保存在服务端环境变量或密钥管理系统中。
- 解析图片结果时同时支持 `url` 和 `b64_json`。
- 异步任务和限流响应都应遵循 `Retry-After`。
- 持久化异步任务的 `task_id`，并在 `expires_at` 前停止轮询。
- 图片 URL 有效期为 1 天，需要长期保存时请及时下载。
