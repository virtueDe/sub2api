<template>
  <div class="space-y-4 text-sm" data-testid="image-generation-detail">
    <p v-if="!detail" class="rounded-lg bg-gray-50 p-4 text-gray-500 dark:bg-dark-900 dark:text-dark-400">
      {{ t('usage.imageDetail.notRecorded') }}
    </p>
    <template v-else>
      <div>
        <h3 class="font-medium text-gray-700 dark:text-dark-200">{{ t('usage.imageDetail.prompt') }}</h3>
        <pre class="mt-2 max-h-60 overflow-auto whitespace-pre-wrap break-words rounded-lg bg-gray-50 p-3 dark:bg-dark-900">{{ detail.prompt || '-' }}</pre>
      </div>
      <div>
        <h3 class="font-medium text-gray-700 dark:text-dark-200">{{ t('usage.imageDetail.requestParams') }}</h3>
        <pre class="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-900">{{ JSON.stringify(detail.request_params, null, 2) }}</pre>
      </div>
      <div>
        <h3 class="font-medium text-gray-700 dark:text-dark-200">{{ t('usage.imageDetail.images') }}</h3>
        <p v-if="!detail.images?.length" class="mt-2 text-gray-500 dark:text-dark-400">{{ t('usage.imageDetail.noImages') }}</p>
        <div v-else class="mt-2 grid grid-cols-1 gap-4 sm:grid-cols-2">
          <figure v-for="(image, index) in detail.images" :key="index" class="min-w-0 rounded-lg border border-gray-200 p-3 dark:border-dark-700">
            <img v-if="imageSource(image) && !failedImages.has(index)" :src="imageSource(image)" :alt="t('usage.imageDetail.imageAlt', { index: index + 1 })" class="max-h-80 w-full rounded object-contain" loading="lazy" referrerpolicy="no-referrer" @error="markFailed(index)">
            <p v-else class="py-8 text-center text-gray-500">{{ t('usage.imageDetail.imageUnavailable') }}</p>
            <figcaption v-if="image.partial || image.revised_prompt" class="mt-2 whitespace-pre-wrap break-words text-gray-500 dark:text-dark-400">
              <span v-if="image.partial" class="mb-1 block text-amber-600">{{ t('usage.imageDetail.partial') }}</span>
              {{ image.revised_prompt }}
            </figcaption>
          </figure>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { GeneratedImage, ImageGenerationDetail } from '@/types'

const props = defineProps<{ detail?: ImageGenerationDetail | null }>()
const { t } = useI18n()
const failedImages = ref(new Set<number>())
watch(() => props.detail, () => { failedImages.value = new Set() })

function markFailed(index: number) {
  failedImages.value = new Set([...failedImages.value, index])
}

function imageSource(image: GeneratedImage): string {
  if (image.b64_json) {
    const mime = /^image\/(png|jpeg|webp|gif)$/.test(image.mime_type || '') ? image.mime_type : 'image/png'
    return `data:${mime};base64,${image.b64_json}`
  }
  if (image.url) {
    try {
      const url = new URL(image.url)
      if (url.protocol === 'https:' || url.protocol === 'http:') return image.url
    } catch { /* An unavailable image is displayed explicitly below. */ }
  }
  return ''
}
</script>
