import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const PORT = Number(process.env.PORT || 3099)
const BASE_URL = (process.env.IMAGE_API_BASE_URL || 'https://imgapi.duomi.cloud').replace(/\/$/, '')
const API_KEY = process.env.IMAGE_API_KEY || ''
const POLL_INTERVAL_MS = Number(process.env.POLL_INTERVAL_MS || 3000)
const POLL_TIMEOUT_MS = Number(process.env.POLL_TIMEOUT_MS || 600000)
const MAX_BODY_BYTES = 2 * 1024 * 1024

const jsonHeaders = { 'content-type': 'application/json; charset=utf-8' }

function writeJson(res, status, body) {
  res.writeHead(status, jsonHeaders)
  res.end(JSON.stringify(body, null, 2))
}

function apiError(message, details = {}) {
  return { error: message, ...details }
}

async function readJson(req) {
  const chunks = []
  let size = 0
  for await (const chunk of req) {
    size += chunk.length
    if (size > MAX_BODY_BYTES) throw new Error(`request body exceeds ${MAX_BODY_BYTES} bytes`)
    chunks.push(chunk)
  }
  const raw = Buffer.concat(chunks).toString('utf8')
  if (!raw.trim()) return {}
  return JSON.parse(raw)
}

function upstreamHeaders() {
  if (!API_KEY) throw new Error('IMAGE_API_KEY is not configured')
  return {
    authorization: `Bearer ${API_KEY}`,
    accept: 'application/json',
  }
}

async function callUpstream(endpoint, init = {}) {
  const startedAt = Date.now()
  const response = await fetch(`${BASE_URL}${endpoint}`, {
    ...init,
    headers: {
      ...upstreamHeaders(),
      ...(init.headers || {}),
    },
  })
  const text = await response.text()
  let body
  try {
    body = text ? JSON.parse(text) : null
  } catch {
    body = text
  }
  return {
    status: response.status,
    ok: response.ok,
    body,
    elapsed_ms: Date.now() - startedAt,
    request_id: response.headers.get('x-request-id'),
  }
}

async function pollTask(taskId) {
  const startedAt = Date.now()
  const history = []
  while (Date.now() - startedAt <= POLL_TIMEOUT_MS) {
    const result = await callUpstream(`/v1/jobs/${encodeURIComponent(taskId)}`)
    const status = result.body?.status
    history.push({ status, http_status: result.status, elapsed_ms: result.elapsed_ms })
    if (!result.ok || ['completed', 'succeeded', 'failed', 'canceled', 'cancelled', 'expired'].includes(status)) {
      return {
        ...result,
        poll_elapsed_ms: Date.now() - startedAt,
        history,
      }
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
  }
  return {
    status: 408,
    ok: false,
    body: apiError('poll timeout', { task_id: taskId }),
    poll_elapsed_ms: Date.now() - startedAt,
    history,
  }
}

async function handleGenerate(req, res, url) {
  const input = await readJson(req)
  const isAsync = url.pathname.endsWith('/async')
  const wait = String(url.searchParams.get('wait') || '').toLowerCase() === 'true'
  const endpoint = isAsync ? '/v1/generate/async' : '/v1/generate'
  const result = await callUpstream(endpoint, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({
      model: input.model || 'gpt-image-2',
      prompt: input.prompt,
      size: input.size,
      quality: input.quality,
      n: input.n,
      output_format: input.output_format,
      background: input.background,
    }),
  })
  if (isAsync && wait && result.ok && result.body?.task_id) {
    result.poll = await pollTask(result.body.task_id)
  }
  writeJson(res, result.status, result)
}

async function handleEdit(req, res) {
  const contentType = req.headers['content-type'] || ''
  if (!contentType.toLowerCase().startsWith('multipart/form-data')) {
    writeJson(res, 415, apiError('edit requires multipart/form-data', {
      expected_fields: ['prompt', 'image'],
    }))
    return
  }
  const body = await readRawBody(req)
  const result = await callUpstream('/v1/edit', {
    method: 'POST',
    headers: { 'content-type': contentType },
    body,
  })
  writeJson(res, result.status, result)
}

async function readRawBody(req) {
  const chunks = []
  let size = 0
  for await (const chunk of req) {
    size += chunk.length
    if (size > 25 * 1024 * 1024) throw new Error('image edit body exceeds 25 MB')
    chunks.push(chunk)
  }
  return Buffer.concat(chunks)
}

async function serveIndex(res) {
  const indexPath = path.join(path.dirname(fileURLToPath(import.meta.url)), 'index.html')
  const html = await readFile(indexPath, 'utf8')
  res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' })
  res.end(html)
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url || '/', `http://${req.headers.host || 'localhost'}`)
  try {
    if (req.method === 'GET' && url.pathname === '/') {
      await serveIndex(res)
      return
    }
    if (req.method === 'GET' && url.pathname === '/health') {
      writeJson(res, 200, {
        status: 'ok',
        upstream: BASE_URL,
        key_configured: Boolean(API_KEY),
      })
      return
    }
    if (req.method === 'POST' && ['/generate', '/generate/async'].includes(url.pathname)) {
      await handleGenerate(req, res, url)
      return
    }
    if (req.method === 'POST' && url.pathname === '/edit') {
      await handleEdit(req, res)
      return
    }
    writeJson(res, 404, apiError('route not found'))
  } catch (error) {
    writeJson(res, 500, apiError(error instanceof Error ? error.message : String(error)))
  }
})

server.listen(PORT, () => {
  console.log(`image-api-smoke listening on http://localhost:${PORT}`)
  console.log(`upstream: ${BASE_URL}`)
  console.log(`api key: ${API_KEY ? 'configured' : 'missing'}`)
})
