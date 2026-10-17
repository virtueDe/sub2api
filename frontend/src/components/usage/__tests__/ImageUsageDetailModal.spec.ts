import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ImageUsageDetailModal from '../ImageUsageDetailModal.vue'
import type { UsageLog } from '@/types'

const mocks = vi.hoisted(() => ({ user: vi.fn(), admin: vi.fn() }))
vi.mock('@/api/usage', () => ({ getById: mocks.user }))
vi.mock('@/api/admin/usage', () => ({ getById: mocks.admin }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))

function record(model: string): UsageLog {
  return { id: 1, model, created_at: '2026-10-14T00:00:00Z', image_generation: { prompt: model, request_params: {}, images: [] } } as UsageLog
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function mountModal(admin = false) {
  return shallowMount(ImageUsageDetailModal, {
    props: { show: true, usageId: 1, admin },
    global: { stubs: { BaseDialog: { template: '<div><slot /></div>' } } }
  })
}

describe('ImageUsageDetailModal', () => {
  beforeEach(() => { vi.clearAllMocks() })

  it('loads user and administrator details through the corresponding API', async () => {
    mocks.user.mockResolvedValue(record('user-image'))
    mocks.admin.mockResolvedValue(record('admin-image'))
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.text()).toContain('user-image')
    expect(mocks.user).toHaveBeenCalledWith(1)
    await wrapper.setProps({ admin: true })
    await flushPromises()
    expect(wrapper.text()).toContain('admin-image')
    expect(mocks.admin).toHaveBeenCalledWith(1)
  })

  it('ignores an old response after selecting another image record', async () => {
    const old = deferred<UsageLog>()
    mocks.user.mockReturnValueOnce(old.promise).mockResolvedValueOnce(record('new-image'))
    const wrapper = mountModal()
    await wrapper.setProps({ usageId: 2 })
    await flushPromises()
    old.resolve(record('old-image'))
    await flushPromises()
    expect(wrapper.text()).toContain('new-image')
    expect(wrapper.text()).not.toContain('old-image')
  })

  it('ignores an old error after closing the modal', async () => {
    const old = deferred<UsageLog>()
    mocks.user.mockReturnValueOnce(old.promise)
    const wrapper = mountModal()
    await wrapper.setProps({ show: false })
    old.reject(new Error('old error'))
    await flushPromises()
    expect(wrapper.text()).not.toContain('usage.imageDetail.loadFailed')
    expect(wrapper.text()).not.toContain('common.loading')
  })
})
