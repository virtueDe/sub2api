import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ModelPlazaDisplayDialog from '../ModelPlazaDisplayDialog.vue'

const mocks = vi.hoisted(() => ({ getConfig: vi.fn(), updateConfig: vi.fn(), getGroupModels: vi.fn(), groups: vi.fn(), success: vi.fn() }))
vi.mock('@/api/admin/modelPlazaDisplay', () => mocks)
vi.mock('@/api/admin/groups', () => ({ getAllIncludingInactive: mocks.groups }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: mocks.success }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getConfig.mockResolvedValue({ mode: 'automatic', groups: [] })
  mocks.updateConfig.mockImplementation(async value => structuredClone(value))
  mocks.getGroupModels.mockImplementation(async (id: number) => {
    if (id === 21) return [{ name: 'gpt-5.4', platform: 'openai' }, { name: 'gpt-image-2.6-sunburst', platform: 'openai' }]
    if (id === 22) return [{ name: 'gemini-2.5-pro', platform: 'gemini' }, { name: 'gpt-5.4', platform: 'openai' }]
    return []
  })
  mocks.groups.mockResolvedValue([
    { id: 21, name: 'Image group without channel', platform: 'openai', status: 'active', model_pricing: [] },
    { id: 22, name: 'Composite group', platform: 'composite', status: 'active', model_pricing: [] },
    { id: 23, name: 'Inactive group', platform: 'openai', status: 'inactive' },
    { id: 24, name: 'Empty group', platform: 'openai', status: 'active', model_pricing: [] },
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
  it('loads account models even without group pricing and saves only the selected group', async () => {
    const wrapper = await openDialog()
    expect(mocks.updateConfig).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    expect(wrapper.get('[data-group-id="23"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await flushPromises()
    expect(mocks.getGroupModels).toHaveBeenCalledWith(21)
    expect(wrapper.get('[data-models-for="21"]').text()).toContain('gpt-image-2.6-sunburst')
    expect(wrapper.get('[data-models-for="21"]').findAll('span').map(model => model.text())).toEqual(['gpt-5.4', 'gpt-image-2.6-sunburst'])
    expect(wrapper.find('[data-add-model]').exists()).toBe(false)
    expect(wrapper.find('[data-model-name]').exists()).toBe(false)
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'manual', groups: [{ group_id: 21, models: [] }] })
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

  it('automatically displays account models of a composite group', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="22"]').setValue(true)
    await flushPromises()
    expect(wrapper.get('[data-models-for="22"]').text()).toContain('gemini-2.5-pro')
    expect(wrapper.get('[data-models-for="22"]').text()).toContain('gpt-5.4')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig.mock.calls[0][0].groups).toEqual([{ group_id: 22, models: [] }])
  })

  it('shows an empty state when the selected group has no account models', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="24"]').setValue(true)
    await flushPromises()
    expect(wrapper.get('[data-entry-id="24"]').text()).toContain('noGroupModels')
    expect(wrapper.find('[data-models-for="24"]').exists()).toBe(false)
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'manual', groups: [{ group_id: 24, models: [] }] })
  })

  it('uses current group models instead of the legacy saved model snapshot', async () => {
    mocks.getConfig.mockResolvedValueOnce({ mode: 'manual', groups: [{ group_id: 21, models: [{ name: 'legacy-model', platform: 'openai' }] }] })
    const wrapper = await openDialog()
    expect(wrapper.get('[data-models-for="21"]').text()).toContain('gpt-5.4')
    expect(wrapper.text()).not.toContain('legacy-model')
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-group-id="22"]').setValue(true)
    await flushPromises()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'manual', groups: [{ group_id: 21, models: [] }, { group_id: 22, models: [] }] })
  })

  it('clears hidden stale selections when switching back to automatic mode', async () => {
    mocks.getConfig.mockResolvedValueOnce({ mode: 'manual', groups: [{ group_id: 999, models: [] }] })
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('automatic')
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith({ mode: 'automatic', groups: [] })
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    expect(wrapper.find('[data-entry-id]').exists()).toBe(false)
  })

  it('does not revive manual selections after switching modes without saving', async () => {
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await wrapper.get('[data-testid="mode"]').setValue('automatic')
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    expect(wrapper.find('[data-entry-id]').exists()).toBe(false)
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
    mocks.updateConfig.mockRejectedValueOnce({ message: 'Selected group is unavailable' })
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await flushPromises()
    await wrapper.get('[data-testid="save"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Selected group is unavailable')
    expect(wrapper.get('[data-models-for="21"]').text()).toContain('gpt-image-2.6-sunburst')
    expect(mocks.success).not.toHaveBeenCalled()
  })

  it('blocks saving during model loading and supports retry after failure', async () => {
    let rejectModels!: (reason: unknown) => void
    mocks.getGroupModels.mockImplementationOnce(() => new Promise((_, reject) => { rejectModels = reject }))
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="21"]').setValue(true)
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
    rejectModels({ message: 'Upstream unavailable' })
    await flushPromises()
    expect(wrapper.get('[data-model-error="21"]').text()).toContain('Upstream unavailable')
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-retry-models="21"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-model-error="21"]').exists()).toBe(false)
    expect(wrapper.get('[data-models-for="21"]').text()).toContain('gpt-5.4')
    expect(wrapper.get('[data-testid="save"]').attributes('disabled')).toBeUndefined()
  })

  it('ignores an old response after removing and reselecting the same group', async () => {
    let resolveModels!: (models: unknown[]) => void
    mocks.getGroupModels.mockImplementationOnce(() => new Promise(resolve => { resolveModels = resolve }))
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await wrapper.get('[data-group-id="21"]').setValue(false)
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await flushPromises()
    resolveModels([{ name: 'stale-model', platform: 'openai' }])
    await flushPromises()
    expect(wrapper.get('[data-models-for="21"]').text()).toContain('gpt-5.4')
    expect(wrapper.text()).not.toContain('stale-model')
  })

  it('ignores model responses after switching to automatic mode', async () => {
    let rejectModels!: (reason: unknown) => void
    mocks.getGroupModels.mockImplementationOnce(() => new Promise((_, reject) => { rejectModels = reject }))
    const wrapper = await openDialog()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    await wrapper.get('[data-group-id="21"]').setValue(true)
    await wrapper.get('[data-testid="mode"]').setValue('automatic')
    rejectModels({ message: 'Old request failed' })
    await flushPromises()
    await wrapper.get('[data-testid="mode"]').setValue('manual')
    expect(wrapper.find('[data-entry-id]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Old request failed')
  })
})
