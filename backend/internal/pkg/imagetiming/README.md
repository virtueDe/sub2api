# 生图阶段耗时日志

实时生图使用 INFO 级别的 `image.timing.start` / `image.timing` 日志。成功、失败、重试都记录；不需要打开 DEBUG。日志写入现有应用日志输出，不额外写入运维日志数据库。

覆盖 OpenAI Images API Key / OAuth / Responses 桥接、Gemini / Grok 的同步和异步 Images 入口，以及显式生图的 HTTP Responses、Gemini 原生图片请求。图片编辑使用同一套计时。

## 关联与字段

- 使用 `client_request_id` 或 `request_id` 查整条请求。异步执行、下载、上传、最终状态保存继续携带原请求标识及 `task_id`。
- `span_id` 在该请求内唯一；`parent_span_id` 表示调用关联，异步执行和 HTTP 响应体读取可以延续至父阶段结束以后。每次账号转发都有独立记录，切换账号后看对应的 `account_id`。
- `start_offset_ms` 是相对于首次启用该请求计时的偏移；`duration_ms` 是该阶段的墙钟耗时，支持小数毫秒。
- `outcome=error` 时记录安全分类 `error_kind`，不记录原始错误字符串、提示词、图片内容、认证头、完整图片 URL 或对象存储 key。
- 有 start 而没有结束事件说明该阶段尚未结束，或进程在执行中退出。

## 排查顺序

| 阶段 | 含义 |
|---|---|
| `request_body_read` / `request_parse` | 收取、解析请求 |
| `security_audit` / `billing_eligibility` | 内容检查、计费资格检查 |
| `image_slot_wait` / `user_slot_wait` / `concurrency_*_wait` | 并发资格检查及等待；`acquired=false` 表示未获得资格 |
| `account_select` / `account_slot_wait` / `retry_wait` | 选账号、等待账号并发、失败后退避 |
| `forward_attempt` | 一次账号转发，包括准备、上游通信、转换和回传 |
| `prepare_upstream_request` / `upstream_credentials` / `request_encode` | 准备请求、取得凭据、编码 |
| `http_transport_attempt` | Grok transport 内部主请求与 fallback 的响应头计时 |
| `http_headers` | 开始 HTTP 调用至收到完整响应头，含连接和请求发送 |
| `connection_acquire` / `dns` / `tcp_connect` / `tls_handshake` | HTTP transport 实际触发的网络阶段 |
| `request_write` / `wait_first_response_byte` | 取得连接后发送请求、发送完成后等待首字节 |
| `response_body` / `response_read` | 读取上游响应体；后者含应用读取上限检查 |
| `response_process` / `response_convert` / `response_backfill` | 本地结果转换及补全；单图失败回退看子记录的 `fallback` |
| `response_write` / `downstream_write` | 写入客户端或内部响应 recorder |
| `image_stream` / `wait_first_complete_image` | 流式处理、响应头之后首次观察到完整图片；心跳不算图片 |
| `input_image_*` / `image_url_download` / `image_base64_*` | 输入图片代理、图片下载、base64 转换 |
| `result_store` / `result_image_decode_or_download` / `result_image_upload` | 结果转存、逐图解码或下载、逐图上传 |
| `storage_put_object` / `storage_presign` | 对象存储上传、生成访问签名 |
| `task_state_read` / `task_state_save` / `task_finalize` | 异步任务状态读取、保存和最终状态 |

例如 `http_headers≈31秒`、`response_body.read_wait_ms≈130000` 表示主要等待上游响应体数据；若 `write_wait_ms≈130000` 则主要卡在向下游写入。流式响应的 body 墙钟耗时还可能包含客户端回压，需同时看读写阻塞时间。`task_finalize.task_status` 表示要保存的最终状态，只有该阶段 `outcome=ok` 才表示保存成功；生成成功但转存失败时状态为 `failed`。

## 计时口径

- 多个阶段的时间可能包含、交错或重叠，**不能把所有 duration 直接相加**。
- `response_body.body_bytes` 是该读取层实际交给应用的字节数，可能已经解压，不是网卡流量；`body_bytes_per_second` 按该阶段墙钟时间计算。`read_wait_ms` 是实际 Read 调用累计阻塞时间。
- `body_end=eof` 才代表读完；`read_error` 表示读取失败；`closed_before_eof` 表示提前关闭，不能当作完整接收。
- `downstream_write.write_wait_ms` / `flush_wait_ms` 是 Write / Flush 的累计耗时；其墙钟 duration 对流式请求可能包含等待下一段上游数据的时间。`heartbeat_bytes` 单独标识 JSON 心跳。
- `wait_first_complete_image` 在读取端观察完成事件，避免将该事件随后的客户端写入算进去；此前的流式回压仍可能延迟后续读取。
- 原生 Gemini 通过请求体中的 IMAGE 模态识别生图时，以及 Responses 显式生图入口，计时从识别后开始；图片专用入口从 handler 入口开始。
- 异步任务及 Gemini 内部适配会写入内存 recorder，此处的 downstream 不等于公网客户端。
- `async_task` 覆盖后台执行及最终处理，`task_http_status` 与任务状态对应；提交返回的 HTTP 202 不代表生成成功。
- `early_response=true` 表示响应首字节早于请求发送完毕（可能含 100 Continue），没有额外的发送后等待。
- `http_protocol` 来自真实响应的 `resp.Proto`，不是 User-Agent 或 HTTP/2 配置开关。
- 连接复用时不会出现新的 DNS/TCP/TLS 阶段。自定义 transport、插件或 TLS 指纹实现没有触发的 hook 不会伪造为 0 毫秒；`phase_completed=false` 表示未观察到该 hook 的结束。
- `connection_acquire` 包含建立连接，不能解释为纯连接池排队；通过代理时 DNS/TCP 可能对应代理端。

服务器已有日志可这样查（替换请求 UUID）：

```sh
docker logs --since 30m sub2api 2>&1 | grep '请求UUID' | grep 'image.timing'
```
