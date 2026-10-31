import { describe, expect, it } from 'vitest'
import { buildVideoModelRoutes, readVideoModelRoutes, type VideoModelRouteRow } from '../videoModelRoutes'

const rows = (): VideoModelRouteRow[] => [{ model: 'sd-video', defaultResolution: '720p', targets: [
  { resolution: '480p', model: 'sd-480p' }, { resolution: '720p', model: 'sd-720p' }, { resolution: '1080p', model: 'sd-1080p' }
] }]

describe('video model resolution routes', () => {
  it('round trips account credentials and keeps ordinary aliases independent', () => {
    const result = buildVideoModelRoutes(rows(), { other: 'other-target' })
    expect(result.error).toBeUndefined()
    expect(result.routes).toEqual({ 'sd-video': { default_resolution: '720p', resolutions: { '480p': 'sd-480p', '720p': 'sd-720p', '1080p': 'sd-1080p' } } })
    expect(readVideoModelRoutes(result.routes)).toEqual(rows())
    expect(buildVideoModelRoutes([]).routes).toEqual({})
  })

  it('rejects duplicate tiers and a removed default', () => {
    const value = rows()
    value[0]!.targets.push({ resolution: '720p', model: 'different' })
    expect(buildVideoModelRoutes(value).error).toBe('duplicateResolution')
    value[0]!.targets = [{ resolution: '480p', model: 'sd-480p' }]
    expect(buildVideoModelRoutes(value).error).toBe('invalidDefault')
  })

  it('rejects ambiguous mappings and public targets', () => {
    expect(buildVideoModelRoutes(rows(), { 'sd-video': 'ordinary-target' }).error).toBe('conflictingMapping')
    const value = rows()
    value[0]!.targets[0]!.model = 'sd-video'
    expect(buildVideoModelRoutes(value).error).toBe('targetIsPublic')
  })
})
