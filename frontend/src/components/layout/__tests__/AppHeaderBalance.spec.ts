import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { reactive, nextTick } from 'vue'
import AppHeader from '../AppHeader.vue'

const mock = vi.hoisted(() => ({
  auth: { user: { id: 1, email: 'test@example.test', username: 'test', role: 'user', balance: 8, frozen_balance: 2 }, isAdmin: false, isSimpleMode: false, refreshUser: vi.fn() },
}))
vi.mock('@/stores', () => ({
  useAuthStore: () => mock.auth,
  useAppStore: () => ({ docUrl: '', contactInfo: '', cachedPublicSettings: null }),
  useOnboardingStore: () => ({ replay: vi.fn() }),
}))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }), useRoute: () => ({ meta: {}, params: {} }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => (({ 'common.imageHeldBalance': '生图暂扣金额', 'common.availableBalance': '可用余额', 'common.totalBalance': '总余额' } as Record<string, string>)[key] || key) }) }))
vi.mock('@/router/title', () => ({ resolveRouteMetaKeys: () => ({}) }))
vi.mock('@/utils/featureFlags', () => ({ FeatureFlags: {}, isFeatureFlagEnabled: () => false, resolveFeatureFlag: () => false }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))

vi.mock('@/components/common/LocaleSwitcher.vue', () => ({ default: { template: '<span />' } }))
vi.mock('@/components/common/SubscriptionProgressMini.vue', () => ({ default: { template: '<span />' } }))
vi.mock('@/components/common/AnnouncementBell.vue', () => ({ default: { template: '<span />' } }))

const mountHeader = () => shallowMount(AppHeader, { global: { stubs: { RouterLink: true, transition: { props: ['name'], template: '<slot />' } } } })

describe('image reservation balance display', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-10T00:00:00Z'))
    mock.auth = reactive({ ...mock.auth, user: { ...mock.auth.user, balance: 8, frozen_balance: 2 }, refreshUser: vi.fn().mockResolvedValue(undefined) })
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
  })
  afterEach(() => { vi.useRealTimers() })

  it('shows available and held balances without subtracting the hold twice', async () => {
    const w = mountHeader()
    expect(w.text()).toContain('生图暂扣金额')
    expect(w.text()).toContain('$8.00')
    expect(w.text()).toContain('$2.00')
    expect(w.text()).toContain('$10.00')
    mock.auth.user.balance = 8.75
    mock.auth.user.frozen_balance = 0
    await nextTick()
    expect(w.text()).toContain('$8.75')
    expect(w.text()).not.toContain('$10.00')
    w.unmount()
  })

  it('preserves small holds and displays them in the mobile menu', async () => {
    mock.auth.user.frozen_balance = 0.00007813
    const w = mountHeader()
    expect(w.text()).toContain('$0.00007813')
    await w.get('button[aria-label="common.userMenu"]').trigger('click')
    expect(w.get('.sm\\:hidden').text()).toContain('生图暂扣金额 $0.00007813')
    w.unmount()
  })

  it('refreshes while visible, throttles repeated opening and stops when hidden', async () => {
    const w = mountHeader()
    const balance = w.get('[tabindex="0"]')
    await balance.trigger('mouseenter')
    await flushPromises()
    expect(mock.auth.refreshUser).toHaveBeenCalledTimes(1)
    await balance.trigger('mouseleave')
    await balance.trigger('mouseenter')
    await flushPromises()
    expect(mock.auth.refreshUser).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(10000)
    expect(mock.auth.refreshUser).toHaveBeenCalledTimes(2)
    await balance.trigger('mouseleave')
    await vi.advanceTimersByTimeAsync(20000)
    expect(mock.auth.refreshUser).toHaveBeenCalledTimes(2)
    w.unmount()
  })

  it('keeps focused balance refreshing after mouse leave, and clears timers on unmount', async () => {
    const w = mountHeader()
    const balance = w.get('[tabindex="0"]')
    await balance.trigger('mouseenter')
    await balance.trigger('focusin')
    await balance.trigger('mouseleave')
    await vi.advanceTimersByTimeAsync(10000)
    expect(mock.auth.refreshUser).toHaveBeenCalledTimes(2)
    w.unmount()
    await vi.advanceTimersByTimeAsync(20000)
    expect(mock.auth.refreshUser).toHaveBeenCalledTimes(2)
  })
})
