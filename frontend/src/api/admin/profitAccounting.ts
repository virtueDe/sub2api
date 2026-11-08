import { apiClient } from '../client'

export interface ProcurementPrice {
  id: number
  scope_type: 'account' | 'channel'
  scope_id: number
  scope_name: string
  model: string
  resolution: string
  unit_price_usd: string
  effective_at: string
  note: string
  created_by: number
  created_at: string
}
export type CreateProcurementPrice = Pick<ProcurementPrice, 'scope_type' | 'scope_id' | 'model' | 'resolution' | 'unit_price_usd' | 'note'> & { effective_at?: string }
export interface ProfitCost {
  resolution: string
  image_count: number
  price_id: number
  unit_price_usd: string | null
  cost_usd: string | null
  missing_reason?: string
}
export interface ProfitLine {
  account_id: number
  channel_id: number
  account_name: string
  channel_name: string
  model: string
  request_count: number
  image_count: number
  revenue_usd: string
  known_cost_usd: string
  missing_count: number
  profit_usd: string | null
  costs: ProfitCost[]
}
export interface ProfitReport {
  source_incomplete: boolean
  id: number
  date: string
  revision: number
  status: 'complete' | 'incomplete'
  request_count: number
  image_count: number
  revenue_usd: string
  known_cost_usd: string
  missing_count: number
  profit_usd: string | null
  reason: string
  created_by: number
  created_at: string
  lines?: ProfitLine[]
}

export const profitAccountingAPI = {
  async prices(page = 1) {
    const { data } = await apiClient.get<{ items: ProcurementPrice[]; total: number }>('/admin/profit-accounting/prices', { params: { page, page_size: 20 } })
    return data
  },
  async createPrice(input: CreateProcurementPrice) {
    const { data } = await apiClient.post<ProcurementPrice>('/admin/profit-accounting/prices', input)
    return data
  },
  async reports(start_date: string, end_date: string, history = false) {
    const { data } = await apiClient.get<{ items: ProfitReport[] }>('/admin/profit-accounting/reports', { params: { start_date, end_date, history } })
    return data.items
  },
  async report(id: number) {
    const { data } = await apiClient.get<ProfitReport>(`/admin/profit-accounting/reports/${id}`)
    return data
  },
  async recalculate(date: string, reason: string) {
    const { data } = await apiClient.post<ProfitReport>('/admin/profit-accounting/recalculate', { date, reason })
    return data
  }
}
