<template>
  <BaseDialog :show="show" :title="t('admin.settings.features.modelPlaza.displayTitle')" width="wide"
    :show-close-button="!saving" :close-on-escape="!saving" @close="!saving && emit('close')">
    <p v-if="loading" class="py-6 text-center text-gray-500">{{ t('common.loading') }}</p>
    <div v-else class="space-y-4">
      <div v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">
        {{ error }}
        <button v-if="!loaded" type="button" data-testid="reload" class="ml-3 underline" @click="load">{{ t('common.refresh') }}</button>
      </div>
      <fieldset v-if="loaded" :disabled="saving" class="space-y-4">
        <p class="text-sm text-gray-500">{{ t('admin.settings.features.modelPlaza.displayHint') }}</p>
        <label class="block">
          <span class="input-label">{{ t('admin.settings.features.modelPlaza.displayMode') }}</span>
          <select v-model="form.mode" data-testid="mode" class="input">
            <option value="automatic">{{ t('admin.settings.features.modelPlaza.automatic') }}</option>
            <option value="manual">{{ t('admin.settings.features.modelPlaza.manual') }}</option>
          </select>
        </label>
        <p v-if="form.mode === 'automatic'" class="text-xs text-gray-500">{{ t('admin.settings.features.modelPlaza.automaticHint') }}</p>
        <template v-if="form.mode === 'manual'">
          <p class="text-sm text-gray-500">{{ t('admin.settings.features.modelPlaza.selectionHint') }}</p>
          <input v-model="search" data-testid="search" type="search" class="input" :aria-label="t('common.search')" :placeholder="t('common.search')" />
          <div class="max-h-48 space-y-1 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-dark-600">
            <label v-for="group in visibleGroups" :key="group.id" class="flex items-center gap-2 rounded p-2 text-sm">
              <input type="checkbox" :data-group-id="group.id" :checked="isSelected(group.id)"
                :disabled="group.status !== 'active' && !isSelected(group.id)" @change="toggleGroup(group.id)" />
              <span>#{{ group.id }} {{ group.name }}</span>
              <span class="ml-auto text-xs text-gray-500">{{ group.platform }}{{ group.status !== 'active' ? ` · ${t('admin.settings.features.modelPlaza.unavailable')}` : '' }}</span>
            </label>
          </div>
          <div v-for="entry in form.groups" :key="entry.group_id" :data-entry-id="entry.group_id" class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
            <div class="flex items-center justify-between gap-3">
              <p class="text-sm font-medium">#{{ entry.group_id }} {{ groupFor(entry.group_id)?.name || t('admin.settings.features.modelPlaza.unavailable') }}</p>
              <button type="button" class="btn btn-ghost btn-sm" :data-remove-group="entry.group_id" @click="toggleGroup(entry.group_id)">{{ t('common.remove') }}</button>
            </div>
            <p class="text-xs text-gray-500">{{ t('admin.settings.features.modelPlaza.modelsHint') }}</p>
            <div v-for="(model, index) in entry.models" :key="index" class="flex flex-wrap items-center gap-2">
              <select v-model="model.platform" :data-model-platform="`${entry.group_id}-${index}`" class="input w-36" :aria-label="t('admin.settings.features.modelPlaza.modelPlatform')">
                <option v-for="platform in modelPlatforms(entry.group_id)" :key="platform.value" :value="platform.value">{{ platform.label }}</option>
              </select>
              <input v-model="model.name" :data-model-name="`${entry.group_id}-${index}`" class="input min-w-40 flex-1 font-mono text-sm" :aria-label="t('admin.settings.features.modelPlaza.modelName')" placeholder="gpt-image-2.6-sunburst" />
              <button type="button" class="btn btn-ghost btn-sm" :aria-label="t('admin.settings.features.modelPlaza.removeModel')" @click="entry.models.splice(index, 1)">{{ t('common.remove') }}</button>
            </div>
            <button type="button" :data-add-model="entry.group_id" class="btn btn-secondary btn-sm" @click="addModel(entry)">{{ t('admin.settings.features.modelPlaza.addModel') }}</button>
          </div>
        </template>
        <p class="text-xs text-gray-500">{{ t('admin.settings.features.modelPlaza.separateSaveHint') }}</p>
      </fieldset>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" data-testid="save" class="btn btn-primary" :disabled="loading || saving || !loaded || !dirty" @click="save">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { getAllIncludingInactive } from '@/api/admin/groups'
import * as displayAPI from '@/api/admin/modelPlazaDisplay'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { AdminGroup } from '@/types'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const app = useAppStore()
const form = reactive<displayAPI.PlazaDisplayConfig>({ mode: 'automatic', groups: [] })
const groups = ref<AdminGroup[]>([])
const search = ref('')
const saved = ref('')
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const error = ref('')
const dirty = computed(() => saved.value !== JSON.stringify(form))
const visibleGroups = computed(() => {
  const query = search.value.trim().toLowerCase()
  return groups.value.filter(group => `${group.id} ${group.name}`.toLowerCase().includes(query))
})
let loadVersion = 0

function errorMessage(err: unknown, key: string) {
  return (err as { message?: string })?.message || t(key)
}
function groupFor(id: number) { return groups.value.find(group => group.id === id) }
function isSelected(id: number) { return form.groups.some(group => group.group_id === id) }
function toggleGroup(id: number) {
  const index = form.groups.findIndex(group => group.group_id === id)
  if (index >= 0) form.groups.splice(index, 1)
  else form.groups.push({ group_id: id, models: [] })
}
function modelPlatforms(id: number) {
  const platform = groupFor(id)?.platform
  if (platform && platform !== 'composite') return [{ value: platform, label: platform }]
  return CONCRETE_PLATFORM_OPTIONS
}
function addModel(entry: displayAPI.PlazaDisplayGroup) {
  entry.models.push({ name: '', platform: modelPlatforms(entry.group_id)[0]?.value || 'openai' })
}

watch(() => props.show, show => {
  if (show) void load()
  else loadVersion++
}, { immediate: true })

async function load() {
  const version = ++loadVersion
  loading.value = true
  loaded.value = false
  error.value = ''
  try {
    const [config, allGroups] = await Promise.all([displayAPI.getConfig(), getAllIncludingInactive()])
    if (version !== loadVersion) return
    Object.assign(form, config)
    groups.value = allGroups
    saved.value = JSON.stringify(form)
    loaded.value = true
  } catch (err) {
    if (version === loadVersion) error.value = errorMessage(err, 'admin.settings.features.modelPlaza.displayLoadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

async function save() {
  if (!loaded.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const config = await displayAPI.updateConfig({
      mode: form.mode,
      groups: form.mode === 'automatic' ? [] : form.groups.map(group => ({
        group_id: group.group_id,
        models: group.models.map(model => ({ ...model, name: model.name.trim() })),
      })),
    })
    Object.assign(form, config)
    saved.value = JSON.stringify(form)
    app.showSuccess(t('admin.settings.features.modelPlaza.displaySaved'))
  } catch (err) {
    error.value = errorMessage(err, 'admin.settings.features.modelPlaza.displaySaveFailed')
  } finally {
    saving.value = false
  }
}
</script>
