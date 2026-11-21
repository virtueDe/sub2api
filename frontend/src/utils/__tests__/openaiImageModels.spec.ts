import { describe, expect, it } from 'vitest'
import type { Account } from '@/types'
import { isOpenAIAccountImageModel, parseOpenAIImageModels } from '../openaiImageModels'

describe('compatible image model configuration', () => {
  const account = {
    platform: 'openai', type: 'apikey', credentials: {
      openai_capabilities: ['images'], openai_image_models: ['seedream-5.5', 'vendor-art-v9'],
      model_mapping: { paint: 'seedream-5.5', 'art-*': 'vendor-art-v9', 'gpt-image-text': 'text-model' }
    }
  } as unknown as Account

  it('parses distinct model IDs from lines and commas', () => {
    expect(parseOpenAIImageModels(' seedream-5.5, vendor-art-v9\nseedream-5.5， ')).toEqual(['seedream-5.5', 'vendor-art-v9'])
  })

  it('uses mapped model capability and keeps ordinary models out', () => {
    for (const id of ['paint', 'art-new', 'seedream-5.5', 'vendor-art-v9']) {
      expect(isOpenAIAccountImageModel(account, id)).toBe(true)
    }
    expect(isOpenAIAccountImageModel(account, 'gpt-image-text')).toBe(false)
    expect(isOpenAIAccountImageModel(account, 'chat-model')).toBe(false)
    expect(isOpenAIAccountImageModel(account, 'gpt-image-2')).toBe(false)
  })

  it('ignores compatible image declarations on OAuth and setup token accounts', () => {
    for (const type of ['oauth', 'setup-token'] as const) {
      expect(isOpenAIAccountImageModel({ ...account, type }, 'paint')).toBe(false)
    }
    expect(isOpenAIAccountImageModel({ ...account, credentials: { ...account.credentials, openai_capabilities: ['chat_completions'] } }, 'paint')).toBe(false)
  })

  it.each([{ capabilities: [' Images '] }, { capabilities: { ' Images ': true } }])('normalizes API-written capability values: $capabilities', ({ capabilities }) => {
    const configured = { ...account, credentials: { ...account.credentials, openai_capabilities: capabilities } }
    expect(isOpenAIAccountImageModel(configured, 'paint')).toBe(true)
  })
})
