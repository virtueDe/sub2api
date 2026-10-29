import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { h, resolveComponent } from 'vue'
import { createI18n } from 'vue-i18n'
import { i18n, loadLocaleMessages } from '../index'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('runtime locale messages', () => {
  it.each(['en', 'zh'] as const)('loads %s messages and interpolates parameters', async (locale) => {
    await loadLocaleMessages(locale)
    i18n.global.locale.value = locale

    const translated = i18n.global.t('common.selectedCount', { count: 3 })
    expect(translated).toContain('3')
    expect(translated).not.toContain('{count}')
    expect(translated).not.toBe('common.selectedCount')
  })

  it('supports string interpolation, plurals and linked messages without dynamic code evaluation', () => {
    const composer = createI18n({
      legacy: false,
      locale: 'en',
      messages: {
        en: {
          greeting: 'Hello, {name}',
          items: 'No items | One item | {count} items',
          product: 'Sub2API',
          linked: 'Welcome to @:product'
        }
      }
    }).global
    vi.stubGlobal('Function', function () {
      throw new Error('CSP blocks dynamic code evaluation')
    })

    expect(composer.t('greeting', { name: 'Ada' })).toBe('Hello, Ada')
    expect(composer.t('items', 2)).toBe('2 items')
    expect(composer.t('linked')).toBe('Welcome to Sub2API')
  })

  it('keeps component interpolation available for guide content', () => {
    const plugin = createI18n({
      legacy: false,
      locale: 'en',
      messages: { en: { guide: 'Read {0} docs' } }
    })
    const wrapper = mount({
      render() {
        return h(resolveComponent('i18n-t'), { keypath: 'guide', tag: 'p', scope: 'global' }, {
          default: () => [h('strong', 'API')]
        })
      }
    }, { global: { plugins: [plugin] } })

    expect(wrapper.text()).toBe('Read API docs')
    expect(wrapper.find('strong').text()).toBe('API')
    wrapper.unmount()
  })
})
