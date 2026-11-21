# 第三方 OpenAI 兼容生图

适用于提供 OpenAI Images API 的第三方服务商。模型名不需要属于 GPT、Gemini 或 Grok 家族，后续模型通过配置接入，无需增加代码白名单。

## 配置

1. 创建或编辑 **OpenAI / API Key** 账号，填写第三方 Base URL 和密钥。例如 `https://provider.example/v1`，网关会请求其 `/images/generations` 或 `/images/edits`。
2. 在“端点能力”中开启 **OpenAI 兼容生图**，在“上游图片模型 ID”中填写上游实际接受的模型名，每行一个或以逗号分隔，例如：

   ```text
   seedream-5.5
   vendor-art-v9
   ```

3. 客户端需要使用别名时，在现有“模型映射”中配置，例如 `paint → seedream-5.5`。图片模型列表填写的是**最终上游模型 ID**。模型映射原有白名单规则仍然生效；存在映射时，请求需命中白名单或映射。图片模型声明使用精确 ID，不支持通配符；模型映射仍支持通配符。
4. 将账号绑定到允许生图的分组；若分组启用了模型白名单，放行客户端请求的模型名。在渠道/分组现有定价中配置该模型的图片单价；需要强制按张收费时，启用分组原有“按张计费”选项。
5. 打开账号测试弹窗，选择配置的图片模型或映射别名，输入提示词并测试。标准 `url` 与 `b64_json` 结果均可预览；没有图片的响应会报告失败。

开启该能力后，账号仅承接列表中声明的上游图片模型。混合文字与图片的账号可以同时开启文本能力；图片模型列表不会把视觉输入模型当成生图模型。OAuth 和 Setup Token 不能开启此模式，仍使用原有原生图片能力。

批量编辑可以统一启用该能力并替换图片模型列表；列表留空时保留各账号已有的配置。每个开启兼容生图的账号都必须有 Base URL 和至少一个图片模型。

## 请求示例

```json
{
  "model": "seedream-5.5",
  "prompt": "画一只坐在窗边的橘猫",
  "size": "2K",
  "response_format": "url"
}
```

同步请求发送至 `POST /v1/images/generations`。异步请求将路径改为 `POST /v1/images/generations/async`，收到 202 后访问响应中的 `poll_url`。编辑使用 `/v1/images/edits` 或其 `/async` 版本。

非流式标准响应采用以下一种格式：

```json
{"data":[{"url":"https://provider.example/output.png"}]}
```

```json
{"data":[{"b64_json":"..."}]}
```

图片数量、模型和用量沿用现有日志与计费流程。账号 URL 转 Base64、对象存储、异步结果存储和返回透传等设置继续生效。未被网关消费的供应商 JSON 字段沿原始请求转发；模型映射会改写 `model`。

## 兼容范围

上游必须兼容 OpenAI Images API 的端点、鉴权、请求体和返回格式。`size` 等参数的实际取值由服务商决定。此模式不把 Chat Completions 的图片输出、自定义任务轮询或原生供应商接口自动转换成 Images API，也不把第三方模型交给 Codex 的 `image_generation` 工具。同步流式请求仍要求现有网关支持的 Images SSE 格式，异步提交不接受 `stream: true`。

升级服务后一次性配置该能力；以后同协议的新模型只需补充账号模型声明、映射和价格。
