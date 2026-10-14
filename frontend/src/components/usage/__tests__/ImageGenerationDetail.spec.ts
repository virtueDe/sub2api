import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ImageGenerationDetail from '../ImageGenerationDetail.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('ImageGenerationDetail', () => {
  it('distinguishes historical records from failed requests without images', async () => {
    const wrapper = mount(ImageGenerationDetail, { props: { detail: null } })
    expect(wrapper.text()).toContain('usage.imageDetail.notRecorded')
    await wrapper.setProps({ detail: { prompt: 'cat', request_params: { size: '1024x1024' }, images: [] } })
    expect(wrapper.text()).toContain('cat')
    expect(wrapper.text()).toContain('1024x1024')
    expect(wrapper.text()).toContain('usage.imageDetail.noImages')
    expect(wrapper.text()).not.toContain('usage.imageDetail.notRecorded')
  })

  it('renders partial images, rejects unsafe URLs and resets image errors for a new record', async () => {
    const wrapper = mount(ImageGenerationDetail, { props: { detail: {
      prompt: 'cat', request_params: {}, images: [
        { b64_json: 'aW1hZ2U=', mime_type: 'image/jpeg', partial: true },
        { url: 'javascript:alert(1)' },
        { url: 'https://example.com/cat.png', revised_prompt: 'a black cat' }
      ]
    } } })
    expect(wrapper.findAll('img')).toHaveLength(2)
    expect(wrapper.find('img').attributes('src')).toBe('data:image/jpeg;base64,aW1hZ2U=')
    expect(wrapper.text()).toContain('usage.imageDetail.partial')
    expect(wrapper.text()).toContain('a black cat')
    await wrapper.find('img').trigger('error')
    expect(wrapper.findAll('img')).toHaveLength(1)
    await wrapper.setProps({ detail: { prompt: 'dog', request_params: {}, images: [{ url: 'https://example.com/dog.png' }] } })
    expect(wrapper.find('img').attributes('src')).toBe('https://example.com/dog.png')
  })
})
