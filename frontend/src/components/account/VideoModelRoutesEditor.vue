<template>
  <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="video-model-routes">
    <div>
      <h3 class="input-label">{{ t('admin.accounts.videoRoutes.title') }}</h3>
      <p class="input-hint">{{ t('admin.accounts.videoRoutes.hint') }}</p>
    </div>
    <div v-for="(route, index) in routes" :key="index" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-testid="video-route">
      <div class="flex items-end gap-2">
        <label class="min-w-0 flex-1">
          <span class="input-label">{{ t('admin.accounts.videoRoutes.publicModel') }}</span>
          <input v-model="route.model" class="input font-mono" placeholder="sd-video" data-testid="video-route-model" />
        </label>
        <button type="button" class="btn btn-danger" :aria-label="t('admin.accounts.videoRoutes.removeModel')" @click="routes.splice(index, 1)">{{ t('common.delete') }}</button>
      </div>
      <div v-for="(target, targetIndex) in route.targets" :key="targetIndex" class="flex flex-wrap items-end gap-2" data-testid="video-route-target">
        <label class="w-28">
          <span class="input-label">{{ t('admin.accounts.videoRoutes.resolution') }}</span>
          <select v-model="target.resolution" class="input" data-testid="video-route-resolution">
            <option v-for="resolution in VIDEO_RESOLUTIONS" :key="resolution" :value="resolution">{{ resolution }}</option>
          </select>
        </label>
        <label class="min-w-40 flex-1">
          <span class="input-label">{{ t('admin.accounts.videoRoutes.upstreamModel') }}</span>
          <input v-model="target.model" class="input font-mono" placeholder="sd-720p" data-testid="video-route-upstream" />
        </label>
        <button type="button" class="btn btn-secondary" :aria-label="t('admin.accounts.videoRoutes.removeResolution')" @click="route.targets.splice(targetIndex, 1)">{{ t('common.delete') }}</button>
      </div>
      <div class="flex flex-wrap items-end gap-3">
        <label>
          <span class="input-label">{{ t('admin.accounts.videoRoutes.defaultResolution') }}</span>
          <select v-model="route.defaultResolution" class="input" data-testid="video-route-default">
            <option value="">{{ t('admin.accounts.videoRoutes.chooseDefault') }}</option>
            <option v-for="resolution in [...new Set(route.targets.map(target => target.resolution))]" :key="resolution" :value="resolution">{{ resolution }}</option>
          </select>
        </label>
        <button type="button" class="btn btn-secondary" @click="route.targets.push({ resolution: VIDEO_RESOLUTIONS.find(value => !route.targets.some(target => target.resolution === value)) || '720p', model: '' })">{{ t('admin.accounts.videoRoutes.addResolution') }}</button>
      </div>
    </div>
    <button type="button" class="btn btn-secondary" data-testid="add-video-route" @click="routes.push({ model: '', defaultResolution: '720p', targets: [{ resolution: '480p', model: '' }, { resolution: '720p', model: '' }, { resolution: '1080p', model: '' }] })">{{ t('admin.accounts.videoRoutes.addModel') }}</button>
    <p class="input-hint">{{ t('admin.accounts.videoRoutes.pricingHint') }}</p>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { VIDEO_RESOLUTIONS, type VideoModelRouteRow } from './videoModelRoutes'

const routes = defineModel<VideoModelRouteRow[]>({ required: true })
const { t } = useI18n()
</script>
