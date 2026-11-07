---
public: true
slug: image-api-by-provider
title: Duomi 生图 API 使用指南
description: OpenAI、Grok 和 Gemini 香蕉模型的模型查询、文生图、图像编辑、同步及异步调用说明
---

# Duomi 生图 API 使用指南

文档核对日期：2026-11-07。

本文按 **OpenAI、Grok（xAI）、Gemini（Nano Banana，香蕉）** 分别介绍调用方式。同步请求采用各厂商的协议；异步请求采用本站提供的任务接口。请求参数以本文说明和当前 API Key 可用模型为准。

## 1. 创建 API Key 与认证

在 [Duomi 控制台](https://subapi.duomi.cloud/) 创建 API Key，选择支持目标厂商且已开启生图权限的分组。下文 `${API_KEY}` 替换为你的 Key。

所有接口统一使用 Bearer 认证：

```http
Authorization: Bearer <API_KEY>
```

## 2. 查询可用模型

`GET https://subapi.duomi.cloud/v1/models`

```bash
curl "https://subapi.duomi.cloud/v1/models" \
  -H "Authorization: Bearer ${API_KEY}"
```

## 3. OpenAI

### 3.1 同步文生图

```bash
curl "https://subapi.duomi.cloud/v1/images/generations" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2.6-sunburst",
    "prompt": "一只在雪地森林中的红狐，电影级光影，写实摄影",
    "n": 1,
    "size": "1536x1024",
    "quality": "high",
    "output_format": "png"
  }'
```

### 3.2 请求参数：生成和编辑共用

下表围绕本站已接入的 GPT Image 常用能力说明；模型原生默认值与本站兼容扩展分别标出。可选字段不需要全部填写。

| 参数 | 类型 | 必填 | 取值、默认值与作用 |
| --- | --- | --- | --- |
| `model` | string | 建议始终填写 | 当前 Key 可用的图片模型 ID，包括 `gpt-image-2.6-sunburst`、`gpt-image-2.6-flare`。本站 OpenAI 图片入口未传时使用 `gpt-image-2`。 |
| `prompt` | string | 是 | 生成内容或编辑指令。GPT Image 官方上限为 32,000 字符。需要特定画风、文字、构图时直接写入提示词。 |
| `n` | integer | 否 | 输出图片或 GIF 数量，默认 `1`，官方范围 `1`～`10`。多张图片会增加耗时和用量；GIF 还需满足总像素预算。 |
| `size` | string | 否 | 默认 `auto`；静态图片常用 `1024x1024`、`1536x1024`、`1024x1536`。`gpt-image-2` 和 2.5 系列的自定义尺寸限制见下文。GIF 建议使用 `auto` 自动选择每帧尺寸；自定义宽高均须为 16 的倍数，帧尺寸、帧数和 `n` 共同受模型像素预算限制。 |
| `quality` | string | 否 | 默认 `auto`；`low`、`medium`、`high`。2.6 和 2.5 的 Sunburst / Flare 还支持 `xhigh`、`max`，不要用于 `gpt-image-2`。 |
| `complexity` | string | 否 | **2.6 独有**：`low`、`medium`、`high`，默认 `high`；控制构图规划的计算投入，与渲染质量 `quality` 分开设置。 |
| `background` | string | 否 | `auto`（默认）、`opaque`（不透明）、`transparent`（透明）。透明输出需选择 `png` 或 `webp`；`gpt-image-2` 的透明背景能力处于官方预览阶段，依赖实际渠道支持。 |
| `output_format` | string | 否 | 静态图片使用 `png`（默认）、`jpeg`、`webp`；**`gif` 为 2.6 独有**，用于生成动图。它决定文件编码，与 `response_format` 的传输形式不同。 |
| `frame_count` | integer | 否 | **2.6 独有，仅 `output_format: "gif"` 时生效**。GIF 帧数，`2`～`16`，默认 `9`。 |
| `frame_duration_ms` | integer | 否 | **2.6 独有，仅 `output_format: "gif"` 时生效**。每帧间隔，单位毫秒，默认 `200`；按官方指南使用 `20`～`2000`，编码时取整到最接近的 10 毫秒。 |
| `output_compression` | integer | 否 | `0`～`100`，官方默认 `100`；仅用于 `jpeg` 或 `webp`，数值越低压缩越强。 |
| `moderation` | string | 否 | `auto`（默认）或 `low`，调整模型支持的内容过滤级别；`low` 不代表关闭审核。 |
| `stream` | boolean | 否 | 默认 `false`。`true` 请求 SSE 流，需客户端和渠道支持；OpenAI 异步任务会拒绝 `true`。 |
| `partial_images` | integer | 否 | `0`～`3`，默认 `0`。流式请求中希望收到的中间图片数量，最终图片可能提前完成；普通同步和异步调用建议省略。 |
| `response_format` | string | 否 | **本站兼容选项**：`url` 或 `b64_json`。OpenAI 官方 GPT Image 返回 Base64，不支持用该字段切换 URL。本站实际返回会受渠道配置影响；建议原生示例省略此字段，客户端兼容 `url` 与 `b64_json`。 |

`gpt-image-2` / 2.5 的自定义尺寸要求宽高都是 16 的倍数，宽高比在 `1:3`～`3:1` 之间，例如 `1536x864`。官方将高于 `2560x1440` 的分辨率标为实验能力，最高支持到 `3840x2160`，还需满足所选模型的像素和边长限制。初次接入建议使用上表的常用尺寸。

表中“2.6”指 `gpt-image-2.6-sunburst` 和 `gpt-image-2.6-flare`。生成或编辑 GIF 时设置 `output_format: "gif"`，在 `prompt` 中描述动作、镜头和循环效果；同步及异步均沿用本节的生成、编辑接口。GIF 不支持 `stream: true`、`partial_images`、`mask` 或自定义 `output_compression`。

`style: vivid/natural` 是旧模型参数，不适用于 GPT Image；请把风格写入 `prompt`。

### 3.3 同步图像编辑：本地文件

通过 `multipart/form-data` 上传源图；curl 会自动生成 Content-Type 和 boundary，不要自行添加 JSON Content-Type。

```bash
curl "https://subapi.duomi.cloud/v1/images/edits" \
  -H "Authorization: Bearer ${API_KEY}" \
  -F "model=gpt-image-2" \
  -F "prompt=将背景替换为日落海滩，保留人物面容、衣服和姿态" \
  -F "image[]=@./input.png;type=image/png" \
  -F "size=1024x1536" \
  -F "quality=high" \
  -F "n=1"
```

多图参考时重复文件字段，例如：

```bash
  -F "image[]=@./person.png;type=image/png" \
  -F "image[]=@./clothes.png;type=image/png"
```

上面两行是替换单图字段的片段，需要放入完整的编辑命令中。

### 3.4 同步图像编辑：图片 URL 或 Data URL

JSON 请求使用 **`images[].image_url`**：

```bash
curl "https://subapi.duomi.cloud/v1/images/edits" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "把商品背景改成浅灰色摄影棚，保留商品外观",
    "images": [
      {"image_url": "https://your-cdn.example/input.png"}
    ],
    "size": "1024x1024",
    "quality": "high"
  }'
```

将示例 URL 替换为实际可访问图片。也可填入 `data:image/png;base64,<完整Base64>`，其中 Base64 必须是图片内容，不能是文件路径。

### 3.5 编辑专用参数与蒙版

| 参数 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `image` / `image[]` | multipart 文件 | multipart 编辑必填 | 单图可用 `image`，多图使用重复的 `image[]`。建议使用 PNG、JPEG 或 WebP；GPT Image 官方最多接受 16 张，单图小于 50 MB，本站还会限制总请求体大小。 |
| `images` | array | JSON 编辑必填 | 非空数组，每项通过 `image_url` 提供源图。不能把 multipart 字段名直接用作 JSON 字段。 |
| `images[].image_url` | string | JSON 每张源图必填 | 完整 HTTPS URL 或带 MIME 类型的 Base64 Data URL。URL 必须能被服务端访问。 |
| `mask` | multipart 文件 / JSON object | 否 | 指定局部编辑区域。multipart 上传文件；JSON 使用 `{"image_url":"..."}`。 |
| `mask.image_url` | string | 使用 JSON 蒙版时必填 | 蒙版的 HTTPS URL 或 Data URL。建议使用带 alpha 通道的 PNG，与第一张待编辑图片尺寸一致；完全透明区域表示需要修改的区域。 |
| `input_fidelity` | string | 否 | 输入细节保真度。官方 `gpt-image-1.5` 支持 `high` / `low`，默认 `low`；`gpt-image-1-mini` 仅支持 `low`。**`gpt-image-2` 请省略此参数**，其他模型按对应文档使用。 |

本站不支持 `images[].file_id` 或 `mask.file_id`。图片与蒙版应保持一致的尺寸和合适的文件格式；蒙版是编辑引导，不能保证边界内外像素完全不变。

在 3.3 的完整命令中添加以下字段即可上传蒙版：

```bash
  -F "mask=@./mask.png;type=image/png"
```

### 3.6 同步返回

典型 Base64 返回，示例中的图片数据已省略：

```json
{
  "created": 1794000000,
  "data": [{"b64_json": "<图片Base64>"}],
  "output_format": "png",
  "size": "1536x1024"
}
```

本站部分渠道会返回 `data[].url`。解析时遍历 `data`：存在 `b64_json` 就进行 Base64 解码；存在 `url` 就下载图片或处理 Data URL。`created` 是秒级 Unix 时间戳，`output_format`、`size`、`usage` 等元数据可能因渠道而异。

### 3.7 异步文生图与异步编辑

参数沿用上述 OpenAI 请求格式，只改变路径，不传 `stream: true`。

异步文生图：

```bash
curl -i "https://subapi.duomi.cloud/v1/images/generations/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-image-2",
    "prompt": "一座漂浮在云海上的未来城市，写实建筑摄影",
    "size": "1536x1024",
    "quality": "high",
    "n": 1
  }'
```

异步编辑：

```bash
curl -i "https://subapi.duomi.cloud/v1/images/edits/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -F "model=gpt-image-2" \
  -F "prompt=保留人物，把背景换成雪山" \
  -F "image[]=@./input.png;type=image/png" \
  -F "size=1024x1536"
```

也可以向异步编辑路径提交 3.4 的 JSON 格式。提交响应、轮询与失败处理见第 6 节。

查询上述文生图或编辑任务：将 `${TASK_ID}` 替换为提交响应的 `task_id`，使用提交时的同一个 Key。

```bash
curl "https://subapi.duomi.cloud/v1/images/tasks/${TASK_ID}" \
  -H "Authorization: Bearer ${API_KEY}"
```

## 4. Grok / xAI

### 4.1 同步文生图

Grok 使用 `/v1/images/generations`，尺寸采用 xAI 原生的 **`aspect_ratio` + `resolution`**：

```bash
curl "https://subapi.duomi.cloud/v1/images/generations" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-image-2.0",
    "prompt": "日出时分的山间湖泊，远处薄雾，写实摄影",
    "n": 1,
    "aspect_ratio": "16:9",
    "resolution": "1k",
    "response_format": "url"
  }'
```

### 4.2 请求参数

| 参数 | 类型 | 必填 | 取值、默认值与作用 |
| --- | --- | --- | --- |
| `model` | string | 是 | 当前 Key 可用的 Grok 图片模型，例如 `grok-imagine-image-2.0`。 |
| `prompt` | string | 是 | 要生成的内容或对源图的编辑指令。 |
| `n` | integer | 否 | 输出数量，官方范围 `1`～`10`，默认 `1`；编辑时还需符合所选模型能力。 |
| `aspect_ratio` | string | 否 | 输出宽高比，见下表。当前官方文生图默认 `auto`；多图编辑未指定时默认跟随第一张源图。 |
| `resolution` | string | 否 | `1k`（默认）或 `2k`，注意小写 `k`。这是分辨率档位，实际宽高由比例决定。 |
| `response_format` | string | 否 | `url`（默认）或 `b64_json`，决定返回图片地址还是 Base64。本站渠道配置可能影响最终形式，客户端应兼容两种结果。 |
| `quality` | string | 否 | **仅 `grok-imagine-image-2.0`**：`low`、`medium`、`auto`（默认）。当前官方 `auto` 文生图选择 `low`、编辑选择 `medium`。不要使用 OpenAI 的 `high`、`xhigh` 或 `max`。 |
| `image` | object | 单图编辑必填 | `{"url":"...","type":"image_url"}`。文生图省略。 |
| `image.url` | string | 使用 `image` 时必填 | 公网图片 URL，或 `data:image/png;base64,<完整Base64>`。 |
| `image.type` | string | 使用 `image` 时填写 | 固定为 `image_url`，即使 `url` 中使用 Data URL 也不变。 |
| `images` | array | 多图编辑必填 | 每项为 `{"url":"...","type":"image_url"}`；使用多图数组时省略单图 `image`，避免重复计入输入。**本站当前最多接受 3 张源图**，即使官方文档允许更多，也以本站限制为准。 |

当前官方文生图支持的宽高比：

| 类别 | `aspect_ratio` |
| --- | --- |
| 自动、正方形 | `auto`、`1:1` |
| 常用横竖屏 | `16:9`、`9:16`、`4:3`、`3:4`、`3:2`、`2:3` |
| 长图和手机屏幕 | `2:1`、`1:2`、`19.5:9`、`9:19.5`、`20:9`、`9:20` |
| 更宽构图 | `21:9`、`5:2`；较旧模型或渠道可能不支持 |

本文不使用 OpenAI 的 `size`、`background`、`output_format`、`input_fidelity` 或 `style` 来控制 Grok。不要认为参数被接受就一定生效，也不要依赖不支持的字段会被忽略。

### 4.3 同步单图编辑

xAI 原生编辑使用 **JSON 的 `image.url`**，与 OpenAI 的 `images[].image_url` 不同：

```bash
curl "https://subapi.duomi.cloud/v1/images/edits" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-image-2.0",
    "prompt": "将这张照片转换为精细的铅笔素描，保留人物面容",
    "image": {
      "url": "https://your-cdn.example/portrait.png",
      "type": "image_url"
    },
    "response_format": "url"
  }'
```

请替换图片 URL。本地文件需先编码成 Data URL 再填入 `image.url`。使用 xAI 原生协议时不要直接套用发送 multipart 的 OpenAI SDK `images.edit()`。

### 4.4 同步多图编辑

把以下 JSON 作为 `/v1/images/edits` 的请求体：

```json
{
  "model": "grok-imagine-image-2.0",
  "prompt": "让第一张图中的人物穿上第二张图中的外套，保持人物面容",
  "images": [
    {"url": "https://your-cdn.example/person.png", "type": "image_url"},
    {"url": "https://your-cdn.example/coat.png", "type": "image_url"}
  ],
  "aspect_ratio": "3:4",
  "response_format": "url"
}
```

多轮编辑时，把上一次输出图片作为下一次的 `image.url`，并提供新的 `prompt`。本站本文约定使用 URL 或 Data URL，不提供 xAI Files API 的上传和 `file_id` 接入流程，也不将 OpenAI 蒙版参数作为 Grok 的通用能力。

### 4.5 同步返回

```json
{
  "data": [{"url": "https://your-result-host.example/generated.jpg"}]
}
```

`response_format: "b64_json"` 对应 `data[].b64_json`。其他元数据可能因模型和渠道变化。URL 通常是临时地址，请及时下载；不能把任务有效期当作图片 URL 的有效期。

### 4.6 异步文生图与异步编辑

本站异步接口继续使用上述 xAI JSON 格式。

异步文生图：

```bash
curl -i "https://subapi.duomi.cloud/v1/images/generations/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-image-2.0",
    "prompt": "雨后城市街道，霓虹灯倒影，电影画面",
    "aspect_ratio": "16:9",
    "resolution": "1k",
    "n": 1
  }'
```

异步编辑：

```bash
curl -i "https://subapi.duomi.cloud/v1/images/edits/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-imagine-image-2.0",
    "prompt": "把照片中的白天改为傍晚，保持建筑结构",
    "image": {
      "url": "https://your-cdn.example/street.png",
      "type": "image_url"
    }
  }'
```

多图编辑可直接使用 4.4 的请求体，改为异步编辑路径。

查询上述文生图或编辑任务：将 `${TASK_ID}` 替换为提交响应的 `task_id`，使用提交时的同一个 Key。

```bash
curl "https://subapi.duomi.cloud/v1/images/tasks/${TASK_ID}" \
  -H "Authorization: Bearer ${API_KEY}"
```

`status: "completed"` 后读取 `result.data`；任务状态和错误说明见第 6 节。

## 5. Gemini / Nano Banana（香蕉）

### 5.1 模型选择

| 常用名称 | 模型 ID | 原生图像输出配置 |
| --- | --- | --- |
| Nano Banana Pro | `gemini-3-pro-image-preview` | `imageSize` 支持 `1K`、`2K`、`4K`。 |
| Nano Banana 2 | `gemini-3.1-flash-image-preview` | `imageSize` 支持 `512`、`1K`、`2K`、`4K`。 |
| Nano Banana 2.1 | `gemini-nano-banana-2.1` | `imageSize` 支持 `1K`、`2K`、`4K`，不支持 `512`。 |

三款模型均使用以下原生调用格式。本站兼容异步入口当前使用两个 `-preview` 模型；`gemini-nano-banana-2.1` 使用原生同步接口。

### 5.2 原生同步文生图

Gemini 的模型写在 URL 中，提示词写在 `contents[].parts[].text` 中：

```bash
curl "https://subapi.duomi.cloud/v1beta/models/gemini-nano-banana-2.1:generateContent" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [
      {
        "role": "user",
        "parts": [{"text": "生成一张香蕉主题咖啡馆海报，画面包含清晰的中文标题：今日营业"}]
      }
    ],
    "generationConfig": {
      "responseModalities": ["TEXT", "IMAGE"],
      "imageConfig": {
        "aspectRatio": "3:4",
        "imageSize": "2K"
      }
    }
  }'
```

使用另外两款模型时，替换 URL 中的模型 ID 即可，例如 `gemini-3-pro-image-preview:generateContent`。

### 5.3 原生请求参数

| 参数 | 类型 | 必填 | 取值、默认值与作用 |
| --- | --- | --- | --- |
| URL 中的 `{model}` | string | 是 | 当前 Key 可用的 Gemini 图片模型，不在 JSON 顶层另写 `model`。 |
| `contents` | array | 是 | 对话内容列表。单次生成通常只有一条用户内容，多轮编辑按时间顺序传入历史。 |
| `contents[].role` | string | 建议填写 | 当前指令使用 `user`；回传模型历史内容时使用 `model`。 |
| `contents[].parts` | array | 是 | 文本和图片片段列表。同一条用户内容中可同时放入提示词和多张图片。 |
| `parts[].text` | string | 文生图及本文编辑示例必填 | 描述要生成的图或要修改的部分；不需要顶层 `prompt`。 |
| `parts[].inlineData` | object | 使用内联源图编辑时必填 | 图片数据对象，包含 `mimeType` 与 `data`。文生图省略。 |
| `inlineData.mimeType` | string | 使用内联图时必填 | 图片真实 MIME 类型，本文推荐 `image/png`、`image/jpeg` 或 `image/webp`，应与图片文件一致。 |
| `inlineData.data` | string | 使用内联图时必填 | **纯 Base64 字符串**，不包含 `data:image/...;base64,` 前缀、文件路径或 HTTP URL。 |
| `generationConfig` | object | 否 | 输出配置容器。 |
| `generationConfig.responseModalities` | string[] | 建议填写 | `['TEXT','IMAGE']` 同时允许文字和图片，也是官方默认输出类型；`['IMAGE']` 请求只返回图片。JSON 实际使用双引号。 |
| `generationConfig.imageConfig` | object | 否 | 图像专用配置容器。 |
| `imageConfig.aspectRatio` | string | 否 | 图片宽高比，见下文。未指定时通常跟随输入图，纯文生图通常为 `1:1`。 |
| `imageConfig.imageSize` | string | 否 | 默认 `1K`，支持值见 5.1。`1K` / `2K` / `4K` 中的 `K` 必须大写；只有 `gemini-3.1-flash-image-preview` 支持低分辨率值 `512`。 |
| `parts[].thoughtSignature` | string | 多轮回传时按原响应保留 | 部分模型返回的思考签名，下一轮回传历史时需保持原值；不要自行编造或丢弃。 |

常用比例为 `1:1`、`2:3`、`3:2`、`3:4`、`4:3`、`4:5`、`5:4`、`9:16`、`16:9`、`21:9`。`gemini-3.1-flash-image-preview` 和 `gemini-nano-banana-2.1` 另支持 `1:4`、`4:1`、`1:8`、`8:1`。

Gemini 原生请求不使用 OpenAI 的 `n`、`size`、`quality`、`response_format` 或 `background`。需要多张独立图片时分别请求；不要把 `candidateCount` 当作可靠的图片数量控制。本文的原生编辑采用提示词指定修改区域，没有 OpenAI 式的独立 `mask` 参数。

### 5.4 原生同步图像编辑

文生图和图像编辑使用同一个 `generateContent` 接口。编辑时增加 `inlineData`，以下示例用 Python 标准库生成请求文件，避免手动粘贴 Base64：

```bash
python - <<'PY'
import base64
import json
from pathlib import Path

body = {
    "contents": [{
        "role": "user",
        "parts": [
            {"text": "保留人物面容和衣服，把背景替换为樱花公园"},
            {"inlineData": {
                "mimeType": "image/png",
                "data": base64.b64encode(Path("input.png").read_bytes()).decode("ascii")
            }}
        ]
    }],
    "generationConfig": {
        "responseModalities": ["TEXT", "IMAGE"],
        "imageConfig": {"aspectRatio": "3:4", "imageSize": "2K"}
    }
}
Path("gemini-edit.json").write_text(json.dumps(body, ensure_ascii=False), encoding="utf-8")
PY

curl "https://subapi.duomi.cloud/v1beta/models/gemini-3-pro-image-preview:generateContent" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  --data-binary @gemini-edit.json \
  -o gemini-result.json
```

该示例需要 Python 3 和本地 `input.png`。使用 JPEG 时同时修改文件名和 `mimeType`。

多图编辑时，在同一个 `parts` 中继续追加 `inlineData`。上述三款模型最多支持 14 张参考图，人物、物体保真能力和请求体大小仍受模型及渠道限制。初次接入建议从 1～3 张开始。

多轮编辑时，保留之前的用户内容与完整 `candidates[].content`（包括 `thoughtSignature`），再追加新的 `role: "user"` 内容。只取出生成图片重新发起请求也是一次新的单轮编辑，不会自动携带原对话上下文。

### 5.5 原生返回

响应结构示例，图片内容已省略：

```json
{
  "candidates": [
    {
      "content": {
        "role": "model",
        "parts": [
          {"text": "已为你生成图片。"},
          {"inlineData": {"mimeType": "image/png", "data": "<图片Base64>"}}
        ]
      },
      "finishReason": "STOP"
    }
  ]
}
```

遍历 `candidates[].content.parts[]`，读取 `inlineData.data` 并解码，文件格式以 `inlineData.mimeType` 为准。文字片段、图片片段的数量和顺序不固定。若有 `thought: true` 的中间思考图片，不应当作最终结果保存。

HTTP 成功也可能只返回文字或内容过滤信息，不能只凭 HTTP `200` 就认定已经生成图片。

### 5.6 Gemini 异步文生图：本站兼容格式

使用 `gemini-3.1-flash-image-preview` 或 `gemini-3-pro-image-preview`。本站单任务异步入口使用 `model`、`prompt` 等字段，**不能直接提交原生 `contents` / `generationConfig` 请求体**。

```bash
curl -i "https://subapi.duomi.cloud/v1/images/generations/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-3.1-flash-image-preview",
    "prompt": "生成一张香蕉主题咖啡馆海报，中文标题：今日营业",
    "aspect_ratio": "3:4",
    "n": 1
  }'
```

使用提交返回的 `task_id` 查询文生图任务：

```bash
curl "https://subapi.duomi.cloud/v1/images/tasks/${TASK_ID}" \
  -H "Authorization: Bearer ${API_KEY}"
```

| 参数 | 类型 | 必填 | 本站 Gemini 异步行为 |
| --- | --- | --- | --- |
| `model` | string | 是 | `gemini-3.1-flash-image-preview` 或 `gemini-3-pro-image-preview`。 |
| `prompt` | string | 是 | 转换为原生 `contents[].parts[].text`。 |
| `aspect_ratio` | string | 否 | 直接转为 `imageConfig.aspectRatio`，取值需符合所选模型；推荐通过此字段指定比例。 |
| `size` | string | 否 | 兼容字段，例如 `1536x1024`。仅近似转换为 `16:9`、`3:2`、`1:1`、`2:3`、`9:16` 之一，**不控制精确像素或分辨率档位**；同时传入时 `aspect_ratio` 优先。 |
| `n` | integer | 否 | 默认 `1`，建议范围 `1`～`4`。本站通过重复生成聚合结果，超过 `4` 当前会按 `4` 次执行；不保证模型每次恰好返回一张图。 |
| `image` / `image[]` | multipart 文件 | multipart 编辑必填 | 本地图片文件，需带正确的 `image/*` Content-Type；可重复上传。 |
| `images[].image_url` | string | JSON 编辑必填 | 仅使用 `data:image/...;base64,<完整Base64>`。此转换入口不会把普通 HTTP 图片 URL 作为源图传给 Gemini，可能导致编辑退化为文生图。 |
| `stream` | boolean | 否 | 异步调用应省略或设为 `false`。JSON 中的 `true` 会被拒绝；Gemini multipart 中的 `stream=true` 当前被忽略，也不会产生流式输出。 |

**本异步入口不能设置原生 `imageConfig.imageSize`，也不传递原生多轮 `contents`、`thoughtSignature`、工具或思考配置。** `quality`、`background`、`output_format`、`input_fidelity`、`mask` 等 OpenAI 专属选项不会转换为 Gemini 生图能力；`response_format` 也不能控制 Gemini 原生输出。需要指定 `2K` / `4K` 或保留多轮上下文时，请使用 5.2～5.4 的原生同步接口。

### 5.7 Gemini 异步图像编辑

推荐直接上传本地文件：

```bash
curl -i "https://subapi.duomi.cloud/v1/images/edits/async" \
  -H "Authorization: Bearer ${API_KEY}" \
  -F "model=gemini-3.1-flash-image-preview" \
  -F "prompt=保留人物，把背景改为樱花公园" \
  -F "image[]=@./input.png;type=image/png" \
  -F "aspect_ratio=3:4" \
  -F "n=1"
```

也可向同一路径提交以下 JSON，需将占位符替换为实际 Base64：

```json
{
  "model": "gemini-3.1-flash-image-preview",
  "prompt": "保留人物，把背景改为樱花公园",
  "images": [{"image_url": "data:image/png;base64,<完整Base64>"}],
  "aspect_ratio": "3:4",
  "n": 1
}
```

此处 Data URL **需要**前缀；原生 `inlineData.data` **不要**前缀。两者不可直接互换。

使用提交返回的 `task_id` 查询编辑任务，认证使用提交时的同一个 Key：

```bash
curl "https://subapi.duomi.cloud/v1/images/tasks/${TASK_ID}" \
  -H "Authorization: Bearer ${API_KEY}"
```

异步结果使用第 6 节的 `result.data` 图片结构，不使用原生 `candidates` 结构。

## 6. 本站异步任务：提交响应、轮询与结果

三家厂商的上述 `/async` 示例都使用本节流程。异步功能需由站点启用；未启用时提交会返回 `404` 和 `async image tasks are not enabled`。

### 6.1 提交成功

HTTP `202 Accepted` 表示任务已接受，不代表图片生成成功：

```http
HTTP/1.1 202 Accepted
Location: /v1/images/tasks/imgtask_example
Retry-After: 3
Content-Type: application/json
```

```json
{
  "id": "imgtask_example",
  "task_id": "imgtask_example",
  "object": "image.generation.task",
  "status": "processing",
  "created_at": 1794000000,
  "expires_at": 1794086400,
  "poll_url": "/v1/images/tasks/imgtask_example"
}
```

任务 ID 由服务端生成。示例中的 `imgtask_example` 仅作占位。

| 字段 / 响应头 | 含义 |
| --- | --- |
| `id`、`task_id` | 同一个任务标识，保存下来用于后续查询。 |
| `object` | 固定为 `image.generation.task`，编辑任务也使用这个类型。 |
| `status` | `processing`、`completed` 或 `failed`。 |
| `created_at` | 创建时间，秒级 Unix 时间戳。 |
| `expires_at` | 任务记录过期时间，秒级 Unix 时间戳。当前默认保留 24 小时，完成后会刷新过期时间，以最新响应为准。 |
| `poll_url` / `Location` | 任务查询路径，提交响应提供。相对路径需拼接 `https://subapi.duomi.cloud`。 |
| `Retry-After` | 建议最小轮询间隔，当前为 3 秒；以响应头为准。 |

### 6.2 查询任务

必须使用**提交任务时同一个 API Key**，同一用户的另一个 Key 也不能查询：

```bash
curl -i "https://subapi.duomi.cloud/v1/images/tasks/imgtask_example" \
  -H "Authorization: Bearer ${API_KEY}"
```

有效任务的查询通常返回 HTTP `200`，包括处理中和生成失败的任务。必须读取 JSON 的 `status`：

| `status` | 调用方处理 |
| --- | --- |
| `processing` | 按 `Retry-After` 等待后继续查询。 |
| `completed` | 读取 `result.data` 中的图片并结束轮询。 |
| `failed` | 读取 `error` 和 `http_status`，结束轮询。 |

成功示例：

```json
{
  "id": "imgtask_example",
  "task_id": "imgtask_example",
  "object": "image.generation.task",
  "status": "completed",
  "http_status": 200,
  "image_url": "https://your-result-host.example/result.png",
  "result": {
    "created": 1794000060,
    "data": [{"url": "https://your-result-host.example/result.png"}]
  },
  "created_at": 1794000000,
  "completed_at": 1794000060,
  "expires_at": 1794086460
}
```

| 结果字段 | 说明 |
| --- | --- |
| `http_status` | 后台执行结果的 HTTP 状态，不是本次轮询的 HTTP 状态。 |
| `result` | 成功时的图片响应，三家在此任务入口均使用 `data` 数组。 |
| `result.data[].url` | 图片地址，通常为转存后的 URL；也可能保留渠道原始 URL。 |
| `result.data[].b64_json` | 部分直返渠道保留 Base64 图片，调用方仍需兼容。 |
| `image_url` | 第一张结果的 URL 快捷字段；没有 URL 时可省略，多图必须遍历 `result.data`。 |
| `completed_at` | 完成时间，秒级 Unix 时间戳。失败任务也可包含。 |
| `error` | 失败时的错误对象，通常包含 `type` 和 `message`。 |

本站通常将图片转存后返回 URL，但部分渠道保留原始响应，因此不要假定异步一定只有 URL。**任务记录的 `expires_at` 不等于图片 URL 失效时间**，获取结果后请及时保存。

失败示例，实际错误信息以返回为准：

```json
{
  "id": "imgtask_example",
  "task_id": "imgtask_example",
  "object": "image.generation.task",
  "status": "failed",
  "http_status": 502,
  "error": {"type": "api_error", "message": "image generation failed"},
  "created_at": 1794000000,
  "completed_at": 1794000060,
  "expires_at": 1794086460
}
```

任务不存在、过期或使用了其他 Key 查询时返回 `404`。

## 7. 常见错误与参数混用检查

| HTTP 状态 | 常见原因 | 处理方式 |
| --- | --- | --- |
| `400` | JSON 格式错误、字段不匹配、模型或尺寸不支持、原生 Gemini 分组不匹配 | 检查厂商章节，尤其是编辑图片字段与尺寸字段；按 `error.message` 修正。 |
| `401` | Key 缺失、无效或已停用 | 使用本站 Key，并检查认证请求头。 |
| `403` | 分组权限、IP 限制、内容策略或额度相关限制 | 查看错误信息，核对分组生图权限及 Key 状态；Gemini 原生生图权限错误也可能返回 `400`。 |
| `404` | 路径错误、异步未开启、任务不存在、已过期或 Key 不匹配 | 核对路径、任务 ID 和提交时的 Key。 |
| `413` | 总请求体或图片过大 | 缩小图片、减少参考图数量；Base64 会增大请求体。 |
| `429` | 频率、并发或配额限制 | 遵循 `Retry-After` 并退避；避免自动重复提交同一生图任务。 |
| `5xx` | 渠道暂不可用、上游失败或结果存储失败 | 保存任务 ID、错误正文和可用的 `x-request-id`；已有任务优先查询任务状态。 |

OpenAI、Grok 和本站任务接口通常返回 `{"error":{"type":"...","message":"..."}}`；Gemini 原生通常返回 `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"..."}}`。

最后检查最容易混用的字段：

| 目的 | OpenAI | Grok | Gemini 原生 |
| --- | --- | --- | --- |
| 指定模型 | JSON / 表单 `model` | JSON `model` | URL 中 `{model}` |
| 提示词 | `prompt` | `prompt` | `contents[].parts[].text` |
| 单图编辑输入 | multipart `image[]` 或 JSON `images[].image_url` | JSON `image.url` 和 `image.type` | `parts[].inlineData` |
| 多图编辑输入 | 重复 `image[]` 或多个 `images` 条目 | `images[].url` 和 `images[].type` | 多个 `inlineData` 片段 |
| 输出尺寸 | `size: "1536x1024"` | `aspect_ratio: "16:9"` + `resolution: "1k"` | `imageConfig.aspectRatio` + 支持模型的 `imageConfig.imageSize` |
| Base64 输入形式 | JSON 图片字段中使用 Data URL | `url` 中使用 Data URL | `inlineData.data` 中使用纯 Base64 |
| 同步图片结果 | `data[].b64_json` 或 `data[].url` | `data[].url` 或 `data[].b64_json` | `candidates[].content.parts[].inlineData` |
| 本站异步请求体 | 沿用同步图片请求体 | 沿用同步 xAI JSON | 改用第 5.6 节的本站兼容请求体 |
