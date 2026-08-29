import { apiClient } from '../client'

export interface DailyTokenRankingRewardEntry {
  rank: number
  display_name: string
  total_tokens: number
  request_count: number
  reward_amount: number
  status: 'pending' | 'paid' | 'skipped' | 'empty' | string
  reason?: string
  note: string
  settled_at?: string
}

export interface DailyTokenRankingRewardResponse {
  date: string
  timezone: string
  settled: boolean
  entries: DailyTokenRankingRewardEntry[]
}

export async function previewDailyTokenRankingReward(date?: string): Promise<DailyTokenRankingRewardResponse> {
  const { data } = await apiClient.get<DailyTokenRankingRewardResponse>('/admin/usage/daily-token-ranking-reward', {
    params: date ? { date } : undefined,
  })
  return data
}

export async function settleDailyTokenRankingReward(date: string): Promise<DailyTokenRankingRewardResponse> {
  const { data } = await apiClient.post<DailyTokenRankingRewardResponse>('/admin/usage/daily-token-ranking-reward/settle', { date })
  return data
}

const dailyTokenRankingRewardAPI = {
  preview: previewDailyTokenRankingReward,
  settle: settleDailyTokenRankingReward,
}

export default dailyTokenRankingRewardAPI
