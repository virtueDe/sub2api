import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProfitAccountingView from '../ProfitAccountingView.vue'

const api = vi.hoisted(() => ({ reports: vi.fn(), prices: vi.fn(), report: vi.fn(), createPrice: vi.fn(), recalculate: vi.fn() }))
const listAccounts = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin/profitAccounting', () => ({ profitAccountingAPI: api }))
vi.mock('@/api/admin/accounts', () => ({ list: listAccounts, default: { list: listAccounts } }))
vi.mock('@/api/admin/channels', () => ({ list: vi.fn().mockResolvedValue({ items: [] }), default: {} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const mountPage = () => shallowMount(ProfitAccountingView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    DataTable: { props: ['data'], template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-profit_usd" :row="row" /><slot name="cell-actions" :row="row" /></div></div>' }
  } }
})

describe('ProfitAccountingView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.reports.mockResolvedValue([])
    api.prices.mockResolvedValue({ items: [], total: 0 })
    listAccounts.mockResolvedValue({ items: [{ id: 1, name: 'Image supplier' }] })
  })

  it('does not display missing profit as zero', async () => {
    api.reports.mockResolvedValue([{ id: 1, date: '2026-11-07', profit_usd: null, status: 'incomplete' }])
    const page = mountPage(); await flushPromises()
    expect(page.text()).toContain('—')
    expect(page.text()).not.toContain('$0.00')
  })

  it('reports loading failures visibly', async () => {
    api.reports.mockRejectedValue(new Error('Unavailable'))
    const page = mountPage(); await flushPromises()
    expect(page.find('[role="alert"]').exists()).toBe(true)
  })

  it('submits the date and reason for explicit recalculation', async () => {
    api.recalculate.mockResolvedValue({ id: 3, date: '2026-11-07' })
    api.report.mockResolvedValue({ id: 3, date: '2026-11-07', revenue_usd: '1', known_cost_usd: '0.2', profit_usd: '0.8', image_count: 1, lines: [], created_at: '2026-11-08T07:00:00+08:00' })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.recalculate')!.trigger('click')
    await page.find('#profit-recalculate-form input').setValue('2026-11-07')
    await page.find('#profit-recalculate-form textarea').setValue('Correct procurement price')
    await page.find('#profit-recalculate-form').trigger('submit'); await flushPromises()
    expect(api.recalculate).toHaveBeenCalledWith('2026-11-07', 'Correct procurement price')
    expect(api.reports).toHaveBeenCalledWith('2026-11-07', '2026-11-07', true)
  })

  it('requires a price and scope before saving', async () => {
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).not.toHaveBeenCalled()
    expect(page.text()).toContain('admin.profitAccounting.required')
  })

  it('keeps the entered procurement price as an exact decimal string', async () => {
    api.createPrice.mockResolvedValue({ id: 1 })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click'); await flushPromises()
    page.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 1)
    await page.find('#procurement-price-form input[maxlength="200"]').setValue('gemini-nano-banana-2.1')
    await page.find('#procurement-price-form input[inputmode="decimal"]').setValue('9999999.1234567891')
    await page.find('#procurement-price-form textarea').setValue('New price')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).toHaveBeenCalledWith(expect.objectContaining({ unit_price_usd: '9999999.1234567891' }))
  })
})
