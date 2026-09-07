import { defineConfig, loadEnv, Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import checker from 'vite-plugin-checker'
import { readdirSync, readFileSync } from 'fs'
import { marked } from 'marked'
import { resolve } from 'path'

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;',
  })[character] || character)
}

function isSafeImageUrl(value: string): boolean {
  const trimmed = value.trim()
  if ((trimmed.startsWith('/') && !trimmed.startsWith('//')) || /^data:image\//i.test(trimmed)) {
    return true
  }
  try {
    const parsed = new URL(trimmed)
    return parsed.protocol === 'http:' || parsed.protocol === 'https:'
  } catch {
    return false
  }
}

function injectBranding(html: string, config: { site_name?: string; site_logo?: string }): string {
  let brandedHtml = html
  const siteName = config.site_name?.trim()
  if (siteName) {
    brandedHtml = brandedHtml.replace(
      /<title>[^<]*<\/title>/i,
      `<title>${escapeHtml(siteName)} - AI API Gateway</title>`,
    )
  }

  const siteLogo = config.site_logo?.trim()
  if (siteLogo && isSafeImageUrl(siteLogo)) {
    brandedHtml = brandedHtml.replace(
      /<link\s+rel=["']icon["'][^>]*>/i,
      `<link rel="icon" href="${escapeHtml(siteLogo)}" />`,
    )
  }
  return brandedHtml
}

/**
 * Vite 插件：开发模式下注入公开配置到 index.html
 * 与生产模式的后端注入行为保持一致，消除闪烁
 */
function injectPublicSettings(backendUrl: string): Plugin {
  return {
    name: 'inject-public-settings',
    apply: 'serve',
    transformIndexHtml: {
      order: 'pre',
      async handler(html) {
        try {
          const response = await fetch(`${backendUrl}/api/v1/settings/public`, {
            signal: AbortSignal.timeout(2000)
          })
          if (response.ok) {
            const data = await response.json()
            if (data.code === 0 && data.data) {
              const script = `<script>window.__APP_CONFIG__=${JSON.stringify(data.data)};</script>`
              return injectBranding(html, data.data).replace('</head>', `${script}\n</head>`)
            }
          }
        } catch (e) {
          console.warn('[vite] 无法获取公开配置，将回退到 API 调用:', (e as Error).message)
        }
        return html
      }
    }
  }
}

type ExternalDocument = {
  body: string
  slug: string
  title: string
  description: string
  source: string
}

type ParsedFrontMatter = {
  body: string
  description: string
  slug: string
  title: string
}

function frontMatterValue(frontMatter: string, key: string): string {
  const line = frontMatter.split(/\r?\n/).find((item) => item.trim().startsWith(`${key}:`))
  return line ? line.slice(line.indexOf(':') + 1).trim().replace(/^['"]|['"]$/g, '') : ''
}

function parseExternalDocument(sourcePath: string, source: string): ParsedFrontMatter | null {
  const match = source.match(/^---\s*\r?\n([\s\S]*?)\r?\n---\s*\r?\n?([\s\S]*)$/)
  if (!match || frontMatterValue(match[1], 'public').toLowerCase() !== 'true') {
    return null
  }

  const fileName = sourcePath.split(/[\\/]/).pop() || sourcePath
  const fallbackSlug = fileName.replace(/\.md$/i, '').toLowerCase().replace(/[^a-z0-9]+/g, '-')
  const requestedSlug = frontMatterValue(match[1], 'slug').toLowerCase()
  const slug = requestedSlug || fallbackSlug
  if (!/^[a-z0-9][a-z0-9-]*$/.test(slug)) {
    throw new Error(`Invalid public document slug: ${slug}`)
  }

  return {
    body: match[2].trim(),
    description: frontMatterValue(match[1], 'description'),
    slug,
    title: frontMatterValue(match[1], 'title') || fileName,
  }
}

function readExternalDocuments(docsDir: string): ExternalDocument[] {
  return readdirSync(docsDir, { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.toLowerCase().endsWith('.md'))
    .map((entry) => {
      const source = readFileSync(resolve(docsDir, entry.name), 'utf8')
      const document = parseExternalDocument(entry.name, source)
      if (!document) {
        return null
      }
      return {
        source,
        ...document,
      }
    })
    .filter((document): document is ExternalDocument => document !== null)
    .sort((left, right) => left.title.localeCompare(right.title, 'zh-CN'))
}

function escapeXml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&apos;',
  })[character] || character)
}

