<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <div class="mb-2 flex items-center gap-2 text-sm text-gray-500 dark:text-gray-400">
            <Icon name="trophy" size="sm" class="text-primary-500" />
            {{ t('nav.dailyTokenRankingReward') }}
          </div>
          <h1 class="text-2xl font-semibold tracking-tight text-gray-900 dark:text-white sm:text-3xl">
            {{ t('admin.dailyTokenRankingReward.title') }}
          </h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.dailyTokenRankingReward.description') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <input
            v-model="selectedDate"
            type="date"
            class="input h-10"
            :max="yesterday"
            :aria-label="t('admin.dailyTokenRankingReward.date')"
            @change="loadPreview"
          />
          <button type="button" class="btn btn-secondary h-10 min-w-[84px] shrink-0 justify-center whitespace-nowrap" :disabled="loading" :title="t('common.refresh')" @click="loadPreview">
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            <span class="hidden sm:inline">{{ t('common.refresh') }}</span>
          </button>
        </div>
      </div>

      <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm text-gray-500 dark:text-gray-400">
        <span>{{ t('admin.dailyTokenRankingReward.date') }}：<strong class="font-medium text-gray-700 dark:text-gray-200">{{ reward?.date || selectedDate }}</strong></span>
        <span>{{ t('admin.dailyTokenRankingReward.timezone') }}：<strong class="font-medium text-gray-700 dark:text-gray-200">{{ reward?.timezone || 'Asia/Shanghai' }}</strong></span>
        <span v-if="reward" class="inline-flex items-center gap-1.5" :class="reward.settled ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'">
          <span class="h-1.5 w-1.5 rounded-full bg-current" />
          {{ reward.settled ? t('admin.dailyTokenRankingReward.settled') : t('admin.dailyTokenRankingReward.pending') }}
        </span>
      </div>

      <section class="card mobile-list-shell overflow-hidden">
        <div class="flex flex-col gap-3 border-b border-gray-200 px-4 py-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between sm:px-6">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.dailyTokenRankingReward.candidates') }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.dailyTokenRankingReward.rewardRule') }}</p>
          </div>
        </div>

        <div v-if="loading" class="flex min-h-56 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="!reward?.entries.length" class="flex min-h-56 flex-col items-center justify-center gap-2 px-6 text-center text-sm text-gray-500 dark:text-gray-400">
          <Icon name="inbox" size="lg" />
          <span>{{ t('admin.dailyTokenRankingReward.empty') }}</span>
        </div>
        <div v-else-if="reward?.entries.length" class="space-y-3 md:hidden">
          <article
            v-for="entry in reward.entries"
            :key="entry.rank"
            class="rounded-lg border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-900"
          >
            <div class="flex items-start justify-between gap-4">
              <div class="flex min-w-0 items-center gap-3">
                <span class="rank-badge shrink-0" :class="`rank-${entry.rank}`">{{ entry.rank }}</span>
                <span class="min-w-0 break-words text-sm font-semibold text-gray-900 dark:text-white">{{ entry.display_name }}</span>
              </div>
              <span class="status-badge shrink-0" :class="`status-${entry.status}`">{{ statusLabel(entry.status) }}</span>
            </div>
            <dl class="mt-4 space-y-3 border-t border-gray-100 pt-3 dark:border-dark-700">
              <div class="flex items-start justify-between gap-4">
                <dt class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.dailyTokenRankingReward.tokens') }}</dt>
                <dd class="text-right text-sm tabular-nums text-gray-700 dark:text-dark-200">{{ formatNumber(entry.total_tokens) }}</dd>
              </div>
              <div class="flex items-start justify-between gap-4">
                <dt class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.dailyTokenRankingReward.requests') }}</dt>
                <dd class="text-right text-sm tabular-nums text-gray-700 dark:text-dark-200">{{ formatNumber(entry.request_count) }}</dd>
              </div>
              <div class="flex items-start justify-between gap-4">
                <dt class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.dailyTokenRankingReward.reward') }}</dt>
                <dd class="text-right text-sm font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">${{ entry.reward_amount.toFixed(2) }}</dd>
              </div>
              <div v-if="entry.reason" class="flex items-start justify-between gap-4">
                <dt class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.dailyTokenRankingReward.status') }}</dt>
                <dd class="max-w-[65%] text-right text-xs text-gray-500 dark:text-gray-400">{{ entry.reason }}</dd>
              </div>
              <div v-if="entry.note" class="flex items-start justify-between gap-4">
                <dt class="text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-dark-400">{{ t('admin.dailyTokenRankingReward.note') }}</dt>
                <dd class="max-w-[65%] text-right text-xs text-gray-500 dark:text-gray-400">{{ entry.note }}</dd>
              </div>
            </dl>
            <button
              v-if="entry.status === 'pending'"
              type="button"
              class="btn btn-primary mt-4 w-full justify-center whitespace-nowrap"
              :disabled="settlingRank !== null"
              @click="settle(entry)"
            >
              <Icon name="checkCircle" size="sm" />
              {{ settlingRank === entry.rank ? t('common.submitting') : t('admin.dailyTokenRankingReward.settle') }}
            </button>
          </article>
        </div>
        <div v-if="!loading && reward?.entries.length" class="hidden overflow-x-auto md:block">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800/70">
              <tr class="text-left text-xs font-medium uppercase tracking-wider text-gray-500 dark:text-gray-400">
                <th class="px-4 py-3 sm:px-6">{{ t('admin.dailyTokenRankingReward.rank') }}</th>
                <th class="px-4 py-3">{{ t('admin.dailyTokenRankingReward.user') }}</th>
                <th class="px-4 py-3 text-right">{{ t('admin.dailyTokenRankingReward.tokens') }}</th>
                <th class="px-4 py-3 text-right">{{ t('admin.dailyTokenRankingReward.requests') }}</th>
                <th class="px-4 py-3 text-right">{{ t('admin.dailyTokenRankingReward.reward') }}</th>
                <th class="px-4 py-3">{{ t('admin.dailyTokenRankingReward.status') }}</th>
                <th class="px-4 py-3 sm:px-6">{{ t('admin.dailyTokenRankingReward.note') }}</th>
                <th class="px-4 py-3 text-right sm:px-6">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700/70">
              <tr v-for="entry in reward.entries" :key="entry.rank" class="text-sm text-gray-700 dark:text-gray-200">
                <td class="whitespace-nowrap px-4 py-4 sm:px-6"><span class="rank-badge" :class="`rank-${entry.rank}`">{{ entry.rank }}</span></td>
                <td class="whitespace-nowrap px-4 py-4 font-medium">{{ entry.display_name }}</td>
                <td class="whitespace-nowrap px-4 py-4 text-right tabular-nums">{{ formatNumber(entry.total_tokens) }}</td>
                <td class="whitespace-nowrap px-4 py-4 text-right tabular-nums">{{ formatNumber(entry.request_count) }}</td>
                <td class="whitespace-nowrap px-4 py-4 text-right font-semibold tabular-nums text-emerald-600 dark:text-emerald-400">${{ entry.reward_amount.toFixed(2) }}</td>
                <td class="whitespace-nowrap px-4 py-4">
                  <span class="status-badge" :class="`status-${entry.status}`">{{ statusLabel(entry.status) }}</span>
                  <span v-if="entry.reason" class="mt-1 block max-w-40 text-xs text-gray-500 dark:text-gray-400">{{ entry.reason }}</span>
                </td>
                <td class="whitespace-nowrap px-4 py-4 text-xs text-gray-500 dark:text-gray-400 sm:px-6">{{ entry.note }}</td>
                <td class="whitespace-nowrap px-4 py-4 text-right sm:px-6">
                  <button
                    v-if="entry.status === 'pending'"
                    type="button"
                    class="btn btn-primary min-w-[96px] justify-center whitespace-nowrap"
                    :disabled="settlingRank !== null"
                    @click="settle(entry)"
                  >
                    <Icon name="checkCircle" size="sm" />
                    {{ settlingRank === entry.rank ? t('common.submitting') : t('admin.dailyTokenRankingReward.settle') }}
                  </button>
                  <span v-else class="text-xs text-gray-400 dark:text-gray-500">—</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import dailyTokenRankingRewardAPI, { type DailyTokenRankingRewardEntry, type DailyTokenRankingRewardResponse } from '@/api/admin/dailyTokenRankingReward'

