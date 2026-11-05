import { apiClient } from '../client'

export interface PaidRedeemDiscountConfig {
  enabled: boolean
  group_id: number
  rate_multiplier: number
  source_group_ids: number[]
}

export interface PaidRedeemDiscountPreview {
  config: PaidRedeemDiscountConfig
  group_name: string
  default_rate: number
  user_count: number
  change_count: number
  preview_token: string
  users: Array<{
    user_id: number
    email: string
    username: string
    current_rate: number | null
    effective_rate: number
    new_rate: number
    will_change: boolean
  }>
}

const endpoint = '/admin/settings/paid-redeem-discount'

export async function getConfig(): Promise<PaidRedeemDiscountConfig> {
  return (await apiClient.get<PaidRedeemDiscountConfig>(endpoint)).data
}

export async function updateConfig(config: PaidRedeemDiscountConfig): Promise<PaidRedeemDiscountConfig> {
  return (await apiClient.put<PaidRedeemDiscountConfig>(endpoint, config)).data
}

export async function preview(): Promise<PaidRedeemDiscountPreview> {
  return (await apiClient.post<PaidRedeemDiscountPreview>(`${endpoint}/preview`)).data
}

export async function backfill(previewToken: string): Promise<{ user_count: number; updated_count: number }> {
  return (await apiClient.post<{ user_count: number; updated_count: number }>(`${endpoint}/backfill`, {
    preview_token: previewToken,
  })).data
}
