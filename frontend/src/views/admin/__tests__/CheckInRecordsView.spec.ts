import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import CheckInRecordsView from '../CheckInRecordsView.vue'

const getRecords = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const formatDateTime = vi.hoisted(() => vi.fn(() => '2026/08/26 09:08:07'))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/api/admin/checkin', () => {
  const api = { getRecords }
  return {
    adminCheckinAPI: api,
    checkinAdminAPI: api,
    default: api,
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('@/utils/format', () => ({ formatDateTime }),
)

describe('CheckInRecordsView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getRecords.mockResolvedValue({
      items: [{
        id: 1,
        user_id: 7,
        email: 'demo@example.com',
        username: 'demo',
        check_in_date: '2026-08-26',
        reward: 1,
        request_count: 3,
        daily_spend: 2.5,
        created_at: '2026-08-26T09:08:07+08:00',
      }],
      total: 1,
      page: 1,
      page_size: 20,
    })
  })

  it('renders the record creation time to seconds', async () => {
    const wrapper = shallowMount(CheckInRecordsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
        },
      },
    })
    await flushPromises()

    expect(formatDateTime).toHaveBeenCalledWith('2026-08-26T09:08:07+08:00')
    expect(wrapper.text()).toContain('2026/08/26 09:08:07')
    expect(wrapper.text()).toContain('$2.5')
  })
})
