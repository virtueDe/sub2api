<template>
  <BaseDialog :show="show" :title="t('admin.redeem.discount.title')" width="wide"
    :show-close-button="!busy" :close-on-escape="!busy" @close="!busy && emit('close')">
    <p v-if="loading" class="py-6 text-center text-gray-500">{{ t('common.loading') }}</p>
    <div v-else class="space-y-5">
      <div v-if="error" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">
        {{ error }}
        <button v-if="!loaded" data-testid="reload" class="ml-3 underline" @click="load">{{ t('common.refresh') }}</button>
      </div>
      <template v-if="loaded">
        <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('admin.redeem.discount.description') }}</p>
        <fieldset :disabled="busy" class="space-y-4">
          <label class="flex items-center gap-2 text-sm">
            <input v-model="form.enabled" type="checkbox" data-testid="enabled" class="rounded" />
            {{ t('admin.redeem.discount.enabled') }}
          </label>
          <div class="grid gap-4 sm:grid-cols-2">
            <label class="block">
              <span class="input-label">{{ t('admin.redeem.discount.targetGroup') }}</span>
              <select v-model.number="form.group_id" data-testid="target-group" class="input">
                <option :value="0">{{ t('admin.redeem.discount.selectGroup') }}</option>
                <option v-for="group in targetGroups" :key="group.id" :value="group.id">#{{ group.id }} {{ group.name }}</option>
              </select>
            </label>
            <label class="block">
              <span class="input-label">{{ t('admin.redeem.discount.multiplier') }}</span>
              <input v-model.number="form.rate_multiplier" data-testid="multiplier" type="number" min="0.0001" max="1" step="0.0001" class="input" />
            </label>
          </div>
          <p class="text-xs text-gray-500">{{ t('admin.redeem.discount.rateHint') }}</p>
          <div>
            <p class="input-label">{{ t('admin.redeem.discount.sourceGroups') }}</p>
            <p class="mb-2 text-xs text-gray-500">{{ t('admin.redeem.discount.sourceHint') }}</p>
            <div class="max-h-44 space-y-2 overflow-auto rounded-lg border border-gray-200 p-3 dark:border-dark-600">
              <label v-for="group in groups" :key="group.id" class="flex items-center gap-2 text-sm">
                <input v-model="form.source_group_ids" type="checkbox" :value="group.id" :data-source-group="group.id" class="rounded" />
                <span>#{{ group.id }} {{ group.name }}</span>
              </label>
            </div>
          </div>
        </fieldset>
        <p class="text-xs text-gray-500">{{ t('admin.redeem.discount.saveHint') }}</p>
        <div class="flex flex-wrap gap-2">
          <button data-testid="save" class="btn btn-primary" :disabled="busy || !dirty" @click="save">{{ t('common.save') }}</button>
          <button data-testid="preview" class="btn btn-secondary"
            :disabled="busy || dirty || !form.group_id || !form.source_group_ids.length" @click="loadPreview">
            {{ t('admin.redeem.discount.preview') }}
          </button>
        </div>
        <div v-if="result" class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
          <p data-testid="preview-target" class="text-sm">{{ t('admin.redeem.discount.targetGroup') }}: #{{ result.config.group_id }} {{ result.group_name }} · {{ result.config.rate_multiplier }}×</p>
          <p class="text-sm font-medium">{{ t('admin.redeem.discount.previewSummary', { count: result.user_count, changed: result.change_count }) }}</p>
          <p class="text-xs text-gray-500">{{ t('admin.redeem.discount.preserveHint') }}</p>
          <div class="max-h-72 overflow-auto rounded-lg border border-gray-200 dark:border-dark-600">
            <table class="w-full text-left text-sm">
              <thead class="bg-gray-50 dark:bg-dark-700">
                <tr><th class="p-2">ID</th><th class="p-2">{{ t('admin.redeem.discount.user') }}</th><th class="p-2">{{ t('admin.redeem.discount.currentRate') }}</th><th class="p-2">{{ t('admin.redeem.discount.newRate') }}</th></tr>
              </thead>
              <tbody>
                <tr v-for="user in result.users" :key="user.user_id" class="border-t border-gray-100 dark:border-dark-700">
                  <td class="p-2">{{ user.user_id }}</td><td class="p-2">{{ user.email }}</td>
                  <td class="p-2">{{ user.effective_rate }}×</td><td class="p-2">{{ user.new_rate }}×</td>
                </tr>
              </tbody>
            </table>
          </div>
          <button data-testid="apply" class="btn btn-primary" :disabled="busy || dirty || !result.change_count" @click="apply">
            {{ t('admin.redeem.discount.apply', { count: result.change_count }) }}
          </button>
        </div>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { getAllIncludingInactive } from '@/api/admin/groups'
