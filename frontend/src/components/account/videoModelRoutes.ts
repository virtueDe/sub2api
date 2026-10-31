export const VIDEO_RESOLUTIONS = ['480p', '540p', '720p', '768p', '1024p', '1080p', '1440p', '2160p']

export interface VideoModelRouteRow {
  model: string
  defaultResolution: string
  targets: { resolution: string; model: string }[]
}

export interface VideoModelRoute {
  default_resolution: string
  resolutions: Record<string, string>
}

export function readVideoModelRoutes(raw: unknown): VideoModelRouteRow[] {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
  return Object.entries(raw).map(([model, route]) => {
    const value = route as VideoModelRoute
    return {
      model,
      defaultResolution: value.default_resolution || '',
      targets: Object.entries(value.resolutions || {}).map(([resolution, target]) => ({ resolution, model: target }))
    }
  })
}

export function buildVideoModelRoutes(rows: VideoModelRouteRow[], ordinary: unknown = undefined): {
  routes: Record<string, VideoModelRoute>; error?: string
} {
  const routes: Record<string, VideoModelRoute> = Object.create(null)
  const fail = (error: string) => ({ routes, error })
  for (const row of rows) {
    const model = row.model.trim()
    if (!model || model.includes('*')) return fail('invalidModel')
    if (routes[model]) return fail('duplicateModel')
    const resolutions: Record<string, string> = Object.create(null)
    for (const target of row.targets) {
      if (!VIDEO_RESOLUTIONS.includes(target.resolution)) return fail('invalidResolution')
      if (resolutions[target.resolution]) return fail('duplicateResolution')
      if (!target.model.trim() || target.model.includes('*')) return fail('missingTarget')
      resolutions[target.resolution] = target.model.trim()
    }
    if (!resolutions[row.defaultResolution]) return fail('invalidDefault')
    const mapped = (ordinary as Record<string, unknown> | undefined)?.[model]
    if (mapped && mapped !== model) return fail('conflictingMapping')
    routes[model] = { default_resolution: row.defaultResolution, resolutions }
  }
  for (const route of Object.values(routes)) {
    if (Object.values(route.resolutions).some(target => routes[target])) return fail('targetIsPublic')
  }
  return { routes }
}
