<template>
  <BaseDialog :show="show" :title="t('usage.imageDetail.title')" width="wide" @close="emit('update:show', false)">
    <div v-if="loading" class="py-10 text-center text-gray-500">{{ t('common.loading') }}</div>
    <p v-else-if="loadError" role="alert" class="py-8 text-center text-red-500">{{ t('usage.imageDetail.loadFailed') }}</p>
    <div v-else-if="record" class="space-y-4">
      <div class="grid grid-cols-2 gap-4 text-sm text-gray-700 dark:text-dark-200">
        <div>{{ t('usage.model') }}<p class="mt-1 font-medium">{{ record.model }}</p></div>
        <div>{{ t('usage.time') }}<p class="mt-1">{{ formatDateTime(record.created_at) }}</p></div>
        <div>{{ t('admin.ops.errorDetail.requestType') }}<p class="mt-1">{{ t(requestTypeLabelKey(resolveUsageRequestType(record))) }}</p></div>
      </div>
      <ImageGenerationDetail :detail="record.image_generation" />
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ImageGenerationDetail from './ImageGenerationDetail.vue'
import { getById as getUserUsage } from '@/api/usage'
import { getById as getAdminUsage } from '@/api/admin/usage'
import { formatDateTime } from '@/utils/format'
import { resolveUsageRequestType } from '@/utils/usageRequestType'
import { requestTypeLabelKey } from '@/utils/errorBadges'
import type { UsageLog } from '@/types'

const props = defineProps<{ show: boolean; usageId: number | null; admin?: boolean }>()
const emit = defineEmits<{ 'update:show': [show: boolean] }>()
const { t } = useI18n()
const loading = ref(false)
const loadError = ref(false)
const record = ref<UsageLog | null>(null)
let version = 0

watch(() => [props.show, props.usageId, props.admin] as const, async ([show, id, admin], _, onCleanup) => {
  const current = ++version
  onCleanup(() => { version++ })
  record.value = null
  loadError.value = false
  loading.value = false
  if (!show || id == null) return
  loading.value = true
  try {
    const data = await (admin ? getAdminUsage(id) : getUserUsage(id))
    if (current === version) record.value = data
  } catch (error) {
    if (current !== version) return
    console.error('[ImageUsageDetailModal] Failed to load detail:', error)
    loadError.value = true
  } finally {
    if (current === version) loading.value = false
  }
}, { immediate: true })
</script>
