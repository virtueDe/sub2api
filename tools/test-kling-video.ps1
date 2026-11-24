[CmdletBinding()]
param(
    [string]$BaseUrl = $env:SUB2API_BASE_URL,
    [string]$ApiKey = $env:SUB2API_API_KEY,
    [string]$Model = "kling-v4.0-flash",
    [string]$Prompt = "参考图片生成一个动感的骑车视频",
    [string]$ImageUrl = "https://img.duomi.cloud/wx/a2cd761b862c4cf9d6ea5bea45da8c6eafbac94a_2_750x750.jpeg",
    [string]$Seconds = "5",
    [string]$Size = "1280x720",
    [int]$PollIntervalSeconds = 10,
    [int]$TimeoutMinutes = 30,
    [string]$TaskId,
    [string]$RequestFile,
    [string]$OutputPath = (Join-Path ([System.IO.Path]::GetTempPath()) ("kling-video-{0}.mp4" -f (Get-Date -Format "yyyyMMdd-HHmmss"))),
    [switch]$SkipDownload
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($BaseUrl)) {
    throw "Set SUB2API_BASE_URL or pass -BaseUrl."
}
if ([string]::IsNullOrWhiteSpace($ApiKey)) {
    throw "Set SUB2API_API_KEY or pass -ApiKey."
}
if ([string]::IsNullOrWhiteSpace($ImageUrl)) {
    throw "ImageUrl is required for this single-frame image-to-video test."
}
if ($PollIntervalSeconds -lt 1 -or $TimeoutMinutes -lt 1) {
    throw "Polling interval and timeout must be positive."
}

$base = $BaseUrl.TrimEnd('/')
$client = [System.Net.Http.HttpClient]::new()
$client.Timeout = [TimeSpan]::FromMinutes($TimeoutMinutes + 2)
$client.DefaultRequestHeaders.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new("Bearer", $ApiKey)
$client.DefaultRequestHeaders.Accept.ParseAdd("application/json")

function Invoke-VideoJsonRequest {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Uri,
        [string]$JsonBody
    )

    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::new($Method), $Uri)
    if ($null -ne $JsonBody) {
        $request.Content = [System.Net.Http.StringContent]::new($JsonBody, [System.Text.Encoding]::UTF8, "application/json")
    }
    try {
        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        $content = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        return [pscustomobject]@{
            StatusCode = [int]$response.StatusCode
            ContentType = if ($null -ne $response.Content.Headers.ContentType) { $response.Content.Headers.ContentType.ToString() } else { "" }
            Body = $content
        }
    }
    finally {
        $request.Dispose()
        if ($null -ne $response) { $response.Dispose() }
    }
}

function Convert-ResponseJson {
    param([Parameter(Mandatory = $true)][string]$Body)
    try {
        return ConvertFrom-Json -InputObject $Body
    }
    catch {
        return $null
    }
}

function Write-JsonResult {
    param([string]$Label, [int]$StatusCode, [string]$Body)
    Write-Host "$Label HTTP $StatusCode"
    $json = Convert-ResponseJson -Body $Body
    if ($null -ne $json) {
        Write-Host ($json | ConvertTo-Json -Depth 30)
    }
    else {
        Write-Host $Body
    }
}

function Save-VideoContent {
    param([Parameter(Mandatory = $true)][string]$Uri, [Parameter(Mandatory = $true)][string]$Path, [switch]$PublicUrl)

    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Get, $Uri)
    $downloadClient = $client
    if ($PublicUrl) {
        $downloadClient = [System.Net.Http.HttpClient]::new()
        $downloadClient.Timeout = [TimeSpan]::FromMinutes(5)
    }
    try {
        $response = $downloadClient.SendAsync($request, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        if (-not $response.IsSuccessStatusCode) {
            $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            Write-JsonResult -Label "Download" -StatusCode ([int]$response.StatusCode) -Body $body
            throw "Video content download failed."
        }

        $directory = Split-Path -Parent $Path
        if ($directory -and -not (Test-Path -LiteralPath $directory)) {
            New-Item -ItemType Directory -Path $directory -Force | Out-Null
        }
        $inputStream = $response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $outputStream = [System.IO.File]::Create($Path)
        try {
            $inputStream.CopyTo($outputStream)
        }
        finally {
            $outputStream.Dispose()
            $inputStream.Dispose()
        }

        $contentLength = $response.Content.Headers.ContentLength
        Write-Host "Download HTTP $([int]$response.StatusCode)"
        Write-Host "Content-Type: $($response.Content.Headers.ContentType)"
        Write-Host "Content-Length: $contentLength"
        Write-Host "Saved: $Path"
    }
    finally {
        $request.Dispose()
        if ($null -ne $response) { $response.Dispose() }
        if ($PublicUrl) { $downloadClient.Dispose() }
    }
}