const { t } = useI18n()
const appStore = useAppStore()
const reward = ref<DailyTokenRankingRewardResponse | null>(null)
const loading = ref(false)
const settlingRank = ref<number | null>(null)

const dateInBeijing = (date: Date) => new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai' }).format(date)
const yesterday = dateInBeijing(new Date(Date.now() - 86400000))
const selectedDate = ref(yesterday)

const formatNumber = (value: number) => new Intl.NumberFormat('en-US').format(value || 0)
const statusLabel = (status: string) => {
  const key = `admin.dailyTokenRankingReward.statuses.${status}`
  const translated = t(key)
  return translated === key ? status : translated
}

const loadPreview = async () => {
  loading.value = true
  try {
    reward.value = await dailyTokenRankingRewardAPI.preview(selectedDate.value)
    selectedDate.value = reward.value.date
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.dailyTokenRankingReward.loadFailed')))
  } finally {
    loading.value = false
  }
}

const settle = async (entry: DailyTokenRankingRewardEntry) => {
  if (!reward.value || entry.status !== 'pending' || !window.confirm(t('admin.dailyTokenRankingReward.confirmSettle', { rank: entry.rank, amount: entry.reward_amount.toFixed(2) }))) return
  settlingRank.value = entry.rank
  try {
    reward.value = await dailyTokenRankingRewardAPI.settle(reward.value.date, entry.rank)
    appStore.showSuccess(t('admin.dailyTokenRankingReward.settleSuccess', { rank: entry.rank }))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.dailyTokenRankingReward.settleFailed')))
  } finally {
    settlingRank.value = null
  }
}

