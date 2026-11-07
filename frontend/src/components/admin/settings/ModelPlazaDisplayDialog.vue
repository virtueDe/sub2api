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
          <select v-model="form.mode" data-testid="mode" class="input" @change="onModeChange">
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
            <p v-if="modelPreviews[entry.group_id]?.loading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
            <div v-else-if="modelPreviews[entry.group_id]?.error" role="alert" :data-model-error="entry.group_id" class="text-sm text-red-600">
              {{ modelPreviews[entry.group_id]?.error }}
              <button type="button" :data-retry-models="entry.group_id" class="ml-2 underline" @click="loadGroupModels(entry.group_id)">{{ t('common.refresh') }}</button>
            </div>
            <div v-else-if="modelPreviews[entry.group_id]?.models.length" :data-models-for="entry.group_id" class="flex flex-wrap gap-2">
              <span v-for="model in modelPreviews[entry.group_id]?.models" :key="`${model.platform}:${model.name}`" :title="model.platform" class="rounded bg-gray-100 px-2 py-1 font-mono text-xs dark:bg-dark-700">{{ model.name }}</span>
            </div>
            <p v-else class="text-sm text-gray-500">{{ t('admin.settings.features.modelPlaza.noGroupModels') }}</p>
          </div>
        </template>
        <p class="text-xs text-gray-500">{{ t('admin.settings.features.modelPlaza.separateSaveHint') }}</p>
      </fieldset>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" data-testid="save" class="btn btn-primary" :disabled="loading || saving || !loaded || !dirty || modelLoadBlocked" @click="save">{{ t('common.save') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { getAllIncludingInactive } from '@/api/admin/groups'
import * as displayAPI from '@/api/admin/modelPlazaDisplay'
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
const modelPreviews = ref<Record<number, { models: displayAPI.PlazaDisplayModel[]; loading: boolean; error: string }>>({})
const modelRequests = new Map<number, number>()
let modelRequestVersion = 0
const modelLoadBlocked = computed(() => form.mode === 'manual' && form.groups.some(group => {
  const preview = modelPreviews.value[group.group_id]
  return preview?.loading || Boolean(preview?.error)
}))
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
  if (index >= 0) {
    form.groups.splice(index, 1)
    modelRequests.delete(id)
    delete modelPreviews.value[id]
  } else {
    form.groups.push({ group_id: id, models: [] })
    void loadGroupModels(id)
  }
}
function onModeChange() {
  if (form.mode === 'automatic') {
    form.groups = []
    clearModelPreviews()
  }
}
function clearModelPreviews() {
  modelRequests.clear()
  modelPreviews.value = {}
}
async function loadGroupModels(id: number) {
  if (groupFor(id)?.status !== 'active' || !isSelected(id)) return
  const request = ++modelRequestVersion
  modelRequests.set(id, request)
  modelPreviews.value[id] = { models: [], loading: true, error: '' }
  try {
    const models = await displayAPI.getGroupModels(id)
    if (modelRequests.get(id) !== request) return
    modelPreviews.value[id] = { models, loading: false, error: '' }
  } catch (err) {
    if (modelRequests.get(id) !== request) return
    modelPreviews.value[id] = { models: [], loading: false, error: errorMessage(err, 'admin.settings.features.modelPlaza.modelsLoadFailed') }
  }
}

watch(() => props.show, show => {
  if (show) void load()
  else {
    loadVersion++
    clearModelPreviews()
  }
}, { immediate: true })
onBeforeUnmount(() => {
  loadVersion++
  clearModelPreviews()
})

async function load() {
  const version = ++loadVersion
  clearModelPreviews()
  loading.value = true
  loaded.value = false
  error.value = ''
  try {
    const [config, allGroups] = await Promise.all([displayAPI.getConfig(), getAllIncludingInactive()])
    if (version !== loadVersion) return
    Object.assign(form, {
      mode: config.mode,
      groups: config.groups.map(group => ({ group_id: group.group_id, models: [] })),
    })
    groups.value = allGroups
    saved.value = JSON.stringify(form)
    loaded.value = true
    if (form.mode === 'manual') {
      for (const group of form.groups) void loadGroupModels(group.group_id)
    }
  } catch (err) {
    if (version === loadVersion) error.value = errorMessage(err, 'admin.settings.features.modelPlaza.displayLoadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

async function save() {
  if (!loaded.value || saving.value || modelLoadBlocked.value) return
  saving.value = true
  error.value = ''
  try {
    const config = await displayAPI.updateConfig({
      mode: form.mode,
      groups: form.mode === 'automatic' ? [] : form.groups.map(group => ({
        group_id: group.group_id,
        models: [],
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
