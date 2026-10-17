import { describe, expect, it } from 'vitest'
import { resolveUsageRequestType, requestTypeToLegacyStream } from '@/utils/usageRequestType'
import { numericRequestTypeKind, requestTypeLabelKey } from '@/utils/errorBadges'

describe('image request execution types', () => {
  it.each(['image_sync', 'image_async'] as const)('preserves %s regardless of transport', (type) => {
    expect(resolveUsageRequestType({ request_type: type, stream: true, openai_ws_mode: true })).toBe(type)
    expect(requestTypeToLegacyStream(type)).toBeNull()
    expect(requestTypeLabelKey(type)).toBe(type === 'image_sync' ? 'usage.imageSync' : 'usage.imageAsync')
  })

  it('uses the same types for error logs', () => {
    expect(numericRequestTypeKind(6)).toBe('image_sync')
    expect(numericRequestTypeKind(7)).toBe('image_async')
  })

  it('preserves legacy transport fallback', () => {
    expect(resolveUsageRequestType({ stream: true })).toBe('stream')
    expect(resolveUsageRequestType({ stream: false })).toBe('sync')
    expect(requestTypeToLegacyStream('stream')).toBe(true)
    expect(requestTypeToLegacyStream('sync')).toBe(false)
  })
})
