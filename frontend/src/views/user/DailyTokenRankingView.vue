<template>
  <AppLayout>
    <div class="ranking-page mx-auto max-w-5xl space-y-7">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div class="min-w-0">
          <div class="flex items-center gap-3">
            <img :src="championIcon" class="ranking-title-icon" alt="" aria-hidden="true" />
            <h1 class="ranking-title text-2xl font-bold">
              {{ t('ranking.title') }}
            </h1>
          </div>
          <p class="ranking-description mt-2 text-sm">
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

      <div v-if="ranking" class="ranking-meta flex flex-wrap items-center gap-x-5 gap-y-2 text-xs">
        <span>{{ t('ranking.date', { date: ranking.date }) }}</span>
        <span>{{ t('ranking.timezone', { timezone: ranking.timezone }) }}</span>
        <span>{{ t('ranking.updatedAt', { time: updatedAtLabel }) }}</span>
      </div>

      <section class="ranking-panel" :aria-busy="loading">
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

        <div v-else class="ranking-results">
          <section v-if="topThree.length" class="podium-shell" aria-labelledby="podium-title">
            <div class="podium-heading">
              <span id="podium-title">{{ t('ranking.topThree') }}</span>
              <img :src="championIcon" class="ranking-section-icon" alt="" aria-hidden="true" />
            </div>
            <div class="podium-grid">
              <article
                v-for="entry in podiumEntries"
                :key="`podium-${entry.rank}-${entry.display_name}`"
                class="podium-slot"
                :class="`podium-slot-${entry.rank}`"
              >
                <div class="podium-avatar" :class="`podium-avatar-${entry.rank}`" aria-hidden="true">
                  <img v-if="entry.rank === 1" :src="championIcon" class="podium-champion-icon" alt="" />
                  <template v-else>{{ initials(entry.display_name) }}</template>
                </div>
                <div class="podium-card" :class="rankRowClass(entry.rank)">
                  <span class="podium-name" :title="entry.display_name">{{ entry.display_name }}</span>
                  <strong class="podium-tokens">{{ formatTokens(entry.total_tokens) }}</strong>
                  <span class="podium-unit">{{ t('ranking.tokens') }}</span>
                </div>
              </article>
            </div>
          </section>

          <section v-if="remainingEntries.length" class="ranking-list" aria-labelledby="ranking-list-title">
            <div class="ranking-list-heading">
              <span id="ranking-list-title">{{ t('ranking.otherRanks') }}</span>
              <span>{{ t('ranking.tokens') }}</span>
            </div>
            <ol class="divide-y divide-gray-100 dark:divide-dark-700">
              <li
                v-for="(entry, index) in remainingEntries"
                :key="`${entry.rank}-${entry.display_name}`"
                class="ranking-row ranking-row-enter"
                :style="{ animationDelay: `${Math.min(index, 12) * 35}ms` }"
              >
                <span class="rank-badge rank-badge-default"><span>{{ entry.rank }}</span></span>
                <span class="ranking-user" :title="entry.display_name">{{ entry.display_name }}</span>
                <span class="ranking-tokens">{{ formatTokens(entry.total_tokens) }}</span>
              </li>
            </ol>
          </section>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import championIcon from '@/assets/icons/guanjun.svg'
import { getDailyTokenRanking, type DailyTokenRankingResponse } from '@/api/usage'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, locale } = useI18n()
const ranking = ref<DailyTokenRankingResponse | null>(null)
const loading = ref(true)
const errorMessage = ref('')

const topThree = computed(() => ranking.value?.ranking.slice(0, 3) ?? [])
const podiumEntries = computed(() => {
  const entries = ranking.value?.ranking ?? []
  return [entries[1], entries[0], entries[2]].filter(Boolean) as typeof entries
})
const remainingEntries = computed(() => ranking.value?.ranking.slice(3) ?? [])

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

function initials(value: string): string {
  const text = value.replace(/\*/g, '').trim()
  return Array.from(text).slice(0, 2).join('').toUpperCase() || '?'
}