function renderExternalDocument(document: ExternalDocument, docsOrigin: string): string {
  const body = marked.parse(document.body) as string
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>${escapeHtml(document.title)} | Duomi API 文档</title>
  <meta name="description" content="${escapeHtml(document.description)}">
  <link rel="canonical" href="${escapeHtml(docsOrigin)}/docs/${document.slug}/">
  <meta name="robots" content="index,follow">
  <style>body{margin:0;color:#1f2937;background:#f9fafb;font:16px/1.75 system-ui,sans-serif}main{max-width:960px;margin:0 auto;padding:48px 24px}article{background:#fff;padding:32px 40px;border:1px solid #e5e7eb;border-radius:8px}h1,h2,h3{line-height:1.3}pre{overflow:auto;padding:16px;background:#111827;color:#f9fafb;border-radius:6px}code{font-family:ui-monospace,SFMono-Regular,Consolas,monospace}table{border-collapse:collapse;display:block;overflow:auto}th,td{border:1px solid #d1d5db;padding:8px 12px;text-align:left}@media(max-width:640px){main{padding:24px 12px}article{padding:24px 18px}}</style>
</head>
<body><main><article>${body}</article></main></body>
</html>
`
}

function renderExternalIndex(documents: ExternalDocument[], docsOrigin: string): string {
  const links = documents.map((document) => `<li><a href="/docs/${document.slug}/">${escapeHtml(document.title)}</a>${document.description ? `: ${escapeHtml(document.description)}` : ''}</li>`).join('')
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Duomi API 文档</title><link rel="canonical" href="${escapeHtml(docsOrigin)}/docs/"><meta name="robots" content="index,follow"></head><body><main><h1>Duomi API 文档</h1><ul>${links}</ul></main></body></html>`
}

function emitExternalDocs(publicDocsOrigin: string): Plugin {
  const docsDir = resolve(__dirname, '../docs/external')
  const docsOrigin = (publicDocsOrigin || 'https://subapi.duomi.cloud').replace(/\/$/, '')

  return {
    name: 'emit-external-docs',
    apply: 'build',
    generateBundle() {
      const documents = readExternalDocuments(docsDir)
      const slugs = new Set<string>()
      for (const document of documents) {
        if (slugs.has(document.slug)) {
          throw new Error(`Duplicate public document slug: ${document.slug}`)
        }
        slugs.add(document.slug)
      }
      const sitemapEntries = documents
        .map((document) => `  <url><loc>${escapeXml(docsOrigin)}/docs/${document.slug}/</loc></url>`)
        .join('\n')
      const llmsEntries = documents
        .map((document) => `- [${document.title}](${docsOrigin}/docs/${document.slug}/): ${document.description}`)
        .join('\n')

      this.emitFile({
        type: 'asset',
        fileName: 'docs/index.html',
        source: renderExternalIndex(documents, docsOrigin),
      })
      for (const document of documents) {
        this.emitFile({
          type: 'asset',
          fileName: `docs/${document.slug}.md`,
          source: document.source,
        })
        this.emitFile({
          type: 'asset',
          fileName: `docs/${document.slug}/index.html`,
          source: renderExternalDocument(document, docsOrigin),
        })
      }
      this.emitFile({
        type: 'asset',
        fileName: 'docs/index.json',
        source: JSON.stringify(documents.map(({ slug, title, description }) => ({ slug, title, description })), null, 2),
      })
      this.emitFile({
        type: 'asset',
        fileName: 'sitemap.xml',
        source: `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n  <url><loc>${escapeXml(docsOrigin)}/docs/</loc></url>\n${sitemapEntries}\n</urlset>\n`,
      })
      this.emitFile({
        type: 'asset',
        fileName: 'llms.txt',
        source: `# Duomi API 文档\n\n${llmsEntries}\n`,
      })
      this.emitFile({
        type: 'asset',
        fileName: 'llms-full.txt',
        source: documents.map((document) => `# ${document.title}\n\n${document.body}\n`).join('\n'),
      })
      this.emitFile({
        type: 'asset',
        fileName: 'robots.txt',
        source: `User-agent: *\nAllow: /docs/\nAllow: /llms.txt\nAllow: /llms-full.txt\nAllow: /sitemap.xml\nDisallow: /api/\nSitemap: ${docsOrigin}/sitemap.xml\n`,
      })
    },
  }
}

export default defineConfig(({ mode }) => {
  // 加载环境变量
  const env = loadEnv(mode, process.cwd(), '')
  const backendUrl = env.VITE_DEV_PROXY_TARGET || 'http://localhost:8080'
  const devPort = Number(env.VITE_DEV_PORT || 3000)
  const publicDocsOrigin = env.VITE_PUBLIC_DOCS_ORIGIN || 'https://subapi.duomi.cloud'

  return {
    plugins: [
      vue(),
      checker({
        vueTsc: true
      }),
      injectPublicSettings(backendUrl),
      emitExternalDocs(publicDocsOrigin)
    ],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
      // 使用 vue-i18n 运行时版本，避免 CSP unsafe-eval 问题
      'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'
    }
  },
  define: {
    // 启用 vue-i18n JIT 编译，在 CSP 环境下处理消息插值
    // JIT 编译器生成 AST 对象而非 JS 代码，无需 unsafe-eval
    __INTLIFY_JIT_COMPILATION__: true
  },
  build: {
    outDir: '../backend/internal/web/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        /**
         * 手动分包配置
         * 分离第三方库并按功能合并应用代码，避免循环依赖
         */
        manualChunks(id: string) {
          if (id.includes('node_modules')) {
            // Vue 核心库
            if (
              id.includes('/vue/') ||
              id.includes('/vue-router/') ||
              id.includes('/pinia/') ||
              id.includes('/@vue/')
            ) {
              return 'vendor-vue'
            }

            // UI 工具库（较大，单独分离）
            if (id.includes('/@vueuse/') || id.includes('/xlsx/')) {
              return 'vendor-ui'
            }

            // 图表库
            if (id.includes('/chart.js/') || id.includes('/vue-chartjs/')) {
              return 'vendor-chart'
            }

            // 国际化
            if (id.includes('/vue-i18n/') || id.includes('/@intlify/')) {
              return 'vendor-i18n'
            }

            // Stripe 仅在支付流程中按需加载，避免进入首页公共依赖。
            if (id.includes('/@stripe/stripe-js/')) {
              return 'vendor-stripe'
            }

            // 其他小型第三方库合并
            return 'vendor-misc'
          }

          // 应用代码：按入口点自动分包，不手动干预
          // 这样可以避免循环依赖，同时保持合理的 chunk 数量
        }
      }
    }
  },
    server: {
      host: '0.0.0.0',
      port: devPort,
      proxy: {
        '/api': {
          target: backendUrl,
          changeOrigin: true
        },
        '/v1': {
          target: backendUrl,
          changeOrigin: true
        },
        '/setup': {
          target: backendUrl,
          changeOrigin: true
        }
      }
    }
  }
})
