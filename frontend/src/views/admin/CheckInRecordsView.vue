<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('nav.checkinRecords') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.description') }}</p>
        </div>
        <div class="flex items-end gap-3">
          <label class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.checkinRecords.month') }}<input v-model="month" type="month" class="input mt-1" @change="load" /></label>
          <label class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.checkinRecords.userId') }}<input v-model.number="userId" type="number" min="1" class="input mt-1 w-28" @change="load" /></label>
        </div>
      </div>
      <div class="card overflow-hidden">
        <div v-if="loading" class="p-8 text-center text-gray-500">{{ t('common.loading') }}</div>
        <table v-else class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
          <thead class="bg-gray-50 dark:bg-dark-800"><tr><th v-for="key in headers" :key="key" class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500">{{ t(`admin.checkinRecords.${key}`) }}</th></tr></thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="record in records" :key="record.id"><td class="whitespace-nowrap px-4 py-3 text-sm text-gray-700 dark:text-dark-200">{{ formatDateTime(record.created_at) }}</td><td class="px-4 py-3 text-sm text-gray-700 dark:text-dark-200">{{ record.username || record.email }}<span class="ml-2 text-xs text-gray-400">{{ record.email }}</span></td><td class="px-4 py-3 text-sm font-semibold text-emerald-600">+${{ Number(record.reward).toFixed(1) }}</td><td class="px-4 py-3 text-sm text-gray-700 dark:text-dark-200">{{ record.request_count }}</td><td class="px-4 py-3 text-sm text-gray-700 dark:text-dark-200">${{ Number(record.daily_spend).toFixed(1) }}</td></tr><tr v-if="records.length === 0"><td colspan="5" class="px-4 py-8 text-center text-gray-500">{{ t('admin.checkinRecords.empty') }}</td></tr></tbody>
        </table>
        <div class="flex items-center justify-between border-t border-gray-100 px-4 py-3 text-sm text-gray-500 dark:border-dark-700"><span>{{ t('admin.checkinRecords.total', { count: total }) }}</span><div class="flex gap-2"><button class="btn btn-secondary btn-sm" :disabled="page <= 1" @click="page -= 1; load()">‹</button><span class="px-2 py-1">{{ page }}</span><button class="btn btn-secondary btn-sm" :disabled="page * pageSize >= total" @click="page += 1; load()">›</button></div></div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { adminCheckinAPI, type CheckInRecord } from '@/api/admin/checkin'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'

const { t } = useI18n(); const appStore = useAppStore()
const now = new Date(); const month = ref(`${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`); const userId = ref<number | undefined>(); const records = ref<CheckInRecord[]>([]); const total = ref(0); const page = ref(1); const pageSize = 20; const loading = ref(false)
const headers = ['date', 'account', 'reward', 'requests', 'spend'] as const
async function load() { loading.value = true; try { const data = await adminCheckinAPI.getRecords({ month: month.value, user_id: userId.value || undefined, page: page.value, page_size: pageSize }); records.value = data.items || []; total.value = data.total || 0 } catch (error) { appStore.showError(error instanceof Error ? error.message : String(error)) } finally { loading.value = false } }
onMounted(load)
</script>
