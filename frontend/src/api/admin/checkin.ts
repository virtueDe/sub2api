import { apiClient } from '../client'
import type { CheckInCondition, CheckInConfig } from '../checkin'

export type { CheckInCondition, CheckInConfig }

export type UpdateCheckInConfigRequest = CheckInConfig

export interface CheckInRecord {
  id: number
  user_id: number
  email: string
  username: string
  check_in_date: string
  reward: number
  request_count: number
  daily_spend: number
  created_at: string
}

export interface CheckInRecordResponse {
  items: CheckInRecord[]
  total: number
  page: number
  page_size: number
}

export async function getCheckInConfig(): Promise<CheckInConfig> {
  const { data } = await apiClient.get<CheckInConfig>('/admin/check-in/config')
  return data
}

export async function updateCheckInConfig(
  config: UpdateCheckInConfigRequest,
): Promise<CheckInConfig> {
  const { data } = await apiClient.put<CheckInConfig>('/admin/check-in/config', config)
  return data
}

const checkInAdminAPI = {
  getConfig: getCheckInConfig,
  updateConfig: updateCheckInConfig,
  getRecords: (params?: { month?: string; user_id?: number; page?: number; page_size?: number }) => apiClient.get<CheckInRecordResponse>('/admin/check-in/records', { params }).then(({ data }) => data),
}

export const adminCheckinAPI = checkInAdminAPI
export const checkinAdminAPI = checkInAdminAPI

export default checkInAdminAPI
