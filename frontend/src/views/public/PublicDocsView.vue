<template>
  <div class="docs-shell min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-gray-100">
    <header class="docs-header border-b border-gray-200 bg-white/95 dark:border-dark-800 dark:bg-dark-900/95">
      <div class="mx-auto flex max-w-7xl items-center justify-between gap-4 px-4 py-4 sm:px-6 lg:px-8">
        <RouterLink to="/docs" class="flex min-w-0 items-center gap-3">
          <span class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg bg-primary-600 text-sm font-bold text-white">D</span>
          <span class="truncate text-base font-semibold text-gray-950 dark:text-white">Duomi API 文档</span>
        </RouterLink>
        <a
          href="https://subapi.duomi.cloud/"
          class="flex-shrink-0 text-sm font-medium text-primary-600 transition hover:text-primary-700 dark:text-primary-300 dark:hover:text-primary-200"
        >
          创建 API Key
        </a>
      </div>
    </header>

    <div class="mx-auto flex max-w-7xl items-start gap-8 px-4 py-8 sm:px-6 lg:px-8">
      <aside class="docs-sidebar hidden w-60 flex-shrink-0 lg:block">
        <p class="mb-3 px-3 text-xs font-semibold uppercase tracking-wider text-gray-500 dark:text-dark-400">文档目录</p>
        <nav class="space-y-1" aria-label="文档目录">
          <RouterLink
            v-for="document in documents"
            :key="document.slug"
            :to="{ name: 'PublicDocs', params: { slug: document.slug } }"
            class="block rounded-lg px-3 py-2 text-sm transition"
            :class="document.slug === currentSlug
              ? 'bg-primary-50 font-semibold text-primary-700 dark:bg-primary-500/10 dark:text-primary-300'
              : 'text-gray-600 hover:bg-gray-100 hover:text-gray-950 dark:text-dark-300 dark:hover:bg-dark-800 dark:hover:text-white'"
          >
            {{ document.title }}
          </RouterLink>
        </nav>
      </aside>

      <main class="min-w-0 flex-1">
        <div v-if="documents.length === 0" class="rounded-xl border border-gray-200 bg-white p-8 dark:border-dark-700 dark:bg-dark-900">
          <h1 class="text-xl font-semibold">暂无公开文档</h1>
          <p class="mt-2 text-sm text-gray-600 dark:text-dark-300">文档正在准备中。</p>
        </div>

        <article v-else class="docs-article rounded-xl border border-gray-200 bg-white px-5 py-7 shadow-sm dark:border-dark-700 dark:bg-dark-900 sm:px-8 sm:py-9">
          <div class="mb-8 border-b border-gray-200 pb-6 dark:border-dark-700">
            <p class="text-sm font-medium text-primary-600 dark:text-primary-300">Duomi API</p>
            <h1 class="mt-2 text-3xl font-bold tracking-tight text-gray-950 dark:text-white">{{ currentDocument.title }}</h1>
            <p v-if="currentDocument.description" class="mt-3 text-base text-gray-600 dark:text-dark-300">{{ currentDocument.description }}</p>
          </div>
          <div class="docs-content" v-html="renderedHtml"></div>
        </article>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { marked } from 'marked'
import DOMPurify from 'dompurify'

type PublicDocument = {
  slug: string
  title: string
  description: string
  content: string
}

const route = useRoute()

marked.setOptions({
  breaks: true,
  gfm: true,
})

const markdownModules = import.meta.glob('../../../../docs/external/*.md', {
  query: '?raw',
  import: 'default',
  eager: true,
}) as Record<string, string>

function frontMatterValue(frontMatter: string, key: string): string {
  const line = frontMatter.split(/\r?\n/).find((item) => item.trim().startsWith(`${key}:`))
  return line ? line.slice(line.indexOf(':') + 1).trim().replace(/^['"]|['"]$/g, '') : ''
}

function parseDocument(sourcePath: string, raw: string): PublicDocument | null {
  const match = raw.match(/^---\s*\r?\n([\s\S]*?)\r?\n---\s*\r?\n?([\s\S]*)$/)
  if (!match || frontMatterValue(match[1], 'public').toLowerCase() !== 'true') {
    return null
  }

  const fileName = sourcePath.split('/').pop() || ''
  const fallbackSlug = fileName.replace(/\.md$/i, '').toLowerCase().replace(/[^a-z0-9]+/g, '-')
  const content = match[2].trim()
  const heading = content.match(/^#\s+(.+)$/m)?.[1]?.trim() || fileName
  return {
    slug: frontMatterValue(match[1], 'slug') || fallbackSlug,
    title: frontMatterValue(match[1], 'title') || heading,
    description: frontMatterValue(match[1], 'description'),
    content,
  }
}

const documents = computed(() => Object.entries(markdownModules)
  .map(([sourcePath, raw]) => parseDocument(sourcePath, raw))
  .filter((document): document is PublicDocument => document !== null)
  .sort((left, right) => left.title.localeCompare(right.title, 'zh-CN')))

const currentSlug = computed(() => {
  const requested = String(route.params.slug || '')
  return documents.value.some((document) => document.slug === requested)
    ? requested
    : documents.value[0]?.slug || ''
})

const currentDocument = computed(() => documents.value.find((document) => document.slug === currentSlug.value) || {
  slug: '',
  title: '暂无公开文档',
  description: '',
  content: '',
})

const renderedHtml = computed(() => DOMPurify.sanitize(marked.parse(currentDocument.value.content) as string))
</script>

<style scoped>
.docs-content {
  line-height: 1.75;
  overflow-wrap: anywhere;
}

.docs-content :deep(h1) {
  @apply mb-4 mt-8 border-b border-gray-200 pb-3 text-3xl font-bold dark:border-dark-700;
}

.docs-content :deep(h2) {
  @apply mb-3 mt-8 text-2xl font-bold text-gray-950 dark:text-white;
}

.docs-content :deep(h3) {
  @apply mb-2 mt-6 text-xl font-semibold text-gray-950 dark:text-white;
}

.docs-content :deep(p) {
  @apply mb-4 text-gray-700 dark:text-dark-200;
}

.docs-content :deep(a) {
  @apply text-primary-600 underline underline-offset-4 hover:text-primary-700 dark:text-primary-300 dark:hover:text-primary-200;
}

.docs-content :deep(ul) {
  @apply mb-4 list-disc pl-6;
}

.docs-content :deep(ol) {
  @apply mb-4 list-decimal pl-6;
}

.docs-content :deep(li) {
  @apply mb-1 text-gray-700 dark:text-dark-200;
}

.docs-content :deep(code) {
  @apply rounded bg-gray-100 px-1.5 py-0.5 font-mono text-sm dark:bg-dark-800;
}

.docs-content :deep(pre) {
  @apply my-5 overflow-x-auto rounded-lg bg-gray-950 p-4 text-sm text-gray-100;
}

.docs-content :deep(pre code) {
  @apply bg-transparent p-0 text-inherit;
}

.docs-content :deep(table) {
  @apply my-5 block w-full overflow-x-auto border-collapse;
}

.docs-content :deep(th) {
  @apply border border-gray-300 bg-gray-50 px-3 py-2 text-left font-semibold dark:border-dark-600 dark:bg-dark-800;
}

.docs-content :deep(td) {
  @apply border border-gray-300 px-3 py-2 dark:border-dark-600;
}

.docs-content :deep(blockquote) {
  @apply my-5 border-l-4 border-primary-300 pl-4 text-gray-600 dark:border-primary-500 dark:text-dark-300;
}
</style>
