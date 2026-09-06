# Image API Smoke Test

独立的 Node 20 验证服务，用来模拟第三方程序调用 `imgapi.duomi.cloud`。

## 启动

PowerShell：

```powershell
cd tools/image-api-smoke
$env:IMAGE_API_KEY = "你的生图分组 API Key"
$env:IMAGE_API_BASE_URL = "https://imgapi.duomi.cloud"
npm start
```

服务默认监听 `http://localhost:3099`。Key 只从环境变量读取，不写入项目文件。

## 同步生图

```powershell
Invoke-RestMethod `
  -Uri "http://localhost:3099/generate" `
  -Method Post `
  -ContentType "application/json" `
  -Body (@{
    model = "gpt-image-2"
    prompt = "一只戴着宇航员头盔的橘猫，电影级光影"
    size = "1024x1024"
    quality = "high"
  } | ConvertTo-Json)
```

## 异步生图并自动轮询

```powershell
Invoke-RestMethod `
  -Uri "http://localhost:3099/generate/async?wait=true" `
  -Method Post `
  -ContentType "application/json" `
  -Body (@{
    model = "gpt-image-2"
    prompt = "一只戴着宇航员头盔的橘猫，电影级光影"
    size = "1024x1024"
    quality = "high"
  } | ConvertTo-Json)
```

不带 `wait=true` 时只提交任务；带上后服务会轮询 `/v1/jobs/{task_id}`，直到完成或超时。

## 图改图

```powershell
curl.exe -X POST "http://localhost:3099/edit" `
  -H "Content-Type: multipart/form-data" `
  -F "model=gpt-image-2" `
  -F "prompt=把背景改成日落海边" `
  -F "image=@C:\path\to\input.png"
```

服务会原样转发 multipart 请求到 `https://imgapi.duomi.cloud/v1/edit`。
