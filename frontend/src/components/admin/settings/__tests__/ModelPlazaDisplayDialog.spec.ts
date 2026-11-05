import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ModelPlazaDisplayDialog from '../ModelPlazaDisplayDialog.vue'

const mocks = vi.hoisted(() => ({ getConfig: vi.fn(), updateConfig: vi.fn(), groups: vi.fn(), success: vi.fn() }))
vi.mock('@/api/admin/modelPlazaDisplay', () => mocks)
vi.mock('@/api/admin/groups', () => ({ getAllIncludingInactive: mocks.groups }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getConfig.mockResolvedValue({ mode: 'automatic', groups: [] })
  mocks.updateConfig.mockImplementation(async value => structuredClone(value))
  mocks.groups.mockResolvedValue([
    { id: 21, name: 'Image group without channel', platform: 'openai', status: 'active' },
    { id: 22, name: 'Composite group', platform: 'composite', status: 'active' },
    { id: 23, name: 'Inactive group', platform: 'openai', status: 'inactive' },
  ])
})

async function openDialog() {
  const wrapper = mount(ModelPlazaDisplayDialog, {
    props: { show: true },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } },
  })
  await flushPromises()
  return wrapper
}

describe('Model Plaza display configuration', () => {
  it('saves an exact group selection and an explicit model without a channel', async () => {
    const wrapper = await openDialog()
    expect(mocks.updateConfig).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    expect(wrapper.get('[data-group-id="23"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await wrapper.get('[data-add-model="21"]').trigger('click')
    await wrapper.get('[data-model-name="21-0"]').setValue(' gpt-image-2.6-sunburst ')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'manual', groups: [{ group_id: 21, models: [{ name: 'gpt-image-2.6-sunburst', platform: 'openai' }] }] })
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
  })

  it('preserves an explicitly empty manual selection', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'manual', groups: [] })
  })

  it('retains missing group IDs until the administrator explicitly removes them', async () => {
    mocks.getConfig.mockResolvedValueOnce({ mode: 'manual', groups: [{ group_id: 999, models: [] }] })
    const wrapper = await openDialog()
    expect(wrapper.get('[data-entry-id="999"]').text()).toContain('unavailable')
    await wrapper.get('[data-remove-group="999"]').trigger('click')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'manual', groups: [] })
  })

  it('supports a concrete platform for each composite model', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="22"]').setValue(true)
    await wrapper.get('[data-add-model="22"]').trigger('click')
    await wrapper.get('[data-model-platform="22-0"]').setValue('gemini')
    await wrapper.get('[data-model-name="22-0"]').setValue('gemini-2.5-pro')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig.mock.calls[0][0].groups[0].models).toEqual([{ name: 'gemini-2.5-pro', platform: 'gemini' }])
  })

  it('clears hidden stale selections when switching back to automatic mode', async () => {
    mocks.getConfig.mockResolvedValueOnce({ mode: 'manual', groups: [{ group_id: 999, models: [] }] })
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('automatic')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'automatic', groups: [] })
  })

  it('prevents saving after a group load failure and supports retry', async () => {
    mocks.groups.mockRejectedValueOnce({ message: 'Groups unavailable' })
    const wrapper = await openDialog()
    expect(wrapper.get('[role="alert"]').text()).toContain('Groups unavailable')
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="reload"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="mode"]').exists()).toBe(true)
  })

  it('shows server validation errors and retains unsaved selections', async () => {
    mocks.updateConfig.mockRejectedValueOnce({ message: 'Use a specific model name' })
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await wrapper.get('[data-add-model="21"]').trigger('click')
    await wrapper.get('[data-model-name="21-0"]').setValue('gpt-*')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Use a specific model name')
    expect(wrapper.get('[data-model-name="21-0"]').element).toHaveProperty('value', 'gpt-*')
    expect(mocks.success).not.toHaveBeenCalled()
  })
})
