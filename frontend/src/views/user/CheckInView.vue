<template>
  <AppLayout>
    <div class="mx-auto max-w-4xl space-y-6">
      <div class="flex items-center justify-between">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('checkin.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('checkin.description') }}</p>
        </div>
        <Icon name="gift" size="xl" class="text-primary-500" />
      </div>

      <div v-if="loading" class="card flex min-h-48 items-center justify-center">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600" />
      </div>

      <template v-else>
        <div class="grid gap-4 sm:grid-cols-3">
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('checkin.account') }}</p>
            <p class="mt-2 truncate text-lg font-semibold text-gray-900 dark:text-white">{{ accountLabel }}</p>
            <p class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400">{{ status?.account?.email }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('checkin.monthTotal') }}</p>
            <p class="mt-2 text-2xl font-bold text-primary-600 dark:text-primary-400">{{ formatBalanceAmount(status?.total_reward) }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('checkin.status') }}</p>
            <p class="mt-2 text-lg font-semibold" :class="status?.checked_in_today ? 'text-emerald-600' : 'text-amber-600'">
              {{ status?.checked_in_today ? t('checkin.checkedIn') : t('checkin.notCheckedIn') }}
            </p>
            <p v-if="status?.streak_days" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('checkin.streak', { count: status.streak_days }) }}</p>
          </div>
        </div>

        <div v-if="!status?.enabled" class="card border-gray-200 bg-gray-50 p-5 dark:border-dark-700 dark:bg-dark-800/60">
          <p class="text-sm text-gray-600 dark:text-dark-300">{{ t('checkin.disabled') }}</p>
        </div>
        <div v-else-if="!status?.checked_in_today" class="card flex flex-col items-start justify-between gap-4 p-6 sm:flex-row sm:items-center">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('checkin.todayTitle') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('checkin.todayHint') }}</p>
          </div>
          <button class="btn btn-primary" :disabled="submitting" @click="handleCheckIn">
            <Icon name="checkCircle" size="sm" class="mr-2" />
            {{ submitting ? t('common.loading') : t('checkin.action') }}
          </button>
        </div>
        <div v-else class="card border-emerald-200 bg-emerald-50 p-5 dark:border-emerald-800/50 dark:bg-emerald-900/20">
          <p class="text-sm text-emerald-700 dark:text-emerald-300">{{ t('checkin.checkedInToday', { amount: formatBalanceAmount(status.today_reward) }) }}</p>
        </div>

        <div class="card p-6">
          <div class="mb-5 flex items-center justify-between">
            <button class="btn btn-secondary btn-sm" :aria-label="t('checkin.previousMonth')" @click="changeMonth(-1)">‹</button>
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ monthLabel }}</h2>
            <button class="btn btn-secondary btn-sm" :aria-label="t('checkin.nextMonth')" :disabled="isCurrentMonth" @click="changeMonth(1)">›</button>
          </div>
          <div class="grid grid-cols-7 gap-2 text-center text-xs text-gray-500 dark:text-dark-400">
            <span v-for="weekday in weekdays" :key="weekday">{{ weekday }}</span>
          </div>
          <div class="mt-2 grid grid-cols-7 gap-2">
            <div v-for="(day, index) in calendarDays" :key="`${monthKey}-${index}`" class="aspect-square rounded-lg border p-1 text-center" :class="day.checked_in ? 'border-emerald-300 bg-emerald-50 dark:border-emerald-700 dark:bg-emerald-900/20' : 'border-gray-100 dark:border-dark-700'">
              <template v-if="day.date">
                <div class="text-xs text-gray-500 dark:text-dark-400">{{ day.day }}</div>
                <div v-if="day.checked_in" class="mt-1 text-xs font-semibold text-emerald-600 dark:text-emerald-400">+{{ formatBalanceAmount(day.reward) }}</div>
              </template>
            </div>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { checkinAPI, type CheckinHistoryItem, type CheckinStatus } from '@/api/checkin'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, locale } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const status = ref<CheckinStatus | null>(null)
const history = ref<CheckinHistoryItem[]>([])
const loading = ref(true)
const submitting = ref(false)
const selectedMonth = ref(new Date())

const monthKey = computed(() => `${selectedMonth.value.getFullYear()}-${String(selectedMonth.value.getMonth() + 1).padStart(2, '0')}`)
const isCurrentMonth = computed(() => {
  const now = new Date()
  return now.getFullYear() === selectedMonth.value.getFullYear() && now.getMonth() === selectedMonth.value.getMonth()
})
const monthLabel = computed(() => new Intl.DateTimeFormat(locale.value, { year: 'numeric', month: 'long' }).format(selectedMonth.value))
const weekdays = computed(() => locale.value.startsWith('zh') ? ['日', '一', '二', '三', '四', '五', '六'] : ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'])
const accountLabel = computed(() => status.value?.account?.username || status.value?.account?.email || '-')

const calendarDays = computed(() => {
  const year = selectedMonth.value.getFullYear()
  const month = selectedMonth.value.getMonth()
  const first = new Date(year, month, 1).getDay()
  const total = new Date(year, month + 1, 0).getDate()
  const records = new Map(history.value.map(item => [item.check_in_date.slice(0, 10), item]))
  const cells: Array<{ date: string; day: number; checked_in: boolean; reward: number }> = []
  for (let i = 0; i < first; i += 1) cells.push({ date: '', day: 0, checked_in: false, reward: 0 })
  for (let day = 1; day <= total; day += 1) {
    const date = `${monthKey.value}-${String(day).padStart(2, '0')}`
    const record = records.get(date)
    cells.push({ date, day, checked_in: Boolean(record), reward: record?.reward || 0 })
  }
  return cells
})

function formatAmount(value: number | undefined) {
  return Number(value || 0).toFixed(1)
}

function formatBalanceAmount(value: number | undefined) {
  return `$${formatAmount(value)}`
}

async function load() {
  loading.value = true
  try {
    const [nextStatus, nextHistory] = await Promise.all([
      checkinAPI.getStatus(),
      checkinAPI.getHistory(62, monthKey.value),
    ])
    status.value = nextStatus
    history.value = nextHistory.items || []
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('checkin.description')))
  } finally {
    loading.value = false
  }
}

async function handleCheckIn() {
  submitting.value = true
  try {
    const result = await checkinAPI.checkIn()
    if (result.balance != null && status.value?.account) {
      status.value.account.balance = result.balance
    }
    await load()
    // The header reads balance from the global auth user, so refresh it after
    // the reward transaction succeeds instead of waiting for a full page reload.
    await authStore.refreshUser().catch(() => undefined)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('checkin.description')))
  } finally {
    submitting.value = false
  }
}

function changeMonth(delta: number) {
  selectedMonth.value = new Date(selectedMonth.value.getFullYear(), selectedMonth.value.getMonth() + delta, 1)
  void load()
}

onMounted(load)
</script>
