<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl space-y-6">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div class="min-w-0">
          <div class="flex items-center gap-3">
            <span class="ranking-mark" aria-hidden="true">
              <Icon name="trophy" size="lg" />
            </span>
            <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
              {{ t('ranking.title') }}
            </h1>
          </div>
          <p class="mt-2 text-sm text-gray-500 dark:text-dark-400">
            {{ t('ranking.description') }}
          </p>
        </div>

        <button
          type="button"
          class="btn btn-secondary btn-sm self-start sm:self-auto"
          :disabled="loading"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          @click="loadRanking"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          <span>{{ t('common.refresh') }}</span>
        </button>
      </header>

      <div v-if="ranking" class="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-gray-500 dark:text-dark-400">
        <span>{{ t('ranking.date', { date: ranking.date }) }}</span>
        <span>{{ t('ranking.timezone', { timezone: ranking.timezone }) }}</span>
        <span>{{ t('ranking.updatedAt', { time: updatedAtLabel }) }}</span>
      </div>

      <section class="ranking-panel" :aria-busy="loading">
        <div class="ranking-header" aria-hidden="true">
          <span>{{ t('ranking.rank') }}</span>
          <span>{{ t('ranking.user') }}</span>
          <span class="text-right">{{ t('ranking.tokens') }}</span>
        </div>

        <div v-if="loading" class="divide-y divide-gray-100 dark:divide-dark-700">
          <div v-for="index in 6" :key="index" class="ranking-row animate-pulse">
            <span class="h-8 w-8 rounded-full bg-gray-100 dark:bg-dark-700" />
            <span class="h-4 w-32 rounded bg-gray-100 dark:bg-dark-700" />
            <span class="ml-auto h-4 w-24 rounded bg-gray-100 dark:bg-dark-700" />
          </div>
        </div>

        <div v-else-if="errorMessage" class="flex min-h-56 flex-col items-center justify-center px-6 py-12 text-center">
          <Icon name="exclamationCircle" size="xl" class="text-rose-500" />
          <p class="mt-3 text-sm font-medium text-gray-900 dark:text-white">{{ errorMessage }}</p>
          <button type="button" class="btn btn-secondary btn-sm mt-4" @click="loadRanking">
            {{ t('ranking.retry') }}
          </button>
        </div>

        <div v-else-if="!ranking?.ranking.length" class="flex min-h-56 flex-col items-center justify-center px-6 py-12 text-center">
          <Icon name="chart" size="xl" class="text-gray-300 dark:text-dark-500" />
          <p class="mt-3 text-sm text-gray-500 dark:text-dark-400">{{ t('ranking.empty') }}</p>
        </div>

        <ol v-else class="divide-y divide-gray-100 dark:divide-dark-700">
          <li
            v-for="(entry, index) in ranking.ranking"
            :key="`${entry.rank}-${entry.display_name}`"
            class="ranking-row ranking-row-enter"
            :class="rankRowClass(entry.rank)"
            :style="{ animationDelay: `${Math.min(index, 12) * 35}ms` }"
          >
            <span class="rank-badge" :class="rankBadgeClass(entry.rank)">
              <Icon v-if="entry.rank <= 3" name="trophy" size="sm" />
              <span>{{ entry.rank }}</span>
            </span>
            <span class="min-w-0 truncate font-medium text-gray-800 dark:text-gray-100" :title="entry.display_name">
              {{ entry.display_name }}
            </span>
            <span class="text-right font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTokens(entry.total_tokens) }}
            </span>
          </li>
        </ol>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { getDailyTokenRanking, type DailyTokenRankingResponse } from '@/api/usage'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, locale } = useI18n()
const ranking = ref<DailyTokenRankingResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')

