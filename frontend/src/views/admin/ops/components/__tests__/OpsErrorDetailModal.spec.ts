import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'

const mocks = vi.hoisted(() => ({
  getRequestErrorDetail: vi.fn(),
  listRequestErrorUpstreamErrors: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRequestErrorDetail: mocks.getRequestErrorDetail,
    getUpstreamErrorDetail: vi.fn(),
    listRequestErrorUpstreamErrors: mocks.listRequestErrorUpstreamErrors
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: mocks.showError })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('OpsErrorDetailModal', () => {
  beforeEach(() => {
    mocks.getRequestErrorDetail.mockReset()
    mocks.showError.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  })

  it('ignores a delayed failure after leaving the page', async () => {
    let rejectOld!: (reason: unknown) => void
    mocks.getRequestErrorDetail.mockReturnValueOnce(new Promise((_, reject) => { rejectOld = reject }))
    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } }
    })
    wrapper.unmount()
    rejectOld(new Error('old error'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it('keeps the selected image failure when an earlier detail request finishes late', async () => {
    let resolveOld!: (value: unknown) => void
    mocks.getRequestErrorDetail.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
      .mockResolvedValueOnce({ id: 2, model: 'current-image', phase: 'request', image_generation: { prompt: 'current prompt', request_params: {}, images: [] } })
    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: { stubs: {
        BaseDialog: { template: '<div><slot /></div>' },
        ImageGenerationDetail: { props: ['detail'], template: '<p>{{ detail?.prompt }}</p>' },
        Icon: true
      } }
    })
    await wrapper.setProps({ errorId: 2 })
    await flushPromises()
    resolveOld({ id: 1, model: 'old-image', phase: 'request', image_generation: { prompt: 'old prompt' } })
    await flushPromises()
    expect(wrapper.text()).toContain('current prompt')
    expect(wrapper.text()).not.toContain('old prompt')
  })

  it('prioritizes upstream root cause and deduplicates diagnostic payloads', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 1,
      created_at: '2026-08-19T00:00:00Z',
      phase: 'request',
      type: 'upstream_error',
      error_owner: 'provider',
      error_source: 'gateway',
      severity: 'P1',
      status_code: 502,
      upstream_status_code: 429,
      platform: 'openai',
      model: 'gpt-5.6',
      resolved: false,
      request_id: 'rid-1',
      message: 'All available accounts exhausted',
      error_body: '{"error":"same"}',
      upstream_error_message: 'provider rate limit exhausted',
      upstream_error_detail: '{"error":"same"}',
      upstream_errors: '[]',
      account_name: 'account',
      group_name: 'group',
      is_business_limited: false
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /></div>' },
          Icon: true
        }
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('provider rate limit exhausted')
    expect(wrapper.text()).toContain('admin.ops.errorDetail.upstreamStatus')
    expect(wrapper.text()).toContain('429')
    expect(wrapper.findAll('pre')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('admin.ops.errorDetail.payloads.upstream_detail')
  })
})
