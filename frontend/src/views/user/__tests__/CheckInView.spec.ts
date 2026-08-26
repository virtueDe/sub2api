import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { ref } from 'vue'
import CheckInView from '../CheckInView.vue'
import type { CheckinStatus } from '@/api/checkin'

const getStatus = vi.hoisted(() => vi.fn())
const getHistory = vi.hoisted(() => vi.fn())
const checkIn = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const refreshUser = vi.hoisted(() => vi.fn())

const translations: Record<string, string> = {
  'checkin.conditionRequest': '签到条件：今日完成至少 {count} 次请求。',
  'checkin.conditionConsumption': '签到条件：今日消费至少 ${amount}。',
  'checkin.errors.CHECK_IN_REQUEST_THRESHOLD_NOT_MET': '今日请求数未达到签到条件（当前 {current} 次，要求至少 {required} 次）。',
  'checkin.errors.fallback': '签到失败，请稍后重试。',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('zh-CN'),
      t: (key: string, params?: Record<string, unknown>) => {
        let message = translations[key] ?? key
        for (const [name, value] of Object.entries(params ?? {})) {
          message = message.replaceAll(`{${name}}`, String(value))
        }
        return message
      },
    }),
  }
})

vi.mock('@/api/checkin', () => ({
  checkinAPI: { getStatus, getHistory, checkIn },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ refreshUser }),
}))

function statusFixture(condition: '' | 'request' | 'consumption'): CheckinStatus {
  return {
    enabled: true,
    checked_in_today: false,
    today_reward: 0,
    total_reward: 0,
    streak_days: 0,
    account: { id: 7, username: 'demo', email: 'demo@example.com', balance: 10 },
    config: {
      enabled: true,
      reward_min: 1,
      reward_max: 1,
      condition,
      request_threshold: 3,
      consumption_threshold: 2.5,
    },
  }
}

async function mountView(status: CheckinStatus) {
  getStatus.mockResolvedValue(status)
  getHistory.mockResolvedValue({ items: [] })
  const wrapper = shallowMount(CheckInView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
      },
    },
  })
  await flushPromises()
  return wrapper
}

describe('CheckInView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    refreshUser.mockResolvedValue(undefined)
  })

  it('only shows the configured check-in condition', async () => {
    const withoutCondition = await mountView(statusFixture(''))
    expect(withoutCondition.text()).not.toContain('签到条件：')

    const withCondition = await mountView(statusFixture('request'))
    expect(withCondition.text()).toContain('签到条件：今日完成至少 3 次请求。')
  })

  it('shows a specific localized error when the request threshold is not met', async () => {
    checkIn.mockRejectedValue({
      reason: 'CHECK_IN_REQUEST_THRESHOLD_NOT_MET',
      message: 'daily request count does not meet the check-in requirement',
      metadata: { current: '1', required: '3' },
    })
    const wrapper = await mountView(statusFixture('request'))

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('今日请求数未达到签到条件（当前 1 次，要求至少 3 次）。')
  })
})
