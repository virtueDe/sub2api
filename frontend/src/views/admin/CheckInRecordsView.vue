<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('nav.checkinRecords') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.description') }}</p>
        </div>
        <div class="flex w-full flex-col gap-3 sm:w-auto sm:flex-row sm:items-end">
          <label class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.checkinRecords.month') }}<input v-model="month" type="month" class="input mt-1 w-full sm:w-auto" @change="load" /></label>
          <label class="text-sm text-gray-600 dark:text-dark-300">{{ t('admin.checkinRecords.userId') }}<input v-model.number="userId" type="number" min="1" class="input mt-1 w-full sm:w-28" @change="load" /></label>
        </div>
      </div>
      <div class="card mobile-list-shell overflow-hidden">
        <div v-if="loading" class="p-8 text-center text-gray-500">{{ t('common.loading') }}</div>
        <div v-else-if="records.length === 0" class="p-8 text-center text-gray-500">
          {{ t('admin.checkinRecords.empty') }}
        </div>
        <div v-else-if="records.length > 0" class="space-y-3 md:hidden">
          <article
            v-for="record in records"
            :key="record.id"
            class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
          >
            <dl class="space-y-3">
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.date') }}</dt>
                <dd class="min-w-0 text-right text-sm text-gray-700 dark:text-dark-200">{{ formatDateTime(record.created_at) }}</dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.account') }}</dt>
                <dd class="min-w-0 text-right text-sm text-gray-700 dark:text-dark-200">
                  <div class="break-words font-medium">{{ record.username || record.email }}</div>
                  <div class="break-all text-xs text-gray-400">{{ record.email }}</div>
                </dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.reward') }}</dt>
                <dd class="text-right text-sm font-semibold text-emerald-600">+${{ Number(record.reward).toFixed(1) }}</dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.requests') }}</dt>
                <dd class="text-right text-sm text-gray-700 dark:text-dark-200">{{ record.request_count }}</dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.checkinRecords.spend') }}</dt>
                <dd class="text-right text-sm text-gray-700 dark:text-dark-200">${{ Number(record.daily_spend).toFixed(1) }}</dd>
              </div>
            </dl>
          </article>
        </div>
        <table v-if="!loading && records.length > 0" class="hidden min-w-full divide-y divide-gray-200 dark:divide-dark-700 md:table">
          <thead class="bg-gray-50 dark:bg-dark-800"><tr><th v-for="key in headers" :key="key" class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500">{{ t(`admin.checkinRecords.${key}`) }}</th></tr></thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="record in records" :key="record.id"><td class="whitespace-nowrap px-4 py-3 text-sm text-gray-700 dark:text-dark-200">{{ formatDateTime(record.created_at) }}</td><td class="px-4 py-3 text-sm text-gray-700 dark:text-dark-200">{{ record.username || record.email }}<span class="ml-2 text-xs text-gray-400">{{ record.email }}</span></td><td class="px-4 py-3 text-sm font-semibold text-emerald-600">+${{ Number(record.reward).toFixed(1) }}</td><td class="px-4 py-3 text-sm text-gray-700 dark:text-dark-200">{{ record.request_count }}</td><td class="px-4 py-3 text-sm text-gray-700 dark:text-dark-200">${{ Number(record.daily_spend).toFixed(1) }}</td></tr></tbody>
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

<style scoped>
@media (max-width: 767px) {
  .mobile-list-shell {
    border-color: transparent;
    border-radius: 0;
    background: transparent;
    box-shadow: none;
  }
}
</style>
