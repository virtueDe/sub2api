<template>
  <section class="w-full min-w-0 space-y-4" :aria-busy="loading">
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }} <button class="underline" @click="$emit('retry')">{{ t('admin.profitAccounting.overviewRetry') }}</button></p>
    <div class="grid gap-4 md:grid-cols-2">
      <article v-for="card in cards" :key="card.key" class="card p-5" :data-period="card.key">
        <div class="flex items-center justify-between gap-3"><h2 class="text-sm font-medium text-gray-500">{{ t(`admin.profitAccounting.${card.key}`) }}</h2><span class="text-xs text-gray-400">USD</span></div>
        <p class="mt-3 text-3xl font-semibold tabular-nums" :class="card.profit !== null && card.profit < 0 ? 'text-red-600' : 'text-primary-600'">{{ loading ? t('common.loading') : error ? '—' : money(card.profit) }}</p>
        <p class="mt-2 text-xs text-gray-500">{{ card.start }} — {{ endDate }}</p>
        <p v-if="!loading && !error && card.pending" class="mt-2 text-sm text-amber-600">{{ t('admin.profitAccounting.daysPending', { count: card.pending }) }}</p>
      </article>
    </div>
    <section class="card min-w-0 p-5">
      <h2 class="text-lg font-semibold">{{ t('admin.profitAccounting.trendTitle') }} <span class="text-xs font-normal text-gray-400">USD</span></h2>
      <p class="mt-1 text-xs text-gray-500">{{ t('admin.profitAccounting.overviewHelp') }}</p>
      <div class="relative mt-5 h-56 min-w-0 sm:h-64">
        <p v-if="loading" class="py-20 text-center text-gray-500">{{ t('common.loading') }}</p>
        <Line v-else-if="!error" :data="chartData" :options="chartOptions" :aria-label="t('admin.profitAccounting.trendTitle')" role="img" />
      </div>
      <p class="mt-3 text-xs text-gray-500">{{ t('admin.profitAccounting.trendHelp') }}</p>
    </section>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Tooltip, type ChartOptions } from 'chart.js'
import { Line } from 'vue-chartjs'
import type { ProfitReport } from '@/api/admin/profitAccounting'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip)
const props = defineProps<{ reports: ProfitReport[]; endDate: string; loading: boolean; error: string }>()
defineEmits<{ retry: [] }>()
const { t } = useI18n()
const money = (value: number | null) => value === null ? '—' : new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 10 }).format(value)
const days = computed(() => {
  const end = new Date(`${props.endDate}T00:00:00Z`).getTime()
  const byDate = new Map(props.reports.map(report => [report.date, report]))
  return Array.from({ length: 15 }, (_, index) => {
    const date = new Date(end - (14 - index) * 86400000).toISOString().slice(0, 10)
    const report = byDate.get(date)
    const profit = report?.status === 'complete' && !report.source_incomplete && report.profit_usd != null ? Number(report.profit_usd) : null
    return { date, profit: profit !== null && Number.isFinite(profit) ? profit : null }
  })
})
const cards = computed(() => [
  { key: 'yesterdayProfit', count: 1 }, { key: 'weekProfit', count: 7 }
].map(({ key, count }) => {
  const period = days.value.slice(-count)
  const pending = period.filter(day => day.profit === null).length
  return { key, start: period[0]!.date, pending, profit: pending ? null : period.reduce((sum, day) => sum + day.profit!, 0) }
}))
const chartData = computed(() => ({
  labels: days.value.map(day => day.date),
  datasets: [{ label: `${t('admin.profitAccounting.profit')} (USD)`, data: days.value.map(day => day.profit), borderColor: '#0d9488', backgroundColor: '#0d9488', borderWidth: 2, pointRadius: 3, pointHoverRadius: 5, spanGaps: false, tension: 0.2 }]
}))
const chartOptions: ChartOptions<'line'> = {
  responsive: true, maintainAspectRatio: false,
  interaction: { mode: 'index', intersect: false },
  plugins: { legend: { display: false }, tooltip: { callbacks: { label: context => `USD ${context.parsed.y ?? '—'}` } } },
  scales: { x: { grid: { display: false }, ticks: { maxTicksLimit: 8, maxRotation: 0 } }, y: { beginAtZero: true, ticks: { callback: value => `$${value}` } } }
}
</script>
