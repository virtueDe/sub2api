import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProfitAccountingView from '../ProfitAccountingView.vue'

const api = vi.hoisted(() => ({ reports: vi.fn(), prices: vi.fn(), report: vi.fn(), createPrice: vi.fn(), recalculate: vi.fn() }))
const { listAccounts, getAvailableModels, getAccount } = vi.hoisted(() => ({ listAccounts: vi.fn(), getAvailableModels: vi.fn(), getAccount: vi.fn() }))
vi.mock('@/api/admin/profitAccounting', () => ({ profitAccountingAPI: api }))
vi.mock('@/api/admin/accounts', () => ({ list: listAccounts, getAvailableModels, getById: getAccount, default: { list: listAccounts } }))
vi.mock('@/api/admin/channels', () => ({ list: vi.fn().mockResolvedValue({ items: [] }), default: {} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))

const mountPage = () => shallowMount(ProfitAccountingView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
    DataTable: { props: ['data', 'columns'], template: '<div><div v-for="column in columns" :key="column.key">{{ column.label }}</div><div v-for="row in data" :key="row.id"><slot name="cell-status" :row="row" /><span data-loss><slot name="cell-abnormal_loss_usd" :row="row" /></span><slot name="cell-profit_usd" :row="row" /><slot name="cell-actions" :row="row" /></div></div>' }
  } }
})

