# Seedance 原生 API

支持火山方舟 Ark 的异步视频任务协议，无需把 `content[]` 转换成 OpenAI `messages` 或 Grok `prompt`。

## 配置

1. 创建 **Seedance / Ark** 平台的 **API Key** 账号，填写 Ark API Key，Base URL 默认为 `https://ark.cn-beijing.volces.com/api/v3`。兼容服务可填写自己的 `/api/v3` 或 `/v3` Base URL。
2. 独立 Seedance 平台自动具有视频能力。原有 OpenAI API Key 账号勾选 **Seedance (Ark)** 的配置继续支持原生接口。
3. 将账号加入同平台分组并启用分组的「允许图片生成」媒体权限；组合分组可配置端点为「任意端点（any）」的显式模型路由。
4. 配置模型映射，例如将公开模型名 `seedance-video` 映射到实际 `doubao-seedance-*` 模型或 `ep-*` 推理接入点。在分组或渠道的模型定价中选择**按次**并填写**默认单次价格**，即可按每个成功视频任务收费；也可保留 **Token** 模式并配置输出 token 价格。本接口不使用 Grok 的按秒视频价格。

## 调用

```bash
curl "$SUB2API_BASE_URL/api/v3/contents/generations/tasks" \
  -H "Authorization: Bearer $SUB2API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "seedance-video",
    "content": [{"type": "text", "text": "海浪轻轻拍打沙滩"}],
    "duration": 5,
    "resolution": "720p",
    "ratio": "16:9",
    "generate_audio": true
  }'

# 使用创建响应中的原生 id 查询，直至 succeeded / failed / cancelled 等终态。
curl "$SUB2API_BASE_URL/api/v3/contents/generations/tasks/$TASK_ID" \
  -H "Authorization: Bearer $SUB2API_KEY"

curl -X DELETE "$SUB2API_BASE_URL/api/v3/contents/generations/tasks/$TASK_ID" \
  -H "Authorization: Bearer $SUB2API_KEY"
```

亦支持 `/v3`、`/v1` 和无版本前缀别名。Ark SDK 的 Base URL 可改为 `$SUB2API_BASE_URL/api/v3`。文本、图片、视频、音频内容、角色及扩展参数原样传递，仅按账号配置改写模型名；响应保持上游原生格式。

## 任务与计费

- 查询和删除只能访问同一用户、同一 API Key、同一分组创建的任务，并始终使用原提交账号；不会转到其他账号查询。
- 创建时不结算。成功查询或成功下载时发现 `succeeded` 后，按模型配置结算：**按次**模式收取一次单价并乘视频倍率，不依赖上游 token 用量，也不乘视频时长；**Token** 模式根据上游 `usage.completion_tokens`（缺失或为零时取 `total_tokens`）计费，未返回有效用量时等待后续查询。下载报错时该次不结算，可再次查询状态。
- 原生查询、统一查询和下载共用任务计费标识；重复访问由共享缓存声明和持久化扣费去重保护。失败、取消、排队及运行中的任务不计费。
- Redis 保存任务绑定及创建时的模型快照，默认 24 小时。需保留 Redis 状态并在有效期内查询完成结果。当前不会后台轮询；不接受 `callback_url` 和 `draft_task_id`，客户端应主动查询完成结果。
- 不开放上游的任务列表接口，防止共享账号的任务泄露给其他用户。删除遵循上游语义，不自动退款。
- 异步创建的上游错误不自动重试，以免重复创建付费任务。

协议依据：[火山官方 Go SDK](https://github.com/volcengine/volcengine-go-sdk/blob/master/service/arkruntime/model/content_generation.go)、[创建任务文档](https://www.volcengine.com/docs/82379/1520757)、[查询任务文档](https://www.volcengine.com/docs/82379/1521309)。
