import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import type { DashboardStats } from '@/types'
import DashboardView from '../DashboardView.vue'

const { getSnapshotV2, getUserUsageTrend, getUserSpendingRanking } = vi.hoisted(() => ({
  getSnapshotV2: vi.fn(),
  getUserUsageTrend: vi.fn(),
  getUserSpendingRanking: vi.fn()
}))

vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', props: ['data', 'options'], template: '<div class="line-chart" />' }
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    dashboard: {
      getSnapshotV2,
      getUserUsageTrend,
      getUserSpendingRanking
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

const createDashboardStats = (): DashboardStats => ({
  total_users: 0,
  today_new_users: 0,
  active_users: 0,
  hourly_active_users: 0,
  stats_updated_at: '',
  stats_stale: false,
  total_api_keys: 0,
  active_api_keys: 0,
  total_accounts: 0,
  normal_accounts: 0,
  error_accounts: 0,
  ratelimit_accounts: 0,
  overload_accounts: 0,
  total_requests: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0,
  total_tokens: 0,
  total_cost: 0,
  total_actual_cost: 0,
  today_requests: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 0,
  today_cost: 0,
  today_actual_cost: 0,
  average_duration_ms: 0,
  uptime: 0,
  rpm: 0,
  tpm: 0
})

const mountDashboard = () => mount(DashboardView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' }, LoadingSpinner: true,
    Icon: true, DateRangePicker: true, Select: true, ModelDistributionChart: true,
    TokenUsageTrend: true
  } }
})

const refreshButton = (wrapper: ReturnType<typeof mountDashboard>) =>
  wrapper.findAll('button').find(button => button.text() === 'common.refresh')!

const changeDate = async (wrapper: ReturnType<typeof mountDashboard>, start: string, end: string) => {
  const picker = wrapper.findComponent({ name: 'DateRangePicker' })
  picker.vm.$emit('update:start-date', start)
  picker.vm.$emit('update:end-date', end)
  picker.vm.$emit('change', { startDate: start, endDate: end, preset: null })
  await flushPromises()
}

