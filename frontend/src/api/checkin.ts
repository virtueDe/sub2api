import { apiClient } from './client'

export interface CheckinConfig {
  enabled: boolean
  reward_min: number
  reward_max: number
  condition: '' | 'request' | 'consumption'
  request_threshold?: number
  consumption_threshold?: number
}

export interface CheckinStatus {
  enabled: boolean
  checked_in_today: boolean
  today_reward: number
  total_reward: number
  streak_days: number
  account?: { id: number; username: string; email: string; balance: number }
  date?: string
  config?: CheckinConfig
}

export interface CheckinHistoryItem {
  id: number
  check_in_date: string
  reward: number
  created_at: string
}

export interface CheckinHistory {
  items: CheckinHistoryItem[]
}

export interface CheckinResult {
  checked_in_today: boolean
  reward: number
  total_reward: number
  streak_days: number
  checked_in_at: string
  balance?: number
}

export type CheckInCondition = CheckinConfig['condition']
export type CheckInConfig = CheckinConfig
export type CheckInStatus = CheckinStatus
export type CheckInRecord = CheckinHistoryItem
export type CheckInHistoryResponse = CheckinHistory
export type CheckInResponse = CheckinResult

export async function getCheckinStatus(): Promise<CheckinStatus> {
  const { data } = await apiClient.get<CheckinStatus>('/user/check-in/status')
  return data
}

export async function checkin(): Promise<CheckinResult> {
  const { data } = await apiClient.post<CheckinResult>('/user/check-in')
  return data
}

export async function getCheckinHistory(limit = 31, month?: string): Promise<CheckinHistory> {
  const { data } = await apiClient.get<CheckinHistory>('/user/check-in/history', { params: { limit, month } })
  return data
}

export const checkinAPI = {
  getStatus: getCheckinStatus,
  checkIn: checkin,
  getHistory: getCheckinHistory,
}

export const getCheckInStatus = getCheckinStatus
export const checkIn = checkin
export const getCheckInHistory = getCheckinHistory

export const checkInAPI = checkinAPI

export const adminCheckinAPI = {
  getConfig: () => apiClient.get<CheckinConfig>('/admin/check-in/config').then(({ data }) => data),
  updateConfig: (config: CheckinConfig) => apiClient.put<CheckinConfig>('/admin/check-in/config', config).then(({ data }) => data),
}

export default checkinAPI
