import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserErrorRequestsTable from '../UserErrorRequestsTable.vue'
import UserErrorDetailModal from '../UserErrorDetailModal.vue'
import type { UserErrorRequest } from '@/types'

const { copyToClipboard } = vi.hoisted(() => ({ copyToClipboard: vi.fn() }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

enableAutoUnmount(afterEach)
beforeEach(() => copyToClipboard.mockReset())

const row: UserErrorRequest = {
  id: 42,
  request_id: 'req-error-list-full-id',
  created_at: '2026-11-05T00:00:00Z',
  model: 'image-model',
  inbound_endpoint: '/v1/images/generations',
  status_code: 502,
  category: 'upstream',
  platform: 'openai',
  message: 'upstream error',
  key_name: 'my-key',
  key_deleted: false,
}

function mountTable(requestId = row.request_id, visibleColumnKeys?: string[]) {
  return mount(UserErrorRequestsTable, {
    props: { rows: [{ ...row, request_id: requestId }], total: 1, loading: false, page: 1, pageSize: 20, visibleColumnKeys },
    global: {
      stubs: {
        DataTable: {
          props: ['columns', 'data'],
          emits: ['rowClick'],
          template: `<div>
            <div v-for="item in data" :key="item.id" data-testid="row" @click="$emit('rowClick', item)">
              <div v-for="column in columns" :key="column.key" :data-field="column.key">
                <slot :name="'cell-' + column.key" :row="item" />
              </div>
            </div>
          </div>`,
        },
        Pagination: true,
        EmptyState: true,
        Icon: true,
        IpGeoCell: true,
        IpGeoBatchToolbar: true,
        UserErrorDetailModal: true,
      },
    },
  })
}

describe('user error request IDs', () => {
  it('copies the full ID without opening the row detail', async () => {
    const wrapper = mountTable()
    const cell = wrapper.get('[data-field="request_id"]')
    expect(cell.text()).toBe(row.request_id)
    expect(cell.get('span').attributes('title')).toBe(row.request_id)

    await cell.get('button').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith(row.request_id, 'admin.usage.requestIdCopied')
    expect(wrapper.getComponent(UserErrorDetailModal).props('show')).toBe(false)

    await wrapper.get('[data-testid="row"]').trigger('click')
    expect(wrapper.getComponent(UserErrorDetailModal).props()).toMatchObject({ show: true, errorId: row.id })
  })

  it('shows a placeholder without a copy action when the ID is empty', () => {
    const cell = mountTable('').get('[data-field="request_id"]')
    expect(cell.text()).toBe('-')
    expect(cell.find('button').exists()).toBe(false)
  })

  it('respects the saved visible columns', () => {
    const wrapper = mountTable(row.request_id, ['model'])
    expect(wrapper.find('[data-field="request_id"]').exists()).toBe(false)
  })
})
