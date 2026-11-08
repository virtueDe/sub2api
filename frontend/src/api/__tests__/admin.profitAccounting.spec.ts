import { beforeEach, describe, expect, it, vi } from 'vitest'
import { profitAccountingAPI } from '../admin/profitAccounting'

const client = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('../client', () => ({ apiClient: client }))

describe('profit accounting API', () => {
  beforeEach(() => vi.clearAllMocks())
  it('uses explicit day boundaries and history selector', async () => {
    client.get.mockResolvedValue({ data: { items: [] } })
    await profitAccountingAPI.reports('2026-11-07', '2026-11-07', true)
    expect(client.get).toHaveBeenCalledWith('/admin/profit-accounting/reports', { params: { start_date: '2026-11-07', end_date: '2026-11-07', history: true } })
  })
  it('preserves decimal prices as strings and effective instants', async () => {
    const input = { scope_type: 'account' as const, scope_id: 1, model: 'gemini-nano-banana-2.1', resolution: '4K', unit_price_usd: '0.1234567891', effective_at: '2026-11-07T16:00:00Z', note: 'New purchase price' }
    client.post.mockResolvedValue({ data: input })
    await profitAccountingAPI.createPrice(input)
    expect(client.post).toHaveBeenCalledWith('/admin/profit-accounting/prices', input)
  })
})
