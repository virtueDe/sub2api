import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { ref } from 'vue'
import DailyTokenRankingView from '../DailyTokenRankingView.vue'

const getDailyTokenRanking = vi.hoisted(() => vi.fn())

vi.mock('@/api/usage', () => ({
  getDailyTokenRanking,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('zh-CN'),
      t: (key: string) => key,
    }),
  }
})

describe('DailyTokenRankingView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getDailyTokenRanking.mockResolvedValue({
      ranking: [
        { rank: 1, display_name: 'x***g', total_tokens: 1200 },
        { rank: 2, display_name: 'd***o@example.com', total_tokens: 800 },
        { rank: 3, display_name: 'a***a', total_tokens: 600 },
        { rank: 4, display_name: 'u***r', total_tokens: 400 },
      ],
      date: '2026-08-26',
      timezone: 'Asia/Shanghai',
      updated_at: '2026-08-26T10:00:00+08:00',
    })
  })

  it('renders only masked identities and highlights the top ranks', async () => {
    const wrapper = shallowMount(DailyTokenRankingView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
        },
      },
    })
    await flushPromises()

    expect(getDailyTokenRanking).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('x***g')
    expect(wrapper.text()).toContain('d***o@example.com')
    expect(wrapper.text()).not.toContain('xiaoming')
    expect(wrapper.find('.rank-row-gold').exists()).toBe(true)
    expect(wrapper.find('.rank-row-silver').exists()).toBe(true)
    expect(wrapper.find('.rank-row-bronze').exists()).toBe(true)
    expect(wrapper.find('.podium-slot-1').exists()).toBe(true)
    expect(wrapper.find('.podium-slot-2').exists()).toBe(true)
    expect(wrapper.find('.podium-slot-3').exists()).toBe(true)
    expect(wrapper.find('.ranking-list .ranking-row').exists()).toBe(true)
  })
})