function rankRowClass(rank: number): string {
  if (rank === 1) return 'rank-row-gold'
  if (rank === 2) return 'rank-row-silver'
  if (rank === 3) return 'rank-row-bronze'
  return ''
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
.ranking-page {
  --ranking-text: #172033;
  --ranking-muted: #64748b;
  --ranking-panel: #ffffff;
  --ranking-panel-soft: #f8fafc;
  --ranking-border: #e2e8f0;
  --ranking-row-border: #edf2f7;
  --ranking-accent: #0f766e;
}

.ranking-title-icon { width: 2.25rem; height: 2.25rem; flex: none; object-fit: contain; }
.ranking-section-icon { width: 1.25rem; height: 1.25rem; object-fit: contain; }

.ranking-title { color: var(--ranking-text); letter-spacing: 0; }
.ranking-description,
.ranking-meta { color: var(--ranking-muted); }

.ranking-panel {
  overflow: hidden;
  border: 1px solid var(--ranking-border);
  border-radius: 16px;
  background: var(--ranking-panel);
  box-shadow: 0 8px 24px rgb(15 23 42 / 6%);
}

.ranking-row {
  display: grid;
  grid-template-columns: 4rem minmax(0, 1fr) minmax(8rem, auto);
  align-items: center;
  gap: 1rem;
}

.ranking-row {
  min-height: 4.5rem;
  padding: 0.875rem 1.25rem;
  border-left: 3px solid transparent;
  color: var(--ranking-text);
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

.rank-badge-default { background: rgb(243 244 246); color: rgb(75 85 99); }

.ranking-results { padding-bottom: 0.25rem; }
.podium-shell { padding: 1.25rem 1.25rem 1.5rem; }
.podium-heading,
.ranking-list-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: var(--ranking-muted);
  font-size: 0.75rem;
  font-weight: 700;
  letter-spacing: 0;
  text-transform: uppercase;
}
.podium-heading { padding: 0 0.25rem 1.25rem; }
.podium-grid {
  display: grid;
  grid-template-columns: minmax(0, 0.9fr) minmax(0, 1.15fr) minmax(0, 0.9fr);
  align-items: end;
  gap: 0.875rem;
  max-width: 44rem;
  margin: 0 auto;
}
.podium-slot {
  position: relative;
  display: flex;
  min-width: 0;
  flex-direction: column;
  align-items: center;
  text-align: center;
}
.podium-slot-1 { grid-column: 2; }
.podium-slot-2 { grid-column: 1; grid-row: 1; }
.podium-slot-3 { grid-column: 3; grid-row: 1; }
.podium-avatar {
  display: grid;
  place-items: center;
  width: 5rem;
  aspect-ratio: 1;
  margin-bottom: -1.1rem;
  border: 3px solid var(--ranking-panel);
  border-radius: 9999px;
  color: #fff;
  font-size: 1.125rem;
  font-weight: 800;
  letter-spacing: 0;
  box-shadow: 0 6px 16px rgb(15 23 42 / 12%);
  z-index: 1;
}
.podium-slot-1 .podium-avatar { width: 5.75rem; }
.podium-avatar-1 { background: #d69e2e; }
.podium-avatar-2 { background: #64748b; }
.podium-avatar-3 { background: #c56a2d; }
.podium-champion-icon { width: 3.75rem; height: 3.75rem; object-fit: contain; }
.podium-card {
  display: flex;
  width: 100%;
  min-height: 7.25rem;
  flex-direction: column;
  align-items: center;
  justify-content: flex-end;
  gap: 0.25rem;
  padding: 1.75rem 0.75rem 0.875rem;
  border: 1px solid var(--ranking-border);
  border-radius: 12px;
  background: var(--ranking-panel-soft);
}
.podium-slot-1 .podium-card { min-height: 8.75rem; }
.podium-name {
  display: block;
  max-width: 100%;
  overflow: hidden;
  color: var(--ranking-text);
  font-size: 0.875rem;
  font-weight: 700;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.podium-tokens {
  color: var(--ranking-text);
  font-size: 1.55rem;
  font-variant-numeric: tabular-nums;
  letter-spacing: 0;
  line-height: 1.1;
}
.podium-slot-1 .podium-tokens { font-size: 1.75rem; }
.podium-unit { color: var(--ranking-muted); font-size: 0.68rem; }
.ranking-list { border-top: 1px solid var(--ranking-border); }
.ranking-list-heading { padding: 1rem 1.25rem 0.75rem; }
.ranking-user { min-width: 0; overflow: hidden; color: var(--ranking-text); font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.ranking-tokens { color: var(--ranking-text); font-weight: 700; font-variant-numeric: tabular-nums; text-align: right; }

.rank-row-gold { border-color: #d69e2e; background: #fffbeb; }
.rank-row-silver { border-color: #94a3b8; background: #f1f5f9; }
.rank-row-bronze { border-color: #c56a2d; background: #fff7ed; }

.ranking-row-enter {
  animation: ranking-row-in 280ms ease-out both;
}

@keyframes ranking-row-in {
  from { opacity: 0; transform: translateY(5px); }
  to { opacity: 1; transform: translateY(0); }
}

@media (max-width: 640px) {
  .podium-shell { padding-left: 0.75rem; padding-right: 0.75rem; }
  .podium-grid { gap: 0.5rem; }
  .podium-avatar { width: 4.5rem; font-size: 1rem; }
  .podium-card { min-height: 7.25rem; padding-left: 0.45rem; padding-right: 0.45rem; }
  .podium-slot-1 .podium-card { min-height: 9rem; }
  .podium-tokens { font-size: 1.35rem; }
  .ranking-row {
    grid-template-columns: 3rem minmax(0, 1fr) minmax(6.5rem, auto);
    gap: 0.625rem;
  }

  .ranking-row {
    padding-left: 0.875rem;
    padding-right: 0.875rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .ranking-row-enter { animation: none; }
}
</style>

<style>
html.dark .ranking-page {
  --ranking-text: #f8fafc;
  --ranking-muted: #94a3b8;
  --ranking-panel: rgb(30 41 59 / 50%);
  --ranking-panel-soft: #1e293b;
  --ranking-border: rgb(51 65 85 / 65%);
  --ranking-row-border: #1e293b;
}

html.dark .ranking-page .ranking-panel {
  box-shadow: none;
}

html.dark .ranking-page .rank-badge-default { background: #334155; color: #cbd5e1; }
html.dark .ranking-page .rank-row-gold { border-color: rgb(215 165 42 / 65%); background: rgb(215 165 42 / 9%); }
html.dark .ranking-page .rank-row-silver { border-color: rgb(124 141 166 / 65%); background: rgb(124 141 166 / 9%); }
html.dark .ranking-page .rank-row-bronze { border-color: rgb(198 106 43 / 65%); background: rgb(198 106 43 / 9%); }
html.dark .ranking-page .podium-avatar { border-color: #1e293b; box-shadow: 0 6px 18px rgb(2 6 23 / 35%); }
html.dark .ranking-page .ranking-row:hover { background: rgb(30 41 59 / 55%); }
</style>
