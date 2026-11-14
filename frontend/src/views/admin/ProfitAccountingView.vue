<template>
  <AppLayout>
    <div class="space-y-5">
      <header class="flex flex-wrap items-start justify-between gap-4">
        <div><h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.profitAccounting.title') }}</h1><p class="mt-2 text-sm text-gray-500">{{ t('admin.profitAccounting.schedule') }}</p></div>
        <div class="flex gap-2"><button class="btn btn-secondary" @click="openRecalculate()">{{ t('admin.profitAccounting.chooseRecalculateDate') }}</button><button class="btn btn-primary" @click="openPrice()">{{ t('admin.profitAccounting.addPrice') }}</button></div>
      </header>
      <ProfitOverview :reports="overviewReports" :end-date="overviewEnd" :loading="overviewLoading" :error="overviewError" @retry="loadOverview" />
      <div class="flex gap-2">
        <button v-for="value in (['reports', 'prices'] as const)" :key="value" :aria-pressed="tab === value" class="btn" :class="tab === value ? 'btn-primary' : 'btn-secondary'" @click="tab = value">{{ t(`admin.profitAccounting.${value}`) }}</button>
      </div>
      <p v-if="error || priceError" role="alert" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">{{ error || priceError }}</p>
      <div v-if="savedPriceNotice" role="status" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">
        {{ savedPriceNotice }} <button class="ml-2 underline" @click="openRecalculate(savedPriceDate || undefined)">{{ t('admin.profitAccounting.chooseRecalculateDate') }}</button>
      </div>
      <section v-if="tab === 'reports'" class="card overflow-hidden">
        <h2 class="px-4 pt-4 text-lg font-semibold">{{ t('admin.profitAccounting.statisticsTable') }}</h2>
        <form class="flex flex-wrap items-end gap-3 p-4" @submit.prevent="loadReports">
          <label class="text-sm">{{ t('admin.profitAccounting.startDate') }}<input v-model="startDate" class="input mt-1 block" type="date" required /></label>
          <label class="text-sm">{{ t('admin.profitAccounting.endDate') }}<input v-model="endDate" class="input mt-1 block" type="date" required /></label>
          <button class="btn btn-secondary" :disabled="loading">{{ t('admin.profitAccounting.refresh') }}</button>
        </form>
        <p class="px-4 pb-3 text-sm text-gray-500">{{ t('admin.profitAccounting.queryHelp') }}</p>
        <p class="px-4 pb-3 text-sm text-gray-500">{{ t('admin.profitAccounting.lossHelp') }} {{ t('admin.profitAccounting.adminExclusionHelp') }}</p>
        <p v-if="!loading && !reports.length" class="px-4 pb-4 text-sm text-gray-500">{{ t('admin.profitAccounting.emptyReports') }}</p>
        <div class="profit-table-region">
        <DataTable tabindex="0" :aria-label="t('admin.profitAccounting.reports')" :columns="reportColumns" :data="reports" :loading="loading" row-key="id">
          <template #cell-revenue_usd="{ row }">{{ money(row.revenue_usd) }}</template>
          <template #cell-known_cost_usd="{ row }">{{ money(row.known_cost_usd) }}</template>
          <template #cell-abnormal_loss_usd="{ row }"><span class="text-amber-700 dark:text-amber-400">{{ money(row.abnormal_loss_usd) }}</span><span v-if="row.abnormal_loss_requests" class="ml-1 text-xs">({{ row.abnormal_loss_requests }})</span></template>
          <template #cell-profit_usd="{ row }"><span :class="row.profit_usd != null && Number(row.profit_usd) < 0 ? 'text-red-600' : ''">{{ money(row.profit_usd) }}</span></template>
          <template #cell-status="{ row }"><span :class="row.status === 'complete' ? 'text-emerald-600' : 'text-amber-600'">{{ t(`admin.profitAccounting.${row.status}`) }}</span><span v-if="row.missing_count" class="ml-1">({{ row.missing_count }})</span><p v-if="row.status === 'incomplete'" class="mt-1 max-w-xs text-xs text-amber-600">{{ reportMissingReasons(row) }} <button class="underline" @click="openReport(row)">{{ t('admin.profitAccounting.viewMissing') }}</button></p></template>
          <template #cell-actions="{ row }"><div class="flex gap-3"><button class="text-primary-600" @click="openReport(row)">{{ t('admin.profitAccounting.detail') }}</button><button class="text-primary-600" @click="openRecalculate(row.date)">{{ t('admin.profitAccounting.recalculateDay') }}</button></div></template>
        </DataTable>
        </div>
      </section>
      <section v-else class="card overflow-hidden">
        <div class="flex items-start justify-between gap-3 p-4"><p class="text-sm text-gray-500">{{ t('admin.profitAccounting.priceHelp') }}</p><button class="btn btn-secondary" :disabled="pricesLoading" @click="loadPrices">{{ t('admin.profitAccounting.refresh') }}</button></div>
        <p v-if="!pricesLoading && !prices.length" class="px-4 pb-4 text-sm text-gray-500">{{ t('admin.profitAccounting.emptyPrices') }}</p>
        <div class="profit-table-region">
        <DataTable tabindex="0" :aria-label="t('admin.profitAccounting.prices')" :columns="priceColumns" :data="prices" :loading="pricesLoading" row-key="id">
          <template #cell-scope_name="{ row }">{{ t(`admin.profitAccounting.${row.scope_type}`) }} · {{ row.scope_name || `#${row.scope_id}` }}</template>
          <template #cell-unit_price_usd="{ row }">{{ money(row.unit_price_usd) }}</template>
          <template #cell-effective_at="{ row }">{{ beijingTime(row.effective_at) }}</template>
          <template #cell-actions="{ row }"><button class="text-primary-600" @click="openPrice(row)">{{ t('admin.profitAccounting.changePrice') }}</button></template>
        </DataTable>
        </div>
        <Pagination v-if="priceTotal > 0" :total="priceTotal" :page="pricePage" :page-size="20" :show-page-size-selector="false" @update:page="pricePage = $event; loadPrices()" />
      </section>
    </div>

    <BaseDialog :show="showPrice" :title="t('admin.profitAccounting.addPrice')" width="extra-wide" @close="!saving && (showPrice = false)">
      <form id="procurement-price-form" class="space-y-4" @submit.prevent="savePrice">
        <p v-if="formError" role="alert" class="text-sm text-red-600">{{ formError }}</p>
        <fieldset :disabled="saving || pendingPrices !== null" class="space-y-4">
          <label class="block text-sm">{{ t('admin.profitAccounting.account') }}</label>
          <Select v-model="priceForm.scope_id" :disabled="saving || pendingPrices !== null" :options="scopeOptions" searchable remote :loading="scopeLoading" :placeholder="t('admin.profitAccounting.chooseScope')" :aria-label="t('admin.profitAccounting.account')" @search="loadScopes" @update:model-value="loadModels()" />
          <p class="text-sm text-gray-500">{{ t('admin.profitAccounting.batchHelp') }}</p>
          <p v-if="modelsLoading" role="status">{{ t('common.loading') }}</p>
          <p v-else-if="priceForm.scope_id && !modelRows.length && !modelsError" class="text-sm text-gray-500">{{ t('admin.profitAccounting.noModels') }}</p>
          <p v-if="modelsError" role="alert" class="text-sm text-red-600">{{ modelsError }} <button type="button" class="text-primary-600" @click="loadModels()">{{ t('admin.profitAccounting.refresh') }}</button></p>
          <div class="space-y-3">
            <div v-for="row in modelRows" :key="row.model" :data-model="row.model" class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="break-all font-medium">{{ row.model }}</span>
                <label class="flex items-center gap-2 text-sm"><input v-model="row.separate" type="checkbox" @change="splitPrice(row)" />{{ t('admin.profitAccounting.separatePrices') }}</label>
              </div>
              <div v-if="row.separate" class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3">
                <label v-for="size in resolutions" :key="size" class="text-sm">{{ size }} · {{ t('admin.profitAccounting.unitPrice') }}<input v-model.trim="row.prices[size]" :aria-label="`${row.model} ${size}`" class="input mt-1" type="text" inputmode="decimal" pattern="[0-9]+(\.[0-9]{1,10})?" /></label>
              </div>
              <label v-else class="mt-3 block text-sm">{{ t('admin.profitAccounting.samePrice') }} · {{ t('admin.profitAccounting.unitPrice') }}<input v-model.trim="row.price" :aria-label="`${row.model} 1K / 2K / 4K`" class="input mt-1" type="text" inputmode="decimal" pattern="[0-9]+(\.[0-9]{1,10})?" /></label>
            </div>
          </div>
          <p class="text-xs text-gray-500">{{ t('admin.profitAccounting.zeroPrice') }}</p>
          <label class="block text-sm">{{ t('admin.profitAccounting.effectiveAt') }}<input v-model="priceTime" class="input mt-1" type="datetime-local" /><span class="mt-1 block text-xs text-gray-500">{{ t('admin.profitAccounting.effectiveHelp') }}</span></label>
          <label class="block text-sm">{{ t('admin.profitAccounting.note') }}<textarea v-model.trim="priceForm.note" class="input mt-1" required maxlength="500" rows="2" /></label>
        </fieldset>
        <p v-if="pendingPrices !== null" role="status" class="text-sm text-gray-500">{{ t('admin.profitAccounting.batchProgress', { saved: savedCount, remaining: pendingPrices.length }) }}</p>
      </form>
      <template #footer><button class="btn btn-secondary" :disabled="saving" @click="showPrice = false">{{ t('admin.profitAccounting.cancel') }}</button><button class="btn btn-primary" form="procurement-price-form" type="submit" :disabled="saving || modelsLoading">{{ t(pendingPrices !== null ? 'admin.profitAccounting.retryRemaining' : 'admin.profitAccounting.saveAll') }}</button></template>
    </BaseDialog>

    <BaseDialog :show="showRecalculate" :title="t('admin.profitAccounting.recalculate')" @close="!saving && (showRecalculate = false)">
      <form id="profit-recalculate-form" class="space-y-4" @submit.prevent="recalculate">
        <p class="text-sm text-gray-500">{{ t('admin.profitAccounting.singleDayHelp') }}</p>
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
        <dl class="grid grid-cols-2 gap-3 sm:grid-cols-5"><div v-for="(value, label) in { revenue: money(detail.revenue_usd), cost: money(detail.known_cost_usd), abnormalLoss: money(detail.abnormal_loss_usd), profit: money(detail.profit_usd), images: detail.image_count }" :key="label"><dt class="text-xs text-gray-500">{{ t(`admin.profitAccounting.${label}`) }}</dt><dd class="mt-1 text-lg font-semibold">{{ value }}</dd></div></dl>
        <p class="text-sm text-gray-500">{{ t('admin.profitAccounting.lossHelp') }}<span v-if="detail.excluded_admin_requests"> {{ t('admin.profitAccounting.excludedAdmins', { count: detail.excluded_admin_requests }) }}</span><span v-if="detail.count_corrections"> {{ t('admin.profitAccounting.correctedCounts', { count: detail.count_corrections }) }}</span></p>
        <div class="flex flex-wrap items-center gap-2 text-sm"><span>{{ t('admin.profitAccounting.revisions') }}:</span><button v-for="version in versions" :key="version.id" class="btn btn-sm" :class="detail.id === version.id ? 'btn-primary' : 'btn-secondary'" @click="selectVersion(version.id)">v{{ version.revision }}</button></div>
        <p class="text-xs text-gray-500">{{ t('admin.profitAccounting.createdAt') }}: {{ beijingTime(detail.created_at) }} · {{ detail.reason === 'scheduled' ? t('admin.profitAccounting.scheduled') : detail.reason }}</p>
        <details v-for="line in detail.lines" :key="`${line.account_id}:${line.channel_id}:${line.model}`" class="rounded-lg border border-gray-200 p-3 dark:border-dark-700" open>
          <summary class="cursor-pointer break-words font-medium">{{ line.account_name || `#${line.account_id}` }} · {{ line.channel_name || t('admin.profitAccounting.noChannel') }} · {{ line.model }}</summary>
          <p class="mt-2 text-sm text-gray-500">{{ t('admin.profitAccounting.revenue') }} {{ money(line.revenue_usd) }} · {{ t('admin.profitAccounting.images') }} {{ line.image_count }} · {{ t('admin.profitAccounting.abnormalLoss') }} {{ money(line.abnormal_loss_usd) }} · {{ t('admin.profitAccounting.profit') }} {{ money(line.profit_usd) }}</p>
          <div v-for="(cost, index) in line.costs" :key="index" class="mt-2 flex flex-wrap items-center justify-between gap-2 border-t border-gray-100 pt-2 text-sm dark:border-dark-700"><span>{{ cost.resolution === 'all_tiers' ? t('admin.profitAccounting.allTiers') : cost.resolution }} · {{ cost.image_count }} × {{ money(cost.unit_price_usd) }} = {{ money(cost.cost_usd) }}<span v-if="cost.price_id" class="ml-2 text-xs text-gray-500">#{{ cost.price_id }}</span><span v-if="cost.price_ids?.length" class="ml-2 text-xs text-gray-500">#{{ cost.price_ids.join(', #') }}</span><span v-if="cost.abnormal_loss" class="ml-2 text-amber-700 dark:text-amber-400">{{ t('admin.profitAccounting.abnormalLoss') }}</span></span><span v-if="cost.missing_reason" class="text-amber-600">{{ missingLabel(cost.missing_reason) }} <button v-if="cost.missing_reason === 'missing_price'" class="ml-2 text-primary-600" @click="fillPrice(line, cost)">{{ t('admin.profitAccounting.fillPrice') }}</button></span></div>
        </details>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import ProfitOverview from './components/ProfitOverview.vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { list as listAccounts, getAvailableModels, getById as getAccount } from '@/api/admin/accounts'