describe('admin DashboardView', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })
  beforeEach(() => {
    setActivePinia(createPinia())

    getSnapshotV2.mockReset()
    getUserUsageTrend.mockReset()
    getUserSpendingRanking.mockReset()

    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: []
    })
    getUserUsageTrend.mockResolvedValue({
      trend: [],
      start_date: '',
      end_date: '',
      granularity: 'hour'
    })
    getUserSpendingRanking.mockResolvedValue({
      ranking: [],
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0,
      start_date: '',
      end_date: ''
    })
  })

  it('switches metrics and ignores a stale tokens response', async () => {
    let resolveTokens!: (value: any) => void
    getUserUsageTrend.mockImplementationOnce(() => new Promise(resolve => { resolveTokens = resolve }))
    getUserUsageTrend.mockResolvedValueOnce({ trend: [{ date: '2026-01-01', user_id: 2, username: 'spender', email: '', requests: 1, tokens: 10, cost: 5, actual_cost: 5 }] })
    const wrapper = mount(DashboardView, {
      global: { stubs: {
        AppLayout: { template: '<div><slot /></div>' }, LoadingSpinner: true,
        Icon: true, DateRangePicker: true, Select: true, ModelDistributionChart: true,
        TokenUsageTrend: true
      } }
    })
    await flushPromises()
    expect(getUserUsageTrend).toHaveBeenCalledWith(expect.objectContaining({ metric: 'tokens', limit: 12 }))
    await wrapper.findAll('button').find(button => button.text() === 'admin.dashboard.actualSpending')!.trigger('click')
    expect(getUserUsageTrend).toHaveBeenCalledTimes(1)
    expect(getUserSpendingRanking).not.toHaveBeenCalled()
    resolveTokens({ trend: [{ date: '2026-01-01', user_id: 1, username: 'tokens', email: '', requests: 1, tokens: 100, cost: 1, actual_cost: 1 }] })
    await flushPromises()
    expect(getUserUsageTrend).toHaveBeenCalledWith(expect.objectContaining({ metric: 'actual_cost', limit: 12 }))
    await flushPromises()
    const chart = wrapper.findComponent({ name: 'Line' })
    expect(chart.props('data').datasets[0].data).toEqual([5])
    expect(chart.props('options').scales.y.ticks.callback(5)).toBe('$5.00')
    expect(chart.props('data').datasets[0].data).toEqual([5])
  })

  it('retries busy statistics once, keeps independent results visible and allows recovery', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getSnapshotV2.mockRejectedValue({ status: 503, reason: 'STATISTICS_BUSY', retry_after_seconds: 1 })
    const wrapper = mountDashboard()
    await flushPromises()
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getUserUsageTrend).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(getSnapshotV2).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').text()).toBe('admin.dashboard.failedToLoad')
    expect(wrapper.text()).toContain('admin.dashboard.quickActions')
    expect(wrapper.text()).not.toContain('admin.dashboard.apiKeys')
    expect(wrapper.findComponent({ name: 'ModelDistributionChart' }).props('rankingError')).toBe(false)
    expect(wrapper.findComponent({ name: 'TokenUsageTrend' }).props('error')).toBe(true)
    expect(getUserUsageTrend).toHaveBeenCalledTimes(1)
    expect(getUserSpendingRanking).toHaveBeenCalledTimes(1)
    expect(refreshButton(wrapper).attributes('disabled')).toBeUndefined()
    await vi.advanceTimersByTimeAsync(10000)
    expect(getSnapshotV2).toHaveBeenCalledTimes(2)

    getSnapshotV2.mockResolvedValue({ stats: createDashboardStats(), trend: [], models: [] })
    await refreshButton(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.dashboard.apiKeys')
    expect(wrapper.findComponent({ name: 'TokenUsageTrend' }).props('error')).toBe(false)
    wrapper.unmount()
  })

  it('clears old date data immediately and distinguishes failures from empty results', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getSnapshotV2.mockResolvedValueOnce({ stats: createDashboardStats(), trend: [{ date: '2026-01-01' }], models: [{ model: 'old' }] })
    getUserSpendingRanking.mockResolvedValueOnce({ ranking: [{ user_id: 1 }], total_actual_cost: 4 })
    getUserUsageTrend.mockResolvedValueOnce({ trend: [{ date: '2026-01-01', user_id: 1, tokens: 100 }] })
    const wrapper = mountDashboard()
    await flushPromises()
    const distribution = wrapper.findComponent({ name: 'ModelDistributionChart' })
    const tokenTrend = wrapper.findComponent({ name: 'TokenUsageTrend' })
    expect(distribution.props('rankingItems')).toHaveLength(1)
    let rejectSnapshot!: (error: unknown) => void
    getSnapshotV2.mockImplementationOnce(() => new Promise((_, reject) => { rejectSnapshot = reject }))
    getUserUsageTrend.mockRejectedValueOnce({ status: 500 })
    getUserSpendingRanking.mockRejectedValueOnce({ status: 500 })
    await changeDate(wrapper, '2026-02-01', '2026-02-02')
    expect(tokenTrend.props('trendData')).toEqual([])
    expect(distribution.props('modelStats')).toEqual([])
    expect(distribution.props('rankingItems')).toEqual([])
    rejectSnapshot({ status: 500 })
    await flushPromises()
    expect(tokenTrend.props('error')).toBe(true)
    expect(distribution.props('rankingError')).toBe(true)
    expect(wrapper.findComponent({ name: 'Line' }).exists()).toBe(false)
    expect(wrapper.findAll('[role="alert"]')).toHaveLength(2)
    expect(getSnapshotV2).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('skips superseded queued ranges and reloads missing stats for the latest date', async () => {
    let resolveOld!: (value: unknown) => void
    getSnapshotV2.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mountDashboard()
    await flushPromises()
    await changeDate(wrapper, '2026-02-01', '2026-02-02')
    await changeDate(wrapper, '2026-03-01', '2026-03-02')
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    resolveOld({ stats: { ...createDashboardStats(), total_api_keys: 999 }, trend: [{ date: 'old' }], models: [] })
    await flushPromises()
    expect(getSnapshotV2).toHaveBeenCalledTimes(2)
    expect(getSnapshotV2).toHaveBeenLastCalledWith(expect.objectContaining({ start_date: '2026-03-01', include_stats: true }))
    expect(getUserUsageTrend).toHaveBeenCalledTimes(1)
    expect(getUserUsageTrend).toHaveBeenCalledWith(expect.objectContaining({ start_date: '2026-03-01' }))
    expect(getUserSpendingRanking).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('999')
    expect(wrapper.findComponent({ name: 'TokenUsageTrend' }).props('trendData')).toEqual([])
    wrapper.unmount()
  })

  it('abandons retries and queued requests on unmount', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    getSnapshotV2.mockRejectedValue({ status: 503, reason: 'STATISTICS_BUSY' })
    const wrapper = mountDashboard()
    await flushPromises()
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getUserUsageTrend).not.toHaveBeenCalled()
    expect(getUserSpendingRanking).not.toHaveBeenCalled()
  })

  it('uses last 24 hours as default dashboard range', async () => {
    mount(DashboardView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          LoadingSpinner: true,
          Icon: true,
          DateRangePicker: true,
          Select: true,
          ModelDistributionChart: true,
          TokenUsageTrend: true,
          Line: true
        }
      }
    })

    await flushPromises()

    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      start_date: formatLocalDate(yesterday),
      end_date: formatLocalDate(now),
      granularity: 'hour'
    }))
  })
})