const updatedAtLabel = computed(() => {
  if (!ranking.value?.updated_at) return '-'
  const value = new Date(ranking.value.updated_at)
  if (Number.isNaN(value.getTime())) return ranking.value.updated_at
  return new Intl.DateTimeFormat(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(value)
})

function formatTokens(value: number): string {
  return new Intl.NumberFormat(locale.value, { maximumFractionDigits: 0 }).format(value)
}

function rankRowClass(rank: number): string {
  if (rank === 1) return 'rank-row-gold'
  if (rank === 2) return 'rank-row-silver'
  if (rank === 3) return 'rank-row-bronze'
  return ''
}

function rankBadgeClass(rank: number): string {
  if (rank === 1) return 'rank-badge-gold'
  if (rank === 2) return 'rank-badge-silver'
  if (rank === 3) return 'rank-badge-bronze'
  return 'rank-badge-default'
}

async function loadRanking() {
  loading.value = true
  errorMessage.value = ''
  try {
    ranking.value = await getDailyTokenRanking()
  } catch (error) {
    ranking.value = null
    errorMessage.value = extractApiErrorMessage(error, t('ranking.loadFailed'))
  } finally {
    loading.value = false
  }
}

onMounted(loadRanking)
</script>

<style scoped>
.ranking-mark {
  display: inline-flex;
  height: 2.5rem;
  width: 2.5rem;
  flex: none;
  align-items: center;
  justify-content: center;
  border: 1px solid rgb(245 158 11 / 35%);
  border-radius: 8px;
  background: rgb(255 251 235);
  color: rgb(180 83 9);
}

.ranking-panel {
  overflow: hidden;
  border: 1px solid rgb(229 231 235);
  border-radius: 8px;
  background: white;
  box-shadow: 0 8px 24px rgb(15 23 42 / 5%);
}

.ranking-header,
.ranking-row {
  display: grid;
  grid-template-columns: 4rem minmax(0, 1fr) minmax(8rem, auto);
  align-items: center;
  gap: 1rem;
}

.ranking-header {
  border-bottom: 1px solid rgb(229 231 235);
  background: rgb(249 250 251);
  padding: 0.75rem 1.25rem;
  color: rgb(107 114 128);
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
}

.ranking-row {
  min-height: 4.5rem;
  padding: 0.875rem 1.25rem;
  border-left: 3px solid transparent;
}

.rank-badge {
  display: inline-flex;
  width: 2.25rem;
  height: 2.25rem;
  align-items: center;
  justify-content: center;
  gap: 0.125rem;
  border-radius: 9999px;
  font-size: 0.75rem;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
}

.rank-badge-gold { background: rgb(254 243 199); color: rgb(146 64 14); }
.rank-badge-silver { background: rgb(229 231 235); color: rgb(55 65 81); }
.rank-badge-bronze { background: rgb(255 237 213); color: rgb(154 52 18); }
.rank-badge-default { background: rgb(243 244 246); color: rgb(75 85 99); }
.rank-row-gold { border-left-color: rgb(245 158 11); background: rgb(255 251 235); }
.rank-row-silver { border-left-color: rgb(156 163 175); background: rgb(249 250 251); }
.rank-row-bronze { border-left-color: rgb(194 65 12); background: rgb(255 247 237); }

.ranking-row-enter {
  animation: ranking-row-in 280ms ease-out both;
}

@keyframes ranking-row-in {
  from { opacity: 0; transform: translateY(5px); }
  to { opacity: 1; transform: translateY(0); }
}

:global(.dark) .ranking-mark {
  border-color: rgb(180 83 9 / 45%);
  background: rgb(120 53 15 / 24%);
  color: rgb(251 191 36);
}

:global(.dark) .ranking-panel {
  border-color: rgb(55 65 81);
  background: rgb(24 30 42);
}

:global(.dark) .ranking-header {
  border-color: rgb(55 65 81);
  background: rgb(17 24 39 / 65%);
  color: rgb(156 163 175);
}

:global(.dark) .rank-badge-gold { background: rgb(120 53 15 / 35%); color: rgb(252 211 77); }
:global(.dark) .rank-badge-silver { background: rgb(75 85 99 / 45%); color: rgb(229 231 235); }
:global(.dark) .rank-badge-bronze { background: rgb(124 45 18 / 35%); color: rgb(253 186 116); }
:global(.dark) .rank-badge-default { background: rgb(55 65 81); color: rgb(209 213 219); }
:global(.dark) .rank-row-gold { background: rgb(120 53 15 / 16%); }
:global(.dark) .rank-row-silver { background: rgb(75 85 99 / 14%); }
:global(.dark) .rank-row-bronze { background: rgb(124 45 18 / 14%); }

@media (max-width: 640px) {
  .ranking-header,
  .ranking-row {
    grid-template-columns: 3rem minmax(0, 1fr) minmax(6.5rem, auto);
    gap: 0.625rem;
  }

  .ranking-header,
  .ranking-row {
    padding-left: 0.875rem;
    padding-right: 0.875rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .ranking-row-enter { animation: none; }
}
</style>
