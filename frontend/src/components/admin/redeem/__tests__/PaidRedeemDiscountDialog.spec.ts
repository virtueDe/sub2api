import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import PaidRedeemDiscountDialog from '../PaidRedeemDiscountDialog.vue'

const mocks = vi.hoisted(() => ({ getConfig: vi.fn(), updateConfig: vi.fn(), preview: vi.fn(), backfill: vi.fn(), groups: vi.fn(), success: vi.fn() }))
vi.mock('@/api/admin/paidRedeemDiscount', () => mocks)
vi.mock('@/api/admin/groups', () => ({ getAllIncludingInactive: mocks.groups }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

const config = { enabled: true, group_id: 21, rate_multiplier: 0.01, source_group_ids: [16, 41] }
const preview = {
  config, group_name: 'image target', default_rate: 1, user_count: 1, change_count: 1, preview_token: 'reviewed-users-token',
  users: [{ user_id: 50, email: 'paid@example.com', username: '', current_rate: null, effective_rate: 1, new_rate: 0.01, will_change: true }],
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getConfig.mockResolvedValue({ ...config, source_group_ids: [...config.source_group_ids] })
  mocks.updateConfig.mockImplementation(async value => value)
  mocks.preview.mockResolvedValue(preview)
  mocks.backfill.mockResolvedValue({ user_count: 1, updated_count: 1 })
  mocks.groups.mockResolvedValue([
    { id: 21, name: 'image target', status: 'active', subscription_type: 'standard', allow_image_generation: true, image_rate_independent: false },
    { id: 16, name: 'source one', status: 'active', subscription_type: 'standard' },
    { id: 41, name: 'source two', status: 'inactive', subscription_type: 'standard' },
    { id: 55, name: 'independent images', status: 'active', subscription_type: 'standard', allow_image_generation: true, image_rate_independent: true },
  ])
})

async function openDialog() {
  const wrapper = mount(PaidRedeemDiscountDialog, {
    props: { show: true },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } },
  })
  await flushPromises()
  return wrapper
}

describe('paid user discounts', () => {
  it('loads without applying changes and only offers compatible target groups', async () => {
    const wrapper = await openDialog()
    expect(wrapper.get('[data-testid="multiplier"]').element).toHaveProperty('value', '0.01')
    expect(wrapper.get('[data-testid="target-group"]').text()).toContain('image target')
    expect(wrapper.get('[data-testid="target-group"]').text()).not.toContain('independent images')
    expect(wrapper.find('[data-source-group="41"]').exists()).toBe(true)
    expect(mocks.updateConfig).not.toHaveBeenCalled()
    expect(mocks.backfill).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
  })

  it('saves the decimal rate and exact source IDs before allowing a preview', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="multiplier"]').setValue('0.005')
    expect(wrapper.get('[data-testid="preview"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ ...config, rate_multiplier: 0.005 })
    expect(wrapper.get('[data-testid="preview"]').attributes('disabled')).toBeUndefined()
    expect(mocks.backfill).not.toHaveBeenCalled()
  })

  it('applies only the preview token after showing the affected users', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('paid@example.com')
    expect(mocks.backfill).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="apply"]').trigger('click')
    await flushPromises()
    expect(mocks.backfill).toHaveBeenCalledTimes(1)
    expect(mocks.backfill).toHaveBeenCalledWith('reviewed-users-token')
    expect(wrapper.find('[data-testid="apply"]').exists()).toBe(false)
  })

  it('discards the preview when the source groups change', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="preview"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-source-group="16"]').setValue(false)
    expect(wrapper.find('[data-testid="apply"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="preview"]').attributes('disabled')).toBeDefined()
    expect(mocks.backfill).not.toHaveBeenCalled()
  })

  it('keeps a load failure visible without offering an empty configuration to save', async () => {
    mocks.getConfig.mockRejectedValueOnce(new Error('offline'))
    const wrapper = await openDialog()
    expect(wrapper.get('[role="alert"]').text()).toContain('offline')
    expect(wrapper.find('[data-testid="save"]').exists()).toBe(false)
  })

  it('requires a fresh preview after the server rejects a stale token', async () => {
    mocks.backfill.mockRejectedValueOnce({ status: 409, reason: 'PAID_DISCOUNT_PREVIEW_CHANGED', message: 'Users changed; preview again' })
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="preview"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="apply"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Users changed; preview again')
    expect(wrapper.find('[data-testid="apply"]').exists()).toBe(false)
    expect(mocks.success).not.toHaveBeenCalled()
  })

  it('reloads after a group list failure without allowing incomplete settings to be saved', async () => {
    mocks.groups.mockRejectedValueOnce({ message: 'Failed to load groups' })
    const wrapper = await openDialog()
    expect(wrapper.get('[role="alert"]').text()).toContain('Failed to load groups')
    expect(wrapper.find('[data-testid="save"]').exists()).toBe(false)
    await wrapper.get('[data-testid="reload"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[data-source-group="41"]').exists()).toBe(true)
    expect(mocks.updateConfig).not.toHaveBeenCalled()
  })

  it('requires review of changed server settings before offering a backfill', async () => {
    const wrapper = await openDialog()
    mocks.preview.mockResolvedValue({ ...preview, config: { ...config, group_id: 55, rate_multiplier: 0.02 }, group_name: 'changed target' })
    await wrapper.get('[data-testid="preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('admin.redeem.discount.configChanged')
    expect(wrapper.get('[data-testid="multiplier"]').element).toHaveProperty('value', '0.02')
    expect(wrapper.find('[data-testid="apply"]').exists()).toBe(false)
    await wrapper.get('[data-testid="preview"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="preview-target"]').text()).toContain('#55 changed target')
    expect(wrapper.get('[data-testid="preview-target"]').text()).toContain('0.02×')
    expect(mocks.backfill).not.toHaveBeenCalled()
  })
})
