import { apiClient } from '../client'

export interface PlazaDisplayModel { name: string; platform: string }
export interface PlazaDisplayGroup { group_id: number; models: PlazaDisplayModel[] }
export interface PlazaDisplayConfig { mode: 'automatic' | 'manual'; groups: PlazaDisplayGroup[] }

const endpoint = '/admin/settings/model-plaza-display'

export async function getConfig(): Promise<PlazaDisplayConfig> {
  return (await apiClient.get<PlazaDisplayConfig>(endpoint)).data
}

export async function getGroupModels(groupID: number): Promise<PlazaDisplayModel[]> {
  return (await apiClient.get<PlazaDisplayModel[]>(`${endpoint}/groups/${groupID}/models`)).data
}

export async function updateConfig(config: PlazaDisplayConfig): Promise<PlazaDisplayConfig> {
  return (await apiClient.put<PlazaDisplayConfig>(endpoint, config)).data
}