describe('ProfitAccountingView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    getAvailableModels.mockResolvedValue([{ id: 'gemini-nano-banana-2.1' }])
    getAccount.mockResolvedValue({ credentials: {} })
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

  it('shows loss separately without subtracting it from server profit and preserves old revisions', async () => {
    const latest = { id: 2, date: '2026-11-09', revision: 2, status: 'complete', revenue_usd: '10', known_cost_usd: '4', abnormal_loss_usd: '1', abnormal_loss_requests: 2, profit_usd: '6', image_count: 3, created_at: '2026-11-14T07:00:00+08:00', lines: [{ account_id: 1, channel_id: 0, model: 'image', image_count: 3, revenue_usd: '10', abnormal_loss_usd: '1', profit_usd: '6', costs: [{ resolution: 'all_tiers', image_count: 1, price_id: 0, price_ids: [1, 2, 3], unit_price_usd: '1', cost_usd: '1', abnormal_loss: true }] }] }
    const old = { ...latest, id: 1, revision: 1, abnormal_loss_usd: undefined, abnormal_loss_requests: undefined, lines: [] }
    api.reports.mockResolvedValue([latest, old])
    api.report.mockImplementation((id: number) => Promise.resolve(id === 2 ? latest : old))
    const page = mountPage(); await flushPromises()
    expect(page.text()).toContain('admin.profitAccounting.abnormalLoss')
    expect(page.findAll('[data-loss]')[0]!.text()).toContain('$1.00')
    expect(page.findAll('[data-loss]')[1]!.text()).toBe('—')
    expect(page.text()).toContain('$6.00')
    expect(page.text()).not.toContain('$5.00')
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.detail')!.trigger('click'); await flushPromises()
    expect(page.find('dl').text()).toContain('$1.00')
    expect(page.find('details').text()).toContain('admin.profitAccounting.allTiers')
    expect(page.find('details').text()).toContain('#1, #2, #3')
    await page.findAll('button').find(b => b.text() === 'v1')!.trigger('click'); await flushPromises()
    expect(page.find('dl').text()).toContain('—')
    expect(page.find('dl').text()).not.toContain('$1.00')
  })

  it('displays a calculated zero loss as zero', async () => {
    api.reports.mockResolvedValue([{ id: 1, status: 'complete', abnormal_loss_usd: '0', profit_usd: '1' }])
    const page = mountPage(); await flushPromises()
    expect(page.find('[data-loss]').text()).toBe('$0.00')
  })

  it('submits the date and reason for explicit recalculation', async () => {
    api.recalculate.mockResolvedValue({ id: 3, date: '2026-11-07' })
    api.report.mockResolvedValue({ id: 3, date: '2026-11-07', revenue_usd: '1', known_cost_usd: '0.2', profit_usd: '0.8', image_count: 1, lines: [], created_at: '2026-11-08T07:00:00+08:00' })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.chooseRecalculateDate')!.trigger('click')
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
    await flushPromises()
    await page.find('#procurement-price-form input[inputmode="decimal"]').setValue('9999999.1234567891')
    await page.find('#procurement-price-form textarea').setValue('New price')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).toHaveBeenCalledWith(expect.objectContaining({ unit_price_usd: '9999999.1234567891', scope_type: 'account', scope_id: 1, model: 'gemini-nano-banana-2.1' }))
  })
  it('loads account models and deduplicates their actual upstream names', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'alias-a' }, { id: 'alias-b' }])
    getAccount.mockResolvedValue({ credentials: { model_mapping: { 'alias-a': 'upstream-image', 'alias-b': 'upstream-image' } } })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    expect(page.find('option[value="channel"]').exists()).toBe(false)
    page.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 1)
    await flushPromises()
    expect(getAvailableModels).toHaveBeenCalledWith(1)
    expect(getAccount).toHaveBeenCalledWith(1)
    expect(page.findAll('[data-model]')).toHaveLength(1)
    expect(page.find('[data-model]').attributes('data-model')).toBe('upstream-image')
  })

  it('ignores stale model responses after switching accounts', async () => {
    let resolveOld!: (models: { id: string }[]) => void
    getAvailableModels.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    const account = page.findComponent({ name: 'Select' })
    account.vm.$emit('update:modelValue', 1); await flushPromises()
    account.vm.$emit('update:modelValue', 2); await flushPromises()
    resolveOld([{ id: 'old-model' }]); await flushPromises()
    expect(page.find('[data-model]').attributes('data-model')).toBe('gemini-nano-banana-2.1')
    expect(page.text()).not.toContain('old-model')
  })

  it('clears the previous model and shows model loading failures', async () => {
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    const account = page.findComponent({ name: 'Select' })
    account.vm.$emit('update:modelValue', 1); await flushPromises()
    getAvailableModels.mockRejectedValueOnce(new Error('Models unavailable'))
    account.vm.$emit('update:modelValue', 2); await flushPromises()
    expect(page.findAll('[data-model]')).toHaveLength(0)
    expect(page.text()).toContain('Models unavailable')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).not.toHaveBeenCalled()
  })

  it('saves all models with shared or individual resolution prices and skips blanks', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'model-a' }, { id: 'model-b' }, { id: 'model-c' }])
    api.createPrice.mockResolvedValue({ id: 1 })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    page.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 1); await flushPromises()
    expect(page.findAll('[data-model]')).toHaveLength(3)
    await page.find('[data-model="model-a"] input[inputmode="decimal"]').setValue('0.0123456789')
    await page.find('[data-model="model-b"] input[type="checkbox"]').setValue(true)
    const inputs = page.findAll('[data-model="model-b"] input[inputmode="decimal"]')
    for (const [i, input] of inputs.entries()) await input.setValue(['0', '0.02', '0.04'][i])
    await page.find('#procurement-price-form textarea').setValue('Bulk pricing')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).toHaveBeenCalledTimes(6)
    expect(api.createPrice.mock.calls.map(([job]) => [job.model, job.resolution, job.unit_price_usd])).toEqual([
      ['model-a', '1K', '0.0123456789'], ['model-a', '2K', '0.0123456789'], ['model-a', '4K', '0.0123456789'],
      ['model-b', '1K', '0'], ['model-b', '2K', '0.02'], ['model-b', '4K', '0.04']
    ])
    expect(new Set(api.createPrice.mock.calls.map(([job]) => job.effective_at)).size).toBe(1)
  })

  it('retries only remaining prices after a partial failure', async () => {
    api.createPrice.mockResolvedValueOnce({ id: 1 }).mockRejectedValueOnce(new Error('Save failed')).mockResolvedValue({ id: 2 })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    page.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 1); await flushPromises()
    await page.find('input[inputmode="decimal"]').setValue('0.1')
    await page.find('#procurement-price-form textarea').setValue('Bulk pricing')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(page.text()).toContain('Save failed')
    expect(page.find('fieldset').attributes('disabled')).toBeDefined()
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice.mock.calls.map(([job]) => job.resolution)).toEqual(['1K', '2K', '2K', '4K'])
  })

  it('copies the shared price when enabling resolution pricing and validates before saving', async () => {
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    page.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 1); await flushPromises()
    await page.find('input[inputmode="decimal"]').setValue('0.1')
    await page.find('input[type="checkbox"]').setValue(true)
    const inputs = page.findAll('input[inputmode="decimal"]')
    expect(inputs.map(input => (input.element as HTMLInputElement).value)).toEqual(['0.1', '0.1', '0.1'])
    await inputs[2]!.setValue('-1')
    await page.find('#procurement-price-form textarea').setValue('Bulk pricing')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).not.toHaveBeenCalled()
  })

  it('keeps a historical model available when adjusting one resolution', async () => {
    api.prices.mockResolvedValue({ items: [{ id: 1, scope_type: 'account', scope_id: 1, model: 'retired-model', resolution: '2K', unit_price_usd: '0.25' }], total: 1 })
    api.createPrice.mockResolvedValue({ id: 2 })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.prices')!.trigger('click')
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.changePrice')!.trigger('click'); await flushPromises()
    const inputs = page.findAll('[data-model="retired-model"] input[inputmode="decimal"]')
    expect(inputs.map(input => (input.element as HTMLInputElement).value)).toEqual(['', '0.25', ''])
    await page.find('#procurement-price-form textarea').setValue('Historical correction')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(api.createPrice).toHaveBeenCalledTimes(1)
    expect(api.createPrice).toHaveBeenCalledWith(expect.objectContaining({ model: 'retired-model', resolution: '2K', unit_price_usd: '0.25' }))
  })

  it('shows missing reasons and preselects the row date for recalculation', async () => {
    api.reports.mockResolvedValue([{ id: 1, date: '2026-10-15', status: 'incomplete', lines: [{ costs: [{ missing_reason: 'missing_price' }] }] }])
    const page = mountPage(); await flushPromises()
    expect(page.text()).toContain('admin.profitAccounting.missingPrice')
    expect(page.text()).toContain('admin.profitAccounting.queryHelp')
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.recalculateDay')!.trigger('click')
    expect((page.find('#profit-recalculate-form input').element as HTMLInputElement).value).toBe('2026-10-15')
    expect(page.text()).toContain('admin.profitAccounting.singleDayHelp')
    expect(api.recalculate).not.toHaveBeenCalled()
  })

  it('keeps a historical price reminder and offers the matching recalculation date', async () => {
    api.createPrice.mockResolvedValue({ id: 1 })
    const page = mountPage(); await flushPromises()
    await page.findAll('button').find(b => b.text() === 'admin.profitAccounting.addPrice')!.trigger('click')
    page.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 1); await flushPromises()
    await page.find('input[inputmode="decimal"]').setValue('0.02')
    await page.find('input[type="datetime-local"]').setValue('2026-10-15T00:00')
    await page.find('#procurement-price-form textarea').setValue('Historical cost')
    await page.find('#procurement-price-form').trigger('submit'); await flushPromises()
    expect(page.text()).toContain('admin.profitAccounting.savedFrom')
    await page.find('[role="status"] button').trigger('click')
    expect((page.find('#profit-recalculate-form input').element as HTMLInputElement).value).toBe('2026-10-15')
    expect(api.recalculate).not.toHaveBeenCalled()
  })

})