onMounted(loadPreview)
</script>

<style scoped>
.rank-badge,
.status-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 9999px;
  font-size: 0.75rem;
  font-weight: 600;
}
.rank-badge { height: 2rem; width: 2rem; background: rgb(243 244 246); color: rgb(75 85 99); }
.rank-1 { background: rgb(254 243 199); color: rgb(180 83 9); }
.rank-2 { background: rgb(229 231 235); color: rgb(55 65 81); }
.rank-3 { background: rgb(254 215 170); color: rgb(154 52 18); }
.status-badge { padding: 0.25rem 0.625rem; background: rgb(243 244 246); color: rgb(75 85 99); }
.status-paid { background: rgb(220 252 231); color: rgb(21 128 61); }
.status-skipped { background: rgb(254 226 226); color: rgb(185 28 28); }
.status-pending { background: rgb(254 249 195); color: rgb(161 98 7); }
:global(html.dark) .rank-badge { background: rgb(55 65 81); color: rgb(209 213 219); }
:global(html.dark) .rank-1 { background: rgb(120 53 15 / 0.55); color: rgb(253 230 138); }
:global(html.dark) .rank-2 { background: rgb(75 85 99 / 0.6); color: rgb(229 231 235); }
:global(html.dark) .rank-3 { background: rgb(124 45 18 / 0.55); color: rgb(254 215 170); }
:global(html.dark) .status-badge { background: rgb(55 65 81); color: rgb(209 213 219); }
:global(html.dark) .status-paid { background: rgb(20 83 45 / 0.5); color: rgb(134 239 172); }
:global(html.dark) .status-skipped { background: rgb(127 29 29 / 0.5); color: rgb(252 165 165); }
:global(html.dark) .status-pending { background: rgb(113 63 18 / 0.5); color: rgb(253 224 71); }
@media (max-width: 767px) {
  .mobile-list-shell {
    border-color: transparent;
    border-radius: 0;
    background: transparent;
    box-shadow: none;
  }
}
</style>
