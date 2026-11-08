<template>
  <AppLayout>
    <div class="space-y-5">
      <header class="flex flex-wrap items-start justify-between gap-4">
        <div><h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.profitAccounting.title') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('admin.profitAccounting.schedule') }}</p></div>
        <div class="flex gap-2"><button class="btn btn-secondary" @click="openRecalculate()">{{ t('admin.profitAccounting.recalculate') }}</button><button class="btn btn-primary" @click="openPrice()">{{ t('admin.profitAccounting.addPrice') }}</button></div>
      </header>
      <div class="flex gap-2">
        <button v-for="value in (['reports', 'prices'] as const)" :key="value" :aria-pressed="tab === value" class="btn" :class="tab === value ? 'btn-primary' : 'btn-secondary'" @click="tab = value">{{ t(`admin.profitAccounting.${value}`) }}</button>
      </div>
      <p v-if="error || priceError" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error || priceError }}</p>
      <section v-if="tab === 'reports'" class="card overflow-hidden">
        <form class="flex flex-wrap items-end gap-3 p-4" @submit.prevent="loadReports">
          <label class="text-sm">{{ t('admin.profitAccounting.startDate') }}<input v-model="startDate" class="input mt-1 block" type="date" required /></label>
          <label class="text-sm">{{ t('admin.profitAccounting.endDate') }}<input v-model="endDate" class="input mt-1 block" type="date" required /></label>
          <button class="btn btn-secondary" :disabled="loading">{{ t('admin.profitAccounting.refresh') }}</button>
        </form>
        <p v-if="!loading && !reports.length" class="px-4 pb-4 text-sm text-gray-500">{{ t('admin.profitAccounting.emptyReports') }}</p>
        <DataTable :columns="reportColumns" :data="reports" :loading="loading" row-key="id">
          <template #cell-revenue_usd="{ row }">{{ money(row.revenue_usd) }}</template>
          <template #cell-known_cost_usd="{ row }">{{ money(row.known_cost_usd) }}</template>
          <template #cell-profit_usd="{ row }"><span :class="row.profit_usd != null && Number(row.profit_usd) < 0 ? 'text-red-600' : ''">{{ money(row.profit_usd) }}</span></template>
          <template #cell-status="{ row }"><span :class="row.status === 'complete' ? 'text-emerald-600' : 'text-amber-600'">{{ t(`admin.profitAccounting.${row.status}`) }}</span><span v-if="row.missing_count" class="ml-1">({{ row.missing_count }})</span></template>
          <template #cell-actions="{ row }"><div class="flex gap-3"><button class="text-primary-600" @click="openReport(row)">{{ t('admin.profitAccounting.detail') }}</button><button class="text-primary-600" @click="openRecalculate(row.date)">{{ t('admin.profitAccounting.recalculate') }}</button></div></template>
        </DataTable>
      </section>
      <section v-else class="card overflow-hidden">
        <div class="flex items-start justify-between gap-3 p-4"><p class="text-sm text-gray-500">{{ t('admin.profitAccounting.priceHelp') }}</p><button class="btn btn-secondary" :disabled="pricesLoading" @click="loadPrices">{{ t('admin.profitAccounting.refresh') }}</button></div>
        <p v-if="!pricesLoading && !prices.length" class="px-4 pb-4 text-sm text-gray-500">{{ t('admin.profitAccounting.emptyPrices') }}</p>
        <DataTable :columns="priceColumns" :data="prices" :loading="pricesLoading" row-key="id">
          <template #cell-scope_name="{ row }">{{ t(`admin.profitAccounting.${row.scope_type}`) }} · {{ row.scope_name || `#${row.scope_id}` }}</template>
          <template #cell-unit_price_usd="{ row }">{{ money(row.unit_price_usd) }}</template>
          <template #cell-effective_at="{ row }">{{ beijingTime(row.effective_at) }}</template>
          <template #cell-actions="{ row }"><button class="text-primary-600" @click="openPrice(row)">{{ t('admin.profitAccounting.changePrice') }}</button></template>
        </DataTable>
        <Pagination v-if="priceTotal > 0" :total="priceTotal" :page="pricePage" :page-size="20" :show-page-size-selector="false" @update:page="pricePage = $event; loadPrices()" />
      </section>
    </div>

    <BaseDialog :show="showPrice" :title="t('admin.profitAccounting.addPrice')" @close="!saving && (showPrice = false)">
      <form id="procurement-price-form" class="space-y-4" @submit.prevent="savePrice">
        <p v-if="formError" role="alert" class="text-sm text-red-600">{{ formError }}</p>
        <label class="block text-sm">{{ t('admin.profitAccounting.scope') }}<select v-model="priceForm.scope_type" class="input mt-1" @change="changeScope"><option value="account">{{ t('admin.profitAccounting.account') }}</option><option value="channel">{{ t('admin.profitAccounting.channel') }}</option></select></label>
        <Select v-model="priceForm.scope_id" :options="scopeOptions" searchable remote :loading="scopeLoading" :placeholder="t('admin.profitAccounting.chooseScope')" :aria-label="t('admin.profitAccounting.scope')" @search="loadScopes" />
        <label class="block text-sm">{{ t('admin.profitAccounting.model') }}<input v-model.trim="priceForm.model" class="input mt-1" required maxlength="200" placeholder="gemini-nano-banana-2.1" /></label>
        <div class="grid grid-cols-2 gap-3"><label class="text-sm">{{ t('admin.profitAccounting.resolution') }}<select v-model="priceForm.resolution" class="input mt-1"><option v-for="size in ['1K', '2K', '4K']" :key="size">{{ size }}</option></select></label><label class="text-sm">{{ t('admin.profitAccounting.unitPrice') }}<input v-model="priceForm.unit_price_usd" class="input mt-1" required type="text" inputmode="decimal" pattern="[0-9]+(\.[0-9]{1,10})?" /></label></div>
        <p class="text-xs text-gray-500">{{ t('admin.profitAccounting.zeroPrice') }}</p>
        <label class="block text-sm">{{ t('admin.profitAccounting.effectiveAt') }}<input v-model="priceTime" class="input mt-1" type="datetime-local" /><span class="mt-1 block text-xs text-gray-500">{{ t('admin.profitAccounting.immediate') }}</span></label>
        <label class="block text-sm">{{ t('admin.profitAccounting.note') }}<textarea v-model.trim="priceForm.note" class="input mt-1" required maxlength="500" rows="2" /></label>
      </form>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="showPrice = false">{{ t('admin.profitAccounting.cancel') }}</button><button class="btn btn-primary" form="procurement-price-form" type="submit" :disabled="saving">{{ t('admin.profitAccounting.save') }}</button></template>
    </BaseDialog>

    <BaseDialog :show="showRecalculate" :title="t('admin.profitAccounting.recalculate')" @close="!saving && (showRecalculate = false)">
      <form id="profit-recalculate-form" class="space-y-4" @submit.prevent="recalculate">
        <p class="text-sm text-gray-500">{{ t('admin.profitAccounting.reasonHelp') }}</p><p v-if="formError" role="alert" class="text-sm text-red-600">{{ formError }}</p>
        <label class="block text-sm">{{ t('admin.profitAccounting.date') }}<input v-model="recalculateDate" class="input mt-1" type="date" :max="yesterday" required /></label>
        <label class="block text-sm">{{ t('admin.profitAccounting.reason') }}<textarea v-model.trim="reason" class="input mt-1" required maxlength="500" rows="3" /></label>
      </form>
      <template #footer><button class="btn btn-primary" form="profit-recalculate-form" type="submit" :disabled="saving">{{ t('admin.profitAccounting.recalculate') }}</button></template>
    </BaseDialog>

    <BaseDialog :show="showDetail" :title="`${t('admin.profitAccounting.detail')} · ${detail?.date || ''}`" width="extra-wide" @close="closeDetail">
      <p v-if="detailLoading" class="py-8 text-center">{{ t('common.loading') }}</p>
      <p v-if="detailError" role="alert" class="text-red-600">{{ detailError }}</p>
      <div v-if="detail" class="space-y-4"><p v-if="detail.source_incomplete" role="alert" class="text-amber-600">{{ t('admin.profitAccounting.sourceIncomplete') }}</p>
        <p class="text-sm text-gray-500">{{ t('admin.profitAccounting.detailHelp') }}</p>
        <p v-if="detail.status === 'incomplete'" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ t('admin.profitAccounting.pendingHelp') }}</p>
        <dl class="grid grid-cols-2 gap-3 sm:grid-cols-4"><div v-for="(value, label) in { revenue: money(detail.revenue_usd), cost: money(detail.known_cost_usd), profit: money(detail.profit_usd), images: detail.image_count }" :key="label"><dt class="text-xs text-gray-500">{{ t(`admin.profitAccounting.${label}`) }}</dt><dd class="mt-1 text-lg font-semibold">{{ value }}</dd></div></dl>
        <div class="flex flex-wrap items-center gap-2 text-sm"><span>{{ t('admin.profitAccounting.revisions') }}:</span><button v-for="version in versions" :key="version.id" class="btn btn-sm" :class="detail.id === version.id ? 'btn-primary' : 'btn-secondary'" @click="selectVersion(version.id)">v{{ version.revision }}</button></div>
        <p class="text-xs text-gray-500">{{ t('admin.profitAccounting.createdAt') }}: {{ beijingTime(detail.created_at) }} · {{ detail.reason === 'scheduled' ? t('admin.profitAccounting.scheduled') : detail.reason }}</p>
        <details v-for="line in detail.lines" :key="`${line.account_id}:${line.channel_id}:${line.model}`" class="rounded-lg border border-gray-200 p-3 dark:border-dark-700" open>
          <summary class="cursor-pointer break-words font-medium">{{ line.account_name || `#${line.account_id}` }} · {{ line.channel_name || t('admin.profitAccounting.noChannel') }} · {{ line.model }}</summary>
          <p class="mt-2 text-sm text-gray-500">{{ t('admin.profitAccounting.revenue') }} {{ money(line.revenue_usd) }} · {{ t('admin.profitAccounting.images') }} {{ line.image_count }} · {{ t('admin.profitAccounting.profit') }} {{ money(line.profit_usd) }}</p>
          <div v-for="(cost, index) in line.costs" :key="index" class="mt-2 flex flex-wrap items-center justify-between gap-2 border-t border-gray-100 pt-2 text-sm dark:border-dark-700"><span>{{ cost.resolution }} · {{ cost.image_count }} × {{ money(cost.unit_price_usd) }} = {{ money(cost.cost_usd) }}<span v-if="cost.price_id" class="ml-2 text-xs text-gray-500">#{{ cost.price_id }}</span></span><span v-if="cost.missing_reason" class="text-amber-600">{{ missingLabel(cost.missing_reason) }} <button v-if="cost.missing_reason === 'missing_price'" class="ml-2 text-primary-600" @click="fillPrice(line, cost)">{{ t('admin.profitAccounting.fillPrice') }}</button></span></div>
        </details>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { list as listAccounts } from '@/api/admin/accounts'
