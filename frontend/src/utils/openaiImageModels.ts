import type { Account } from '@/types'

export function parseOpenAIImageModels(value: string): string[] {
  return [...new Set(value.split(/[\n,，]+/).map((model) => model.trim()).filter(Boolean))]
}

export function isOpenAIAccountImageModel(account: Account | null | undefined, model: string): boolean {
  if (account?.platform !== 'openai' || !model) return false
  const credentials = account.credentials as Record<string, unknown> | undefined
  const mapping = credentials?.model_mapping as Record<string, string> | undefined
  const pattern = Object.keys(mapping ?? {})
    .filter((key) => key.includes('*'))
    .sort((a, b) => b.length - a.length)
    .find((key) => new RegExp(`^${key.split('*').map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('.*')}$`).test(model))
  const upstream = (mapping?.[model] ?? (pattern ? mapping?.[pattern] : undefined) ?? model).trim()
  const capabilities = credentials?.openai_capabilities
  const enabled = Array.isArray(capabilities)
    ? capabilities.some((value) => typeof value === 'string' && value.trim().toLowerCase() === 'images')
    : capabilities !== null && typeof capabilities === 'object' &&
      Object.entries(capabilities).some(([key, value]) => key.trim().toLowerCase() === 'images' && value === true)
  const models = credentials?.openai_image_models
  if (account.type === 'apikey' && enabled) {
    return Array.isArray(models) && models.some((value) => typeof value === 'string' && value.trim() === upstream)
  }
  return upstream.toLowerCase().startsWith('gpt-image-')
}