$payload = [ordered]@{
    model = $Model
    prompt = $Prompt
    seconds = $Seconds
    size = $Size
    input_reference = $ImageUrl
}
$jsonBody = $payload | ConvertTo-Json -Depth 20 -Compress
if ($RequestFile) {
    $jsonBody = Get-Content -LiteralPath $RequestFile -Raw
    $payload = $jsonBody | ConvertFrom-Json
}

if (-not $TaskId) {
    Write-Host "POST $base/v1/videos"
    Write-Host "Authorization: Bearer <redacted>"
    Write-Host "Request JSON:"
    Write-Host ($payload | ConvertTo-Json -Depth 20)

    $submit = Invoke-VideoJsonRequest -Method "POST" -Uri "$base/v1/videos" -JsonBody $jsonBody
    Write-JsonResult -Label "Submit" -StatusCode $submit.StatusCode -Body $submit.Body
    $task = Convert-ResponseJson -Body $submit.Body
    if ($submit.StatusCode -lt 200 -or $submit.StatusCode -ge 300) {
        if ($submit.StatusCode -eq 409 -and $task.data.task_id) {
            $TaskId = [string]$task.data.task_id
            Write-Host "Submission unconfirmed; polling existing task: $TaskId"
        }
        else {
            throw "Video submission failed; no automatic resubmission was attempted."
        }
    }

    if (-not $TaskId -and ($null -eq $task -or [string]::IsNullOrWhiteSpace([string]$task.id))) {
        throw "Submit response did not contain an id; inspect the response above before retrying."
    }
    if (-not $TaskId) { $TaskId = [string]$task.id }
}
$taskId = $TaskId
$encodedId = [Uri]::EscapeDataString($taskId)
$statusUri = "$base/v1/videos/$encodedId"
$deadline = [DateTime]::UtcNow.AddMinutes($TimeoutMinutes)

do {
    Start-Sleep -Seconds $PollIntervalSeconds
    $poll = Invoke-VideoJsonRequest -Method "GET" -Uri $statusUri
    Write-JsonResult -Label "Poll" -StatusCode $poll.StatusCode -Body $poll.Body
    if ($poll.StatusCode -lt 200 -or $poll.StatusCode -ge 300) {
        throw "Video status query failed; task id $taskId was not resubmitted."
    }
    $task = Convert-ResponseJson -Body $poll.Body
    if ($null -eq $task) {
        throw "Video status response was not valid JSON."
    }
    $status = [string]$task.status
    if ($status -eq "failed") {
        throw "Video generation failed for task id $taskId."
    }
    if ($status -notin @("queued", "in_progress", "completed")) {
        throw "Unexpected video status '$status' for task id $taskId."
    }
    if ([DateTime]::UtcNow -ge $deadline -and $status -ne "completed") {
        throw "Timed out waiting for task id $taskId. Continue polling this id; do not submit it again."
    }
} while ($status -ne "completed")

if (-not $SkipDownload) {
    $contentUri = "$base/v1/videos/$encodedId/content"
    $isPublicUrl = [string]$task.url -like 'https://*'
    if ($isPublicUrl) { $contentUri = [string]$task.url }
    Write-Host "GET $contentUri"
    if (-not $isPublicUrl) { Write-Host "Authorization: Bearer <redacted>" }
    Save-VideoContent -Uri $contentUri -Path $OutputPath -PublicUrl:$isPublicUrl
}

$client.Dispose()