import { list as listChannels } from '@/api/admin/channels'
import { profitAccountingAPI as api, type ProcurementPrice, type CreateProcurementPrice, type ProfitReport, type ProfitLine, type ProfitCost } from '@/api/admin/profitAccounting'

const { t } = useI18n()
const app = useAppStore()
const beijingDate = (d: Date) => new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai' }).format(d)
const yesterday = ref(beijingDate(new Date(Date.now() - 86400000)))
const startDate = ref(beijingDate(new Date(Date.now() - 30 * 86400000)))
const endDate = ref(yesterday.value)
const tab = ref<'reports' | 'prices'>('reports')
const reports = ref<ProfitReport[]>([]), prices = ref<ProcurementPrice[]>([])
const loading = ref(false), pricesLoading = ref(false), error = ref(''), priceError = ref(''), formError = ref(''), saving = ref(false)
const pricePage = ref(1), priceTotal = ref(0)
const showPrice = ref(false), showRecalculate = ref(false), showDetail = ref(false)
const detail = ref<ProfitReport | null>(null), versions = ref<ProfitReport[]>([]), detailLoading = ref(false), detailError = ref('')
const reason = ref(''), recalculateDate = ref(yesterday.value), priceTime = ref('')
const priceForm = reactive<CreateProcurementPrice>({ scope_type: 'account', scope_id: 0, model: '', resolution: '1K', unit_price_usd: '', note: '' })
const scopeOptions = ref<{ value: number; label: string }[]>([]), scopeLoading = ref(false)
let reportSequence = 0, priceSequence = 0, detailSequence = 0, scopeSequence = 0
let scopeAbort: AbortController | undefined
const message = (e: unknown) => extractApiErrorMessage(e, t('admin.profitAccounting.failed'))
const money = (value: string | null | undefined) => value == null ? '—' : new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 10 }).format(Number(value))
const beijingTime = (value: string) => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value))
const columns = (keys: string[]) => keys.map(key => ({ key, label: t(`admin.profitAccounting.${({ revenue_usd: 'revenue', known_cost_usd: 'cost', profit_usd: 'profit', image_count: 'images', scope_name: 'scope', unit_price_usd: 'unitPrice', effective_at: 'effectiveAt' } as Record<string, string>)[key] || key}`) }))
const reportColumns = computed(() => columns(['date', 'image_count', 'revenue_usd', 'known_cost_usd', 'profit_usd', 'status', 'actions']))
const priceColumns = computed(() => columns(['scope_name', 'model', 'resolution', 'unit_price_usd', 'effective_at', 'note', 'actions']))
function missingLabel(value: string) {
  const key = ({ missing_price: 'missingPrice', unknown_resolution: 'unknownResolution', unknown_image_count: 'unknownCount', inconsistent_image_count: 'inconsistentCount', unconfirmed_charge: 'unconfirmedCharge' } as Record<string, string>)[value] || 'unknown'
  return t(`admin.profitAccounting.${key}`)
}
async function loadReports() {
  const seq = ++reportSequence; loading.value = true; error.value = ''
  try { const result = await api.reports(startDate.value, endDate.value); if (seq === reportSequence) reports.value = result }
  catch (e) { if (seq === reportSequence) error.value = message(e) }
  finally { if (seq === reportSequence) loading.value = false }
}
async function loadPrices() {
  const seq = ++priceSequence; pricesLoading.value = true; priceError.value = ''
  try { const result = await api.prices(pricePage.value); if (seq === priceSequence) { prices.value = result.items; priceTotal.value = result.total } }
  catch (e) { if (seq === priceSequence) priceError.value = message(e) }
  finally { if (seq === priceSequence) pricesLoading.value = false }
}
async function loadScopes(search = '') {
  const seq = ++scopeSequence; scopeAbort?.abort(); scopeAbort = new AbortController(); scopeLoading.value = true
  try {
    const result = priceForm.scope_type === 'account'
      ? await listAccounts(1, 20, { search, lite: 'true' }, { signal: scopeAbort.signal })
      : await listChannels(1, 20, { search }, { signal: scopeAbort.signal })
    if (seq !== scopeSequence) return
    const selected = scopeOptions.value.find(item => item.value === priceForm.scope_id)
    scopeOptions.value = result.items.map(item => ({ value: item.id, label: `${item.name} (#${item.id})` }))
    if (selected && !scopeOptions.value.some(item => item.value === selected.value)) scopeOptions.value.unshift(selected)
  } catch (e) { if (seq === scopeSequence) formError.value = message(e) }
  finally { if (seq === scopeSequence) scopeLoading.value = false }
}
function changeScope() { priceForm.scope_id = 0; scopeOptions.value = []; void loadScopes() }
function openPrice(price?: ProcurementPrice) {
  formError.value = ''; priceTime.value = ''
  Object.assign(priceForm, { scope_type: price?.scope_type || 'account', scope_id: price?.scope_id || 0, model: price?.model || '', resolution: price?.resolution || '1K', unit_price_usd: price?.unit_price_usd || '', note: '' })
  scopeOptions.value = price ? [{ value: price.scope_id, label: `${price.scope_name || price.scope_id} (#${price.scope_id})` }] : []
  showPrice.value = true; void loadScopes()
}
function fillPrice(line: ProfitLine, cost: ProfitCost) {
  const date = detail.value?.date; closeDetail(); openPrice()
  Object.assign(priceForm, { scope_id: line.account_id, model: line.model, resolution: cost.resolution })
  scopeOptions.value = [{ value: line.account_id, label: `${line.account_name || line.account_id} (#${line.account_id})` }]
  priceTime.value = date ? `${date}T00:00` : ''
}
async function savePrice() {
  if (saving.value) return
  if (!priceForm.scope_id || !priceForm.model || !priceForm.note || priceForm.unit_price_usd === '' || !Number.isFinite(Number(priceForm.unit_price_usd)) || Number(priceForm.unit_price_usd) < 0) { formError.value = t('admin.profitAccounting.required'); return }
  saving.value = true; formError.value = ''
  try { await api.createPrice({ ...priceForm, effective_at: priceTime.value ? new Date(`${priceTime.value}:00+08:00`).toISOString() : undefined }); showPrice.value = false; app.showSuccess(t('admin.profitAccounting.saved')); pricePage.value = 1; await loadPrices() }
  catch (e) { formError.value = message(e) }
  finally { saving.value = false }
}
function openRecalculate(date?: string) { yesterday.value = beijingDate(new Date(Date.now() - 86400000)); recalculateDate.value = date || yesterday.value; reason.value = ''; formError.value = ''; showRecalculate.value = true }
async function recalculate() {
  if (saving.value || !reason.value.trim()) return
  saving.value = true; formError.value = ''
  try { const report = await api.recalculate(recalculateDate.value, reason.value); showRecalculate.value = false; app.showSuccess(t('admin.profitAccounting.calculated')); await loadReports(); await openReport(report) }
  catch (e) { formError.value = message(e) }
  finally { saving.value = false }
}
async function openReport(report: ProfitReport) {
  const seq = ++detailSequence; showDetail.value = true; detail.value = null; versions.value = []; detailError.value = ''; detailLoading.value = true
  try { const [value, history] = await Promise.all([api.report(report.id), api.reports(report.date, report.date, true)]); if (seq === detailSequence) { detail.value = value; versions.value = history } }
  catch (e) { if (seq === detailSequence) detailError.value = message(e) }
  finally { if (seq === detailSequence) detailLoading.value = false }
}
async function selectVersion(id: number) {
  const seq = ++detailSequence; detailLoading.value = true; detailError.value = ''
  try { const value = await api.report(id); if (seq === detailSequence) detail.value = value }
  catch (e) { if (seq === detailSequence) detailError.value = message(e) }
  finally { if (seq === detailSequence) detailLoading.value = false }
}
function closeDetail() { showDetail.value = false; detailSequence++ }
onMounted(() => { void loadReports(); void loadPrices() })
onUnmounted(() => { reportSequence++; priceSequence++; detailSequence++; scopeSequence++; scopeAbort?.abort() })
</script>