import { profitAccountingAPI as api, type ProcurementPrice, type CreateProcurementPrice, type ProfitReport, type ProfitLine, type ProfitCost } from '@/api/admin/profitAccounting'

const { t } = useI18n()
const app = useAppStore()
const beijingDate = (d: Date) => new Intl.DateTimeFormat('en-CA', { timeZone: 'Asia/Shanghai' }).format(d)
const yesterday = ref(beijingDate(new Date(Date.now() - 86400000)))
const startDate = ref(beijingDate(new Date(Date.now() - 30 * 86400000)))
const endDate = ref(yesterday.value)
const overviewReports = ref<ProfitReport[]>([]), overviewEnd = ref(yesterday.value), overviewLoading = ref(false), overviewError = ref('')
let overviewSequence = 0
const tab = ref<'reports' | 'prices'>('reports')
const reports = ref<ProfitReport[]>([]), prices = ref<ProcurementPrice[]>([])
const loading = ref(false), pricesLoading = ref(false), error = ref(''), priceError = ref(''), formError = ref(''), saving = ref(false)
const pricePage = ref(1), priceTotal = ref(0)
const showPrice = ref(false), showRecalculate = ref(false), showDetail = ref(false)
const detail = ref<ProfitReport | null>(null), versions = ref<ProfitReport[]>([]), detailLoading = ref(false), detailError = ref('')
const savedPriceNotice = ref(''), savedPriceDate = ref('')
const reason = ref(''), recalculateDate = ref(yesterday.value), priceTime = ref('')
const priceForm = reactive({ scope_id: 0, note: '' })
const resolutions = ['1K', '2K', '4K'] as const
interface ModelPriceRow { model: string; separate: boolean; price: string; prices: Record<typeof resolutions[number], string> }
const modelRows = ref<ModelPriceRow[]>([]), modelsLoading = ref(false), modelsError = ref('')
const pendingPrices = ref<CreateProcurementPrice[] | null>(null), savedCount = ref(0)
function splitPrice(row: ModelPriceRow) {
  if (row.separate && resolutions.every(size => row.prices[size] === '')) {
    for (const size of resolutions) row.prices[size] = row.price
  }
}
let modelSequence = 0
const scopeOptions = ref<{ value: number; label: string }[]>([]), scopeLoading = ref(false)
let reportSequence = 0, priceSequence = 0, detailSequence = 0, scopeSequence = 0
let scopeAbort: AbortController | undefined
const message = (e: unknown) => extractApiErrorMessage(e, t('admin.profitAccounting.failed'))
const money = (value: string | null | undefined) => value == null ? '—' : new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 10 }).format(Number(value))
const beijingTime = (value: string) => new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value))
const columns = (keys: string[]) => keys.map(key => ({ key, label: t(`admin.profitAccounting.${({ revenue_usd: 'revenue', known_cost_usd: 'cost', abnormal_loss_usd: 'abnormalLoss', profit_usd: 'profit', image_count: 'images', scope_name: 'scope', unit_price_usd: 'unitPrice', effective_at: 'effectiveAt' } as Record<string, string>)[key] || key}`) }))
const reportColumns = computed(() => columns(['date', 'image_count', 'revenue_usd', 'known_cost_usd', 'abnormal_loss_usd', 'profit_usd', 'status', 'actions']))
const priceColumns = computed(() => columns(['scope_name', 'model', 'resolution', 'unit_price_usd', 'effective_at', 'note', 'actions']))
function missingLabel(value: string) {
  const key = ({ missing_price: 'missingPrice', unknown_resolution: 'unknownResolution', unknown_image_count: 'unknownCount', inconsistent_image_count: 'inconsistentCount', unconfirmed_charge: 'unconfirmedCharge', loss_confirmation_conflict: 'lossConflict' } as Record<string, string>)[value] || 'unknown'
  return t(`admin.profitAccounting.${key}`)
}
function reportMissingReasons(report: ProfitReport) {
  const reasons = new Set<string>()
  if (report.source_incomplete) reasons.add(t('admin.profitAccounting.sourceIncomplete'))
  for (const line of report.lines || []) {
    for (const cost of line.costs) if (cost.missing_reason) reasons.add(missingLabel(cost.missing_reason))
  }
  return [...reasons].join(' · ') || t('admin.profitAccounting.inspectMissing')
}
async function loadOverview() {
  const seq = ++overviewSequence
  overviewLoading.value = true; overviewError.value = ''
  const end = beijingDate(new Date(Date.now() - 86400000))
  const start = beijingDate(new Date(new Date(`${end}T00:00:00+08:00`).getTime() - 14 * 86400000))
  try {
    const result = await api.reports(start, end)
    if (seq === overviewSequence) { overviewReports.value = result; overviewEnd.value = end }
  } catch (e) { if (seq === overviewSequence) overviewError.value = message(e) }
  finally { if (seq === overviewSequence) overviewLoading.value = false }
}
async function loadReports() {
  void loadOverview()
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
    const result = await listAccounts(1, 20, { search, lite: 'true' }, { signal: scopeAbort.signal })
    if (seq !== scopeSequence) return
    const selected = scopeOptions.value.find(item => item.value === priceForm.scope_id)
    scopeOptions.value = result.items.map(item => ({ value: item.id, label: `${item.name} (#${item.id})` }))
    if (selected && !scopeOptions.value.some(item => item.value === selected.value)) scopeOptions.value.unshift(selected)
  } catch (e) { if (seq === scopeSequence) formError.value = message(e) }
  finally { if (seq === scopeSequence) scopeLoading.value = false }
}
async function loadModels(initial?: { model: string; resolution: string; unit_price_usd: string }) {
  const seq = ++modelSequence
  const accountID = priceForm.scope_id
  modelRows.value = []; modelsError.value = ''; modelsLoading.value = false
  if (!accountID) return
  modelsLoading.value = true
  try {
    const [models, account] = await Promise.all([getAvailableModels(accountID), getAccount(accountID)])
    if (seq !== modelSequence) return
    const mapping = account.credentials?.model_mapping as Record<string, unknown> | undefined
    const ids = models.map(model => typeof mapping?.[model.id] === 'string' && mapping[model.id] ? String(mapping[model.id]) : model.id)
    if (initial?.model) ids.push(initial.model)
    modelRows.value = [...new Set(ids)].filter(id => id && !id.includes('*')).map(model => {
      const row: ModelPriceRow = { model, separate: false, price: '', prices: { '1K': '', '2K': '', '4K': '' } }
      if (initial?.model === model && resolutions.includes(initial.resolution as typeof resolutions[number])) {
        row.separate = true
        row.prices[initial.resolution as typeof resolutions[number]] = initial.unit_price_usd
      }
      return row
    })
  } catch (e) { if (seq === modelSequence) modelsError.value = message(e) }
  finally { if (seq === modelSequence) modelsLoading.value = false }
}
function openPrice(price?: ProcurementPrice) {
  formError.value = ''; priceTime.value = ''; pendingPrices.value = null; savedCount.value = 0
  Object.assign(priceForm, { scope_id: price?.scope_type === 'account' ? price.scope_id : 0, note: '' })
  scopeOptions.value = price?.scope_type === 'account' ? [{ value: price.scope_id, label: `${price.scope_name || price.scope_id} (#${price.scope_id})` }] : []
  showPrice.value = true; void loadScopes(); void loadModels(price)
}
function fillPrice(line: ProfitLine, cost: ProfitCost) {
  const date = detail.value?.date; closeDetail(); openPrice()
  priceForm.scope_id = line.account_id
  scopeOptions.value = [{ value: line.account_id, label: `${line.account_name || line.account_id} (#${line.account_id})` }]
  priceTime.value = date ? `${date}T00:00` : ''; void loadModels({ model: line.model, resolution: cost.resolution, unit_price_usd: '' })
}
async function savePrice() {
  if (saving.value || modelsLoading.value) return
  if (pendingPrices.value === null) {
    const jobs: CreateProcurementPrice[] = []
    const effectiveAt = priceTime.value ? new Date(`${priceTime.value}:00+08:00`) : new Date()
    if (!priceForm.scope_id || !priceForm.note.trim() || !Number.isFinite(effectiveAt.getTime())) { formError.value = t('admin.profitAccounting.required'); return }
    for (const row of modelRows.value) {
      for (const resolution of resolutions) {
        const price = row.separate ? row.prices[resolution] : row.price
        if (price === '') continue
        if (!/^\d+(\.\d{1,10})?$/.test(price) || !Number.isFinite(Number(price))) { formError.value = `${row.model} ${resolution}: ${t('admin.profitAccounting.required')}`; return }
        jobs.push({ scope_type: 'account', scope_id: priceForm.scope_id, model: row.model, resolution, unit_price_usd: price, note: priceForm.note, effective_at: effectiveAt.toISOString() })
      }
    }
    if (!jobs.length) { formError.value = t('admin.profitAccounting.required'); return }
    pendingPrices.value = jobs
  }
  saving.value = true; formError.value = ''
  try {
    while (pendingPrices.value.length) {
      await api.createPrice(pendingPrices.value[0]!)
      pendingPrices.value.shift(); savedCount.value++
    }
    savedPriceDate.value = priceTime.value ? priceTime.value.slice(0, 10) : ''
    savedPriceNotice.value = priceTime.value
      ? t('admin.profitAccounting.savedFrom', { time: priceTime.value.replace('T', ' ') })
      : t('admin.profitAccounting.savedNow')
    showPrice.value = false; app.showSuccess(t('admin.profitAccounting.saved')); pricePage.value = 1
  } catch (e) { formError.value = message(e) }
  finally { saving.value = false; await loadPrices() }
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
onUnmounted(() => { overviewSequence++; reportSequence++; priceSequence++; detailSequence++; scopeSequence++; modelSequence++; scopeAbort?.abort() })
</script>

<style scoped>
/* Bound the existing scroll container so DataTable's sticky header stays visible. */
.profit-table-region :deep(.table-wrapper) {
  max-height: min(42vh, 420px);
  max-height: min(42dvh, 420px);
  scrollbar-gutter: stable;
}

.profit-table-region :deep(.table-wrapper:focus-visible) {
  outline: 2px solid var(--color-primary-500, #14b8a6);
  outline-offset: -2px;
}

.profit-table-region :deep(.table-header) {
  box-shadow: 0 1px 0 rgb(148 163 184 / 30%);
}
</style>