import * as discountAPI from '@/api/admin/paidRedeemDiscount'
import type { AdminGroup } from '@/types'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (event: 'close'): void }>()
const { t } = useI18n()
const app = useAppStore()
const form = reactive<discountAPI.PaidRedeemDiscountConfig>({ enabled: false, group_id: 0, rate_multiplier: 0.01, source_group_ids: [] })
const saved = ref('')
const groups = ref<AdminGroup[]>([])
const loaded = ref(false)
const loading = ref(false)
const working = ref(false)
const error = ref('')
const result = ref<discountAPI.PaidRedeemDiscountPreview | null>(null)
const busy = computed(() => loading.value || working.value)
const dirty = computed(() => JSON.stringify(form) !== saved.value)
const targetGroups = computed(() => groups.value.filter(group => group.status === 'active' && group.subscription_type !== 'subscription' && group.allow_image_generation && !group.image_rate_independent))
let loadVersion = 0

function message(err: unknown, key: string) {
  const failure = err as { message?: string; response?: { data?: { detail?: string; message?: string } } }
  return failure?.message || failure?.response?.data?.detail || failure?.response?.data?.message || t(key)
}

watch(() => props.show, show => {
  if (show) void load()
  else loadVersion++
}, { immediate: true })
watch(form, () => { result.value = null }, { deep: true, flush: 'sync' })

async function load() {
  const version = ++loadVersion
  loading.value = true
  loaded.value = false
  error.value = ''
  result.value = null
  try {
    const [config, allGroups] = await Promise.all([discountAPI.getConfig(), getAllIncludingInactive()])
    if (version !== loadVersion) return
    Object.assign(form, config)
    saved.value = JSON.stringify(form)
    groups.value = allGroups
    loaded.value = true
  } catch (err) {
    if (version === loadVersion) error.value = message(err, 'admin.redeem.discount.loadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

async function save() {
  working.value = true
  error.value = ''
  result.value = null
  try {
    const updated = await discountAPI.updateConfig({ ...form, source_group_ids: [...form.source_group_ids] })
    Object.assign(form, updated)
    saved.value = JSON.stringify(form)
    app.showSuccess(t('admin.redeem.discount.saved'))
  } catch (err) {
    error.value = message(err, 'admin.redeem.discount.saveFailed')
  } finally {
    working.value = false
  }
}

async function loadPreview() {
  working.value = true
  error.value = ''
  result.value = null
  try {
    const preview = await discountAPI.preview()
    const previous = saved.value
    Object.assign(form, preview.config)
    saved.value = JSON.stringify(form)
    if (saved.value !== previous) {
      error.value = t('admin.redeem.discount.configChanged')
      return
    }
    result.value = preview
  } catch (err) {
    error.value = message(err, 'admin.redeem.discount.previewFailed')
  } finally {
    working.value = false
  }
}

async function apply() {
  if (!result.value || dirty.value) return
  working.value = true
  error.value = ''
  const token = result.value.preview_token
  result.value = null
  try {
    const applied = await discountAPI.backfill(token)
    app.showSuccess(t('admin.redeem.discount.applied', { count: applied.updated_count }))
  } catch (err) {
    error.value = message(err, 'admin.redeem.discount.applyFailed')
  } finally {
    working.value = false
  }
}
</script>
