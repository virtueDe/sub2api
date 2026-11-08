import { describe, expect, it, vi } from 'vitest'
import { Line } from 'vue-chartjs'
import { shallowMount } from '@vue/test-utils'
import ProfitOverview from '../components/ProfitOverview.vue'
import type { ProfitReport } from '@/api/admin/profitAccounting'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const reports = Array.from({ length: 15 }, (_, index) => ({ date: `2026-10-${String(index + 1).padStart(2, '0')}`, status: 'complete', profit_usd: '0.25' } as ProfitReport))
const mount = (items = reports) => shallowMount(ProfitOverview, { props: { reports: items, endDate: '2026-10-15', loading: false, error: '' } })

describe('ProfitOverview', () => {
  it('summarizes yesterday and seven days and orders all fifteen chart dates', () => {
    const page = mount([...reports].reverse())
    expect(page.find('[data-period="yesterdayProfit"]').text()).toContain('$0.25')
    expect(page.find('[data-period="weekProfit"]').text()).toContain('$1.75')
    const data = page.findComponent(Line).props('data')
    expect(data.labels).toHaveLength(15)
    expect(data.labels[0]).toBe('2026-10-01')
    expect(data.labels[14]).toBe('2026-10-15')
  })

  it('preserves missing and incomplete dates as gaps instead of zero', () => {
    const page = mount(reports.filter(report => report.date !== '2026-10-14').map(report => report.date === '2026-10-15' ? { ...report, status: 'incomplete', profit_usd: null } : report))
    expect(page.find('[data-period="weekProfit"]').text()).toContain('—')
    const data = page.findComponent(Line).props('data')
    expect(data.datasets[0].data.slice(-2)).toEqual([null, null])
    expect(data.datasets[0].spanGaps).toBe(false)
  })

  it('shows losses and completed zero profit', () => {
    const page = mount(reports.map(report => ({ ...report, profit_usd: report.date === '2026-10-15' ? '-0.5' : '0' })))
    expect(page.find('[data-period="yesterdayProfit"]').text()).toContain('-$0.50')
    expect(page.findComponent(Line).props('data').datasets[0].data[0]).toBe(0)
  })
})
