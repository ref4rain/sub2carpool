import { shallowMount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { CarpoolDetail, CarpoolMember } from '@/api/carpools'
import CarpoolDetailPanel from './CarpoolDetailPanel.vue'

const { showError } = vi.hoisted(() => ({ showError: vi.fn() }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, values?: Record<string, unknown>) => `${key} ${values?.amount ?? ''}`.trim() })
}))

function mountPanel(source: 'auto' | 'manual' = 'auto') {
  const member = (id: number, usage: number): CarpoolMember => ({
    user_id: id, email: `${id}@example.com`, username: '', role: 'member', is_me: false,
    joined_at: '', share_percent: 50, weekly_usage_usd: usage, weekly_limit_usd: 1059.6329,
    daily_usage_usd: 0, monthly_usage_usd: 0, requests_7d: 0, tokens_7d: 0, cost_7d_usd: 0
  })
  const detail: CarpoolDetail = {
    carpool: { id: 1, name: 'Car', account_id: 1, group_id: 2, weekly_quota_usd: 2119.2657,
      quota_source: source, reserve_ratio: 0, distributable_weekly_usd: 2119.2657, seats: 2,
      allocated_percent: 100, validity_days: 30, notes: '', status: 'active', member_count: 2, created_at: '' },
    account: null,
    estimate: { estimated_weekly_usd: 2119.2657, sample_cost_usd: 1707, sample_used_percent: 80,
      min_sample_percent: 5, applied_weekly_usd: 2119.2657 },
    members: [member(1, 800.816), member(2, 607.9015)]
  }
  const actions = { updateQuota: vi.fn().mockResolvedValue(detail.carpool), accountAction: vi.fn(),
    setMemberShare: vi.fn(), getResetCards: vi.fn(), useResetCard: vi.fn() }
  const wrapper = shallowMount(CarpoolDetailPanel, { props: { detail, canManage: true, actions } })
  return { wrapper, actions }
}

describe('Carpool quota precision', () => {
  it.each(['auto', 'manual'] as const)('saves a four-decimal quota from %s mode', async (source) => {
    const { wrapper, actions } = mountPanel(source)
    const edit = wrapper.findAll('button').find(button => button.text() === 'carpool.estimate.edit')!
    await edit.trigger('click')
    const input = wrapper.get<HTMLInputElement>('input[step="0.0001"]')
    if (source === 'manual') expect(input.element.value).toBe('2119.2657')
    await input.setValue('2119.2657')
    expect(input.element.checkValidity()).toBe(true)
    await input.element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(actions.updateQuota).toHaveBeenCalledWith(1, { weekly_quota_usd: 2119.2657, reserve_ratio: 0 })
    wrapper.unmount()
  })

  it('rejects a nonblank zero instead of silently enabling auto quota', async () => {
    const { wrapper, actions } = mountPanel()
    await wrapper.findAll('button').find(button => button.text() === 'carpool.estimate.edit')!.trigger('click')
    const input = wrapper.get<HTMLInputElement>('input[step="0.0001"]')
    await input.setValue('0')
    expect(input.element.checkValidity()).toBe(false)
    input.element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(actions.updateQuota).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('carpool.estimate.quotaInvalid')
    wrapper.unmount()
  })

  it('keeps an empty manual quota as the explicit auto option', async () => {
    const { wrapper, actions } = mountPanel()
    await wrapper.findAll('button').find(button => button.text() === 'carpool.estimate.edit')!.trigger('click')
    const input = wrapper.get<HTMLInputElement>('input[step="0.0001"]')
    expect(input.element.checkValidity()).toBe(true)
    input.element.closest('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(actions.updateQuota).toHaveBeenCalledWith(1, { quota_source: 'auto', reserve_ratio: 0 })
    wrapper.unmount()
  })

  it('shows the member weekly total independently of the estimate sample', () => {
    const { wrapper } = mountPanel()
    expect(wrapper.text()).toContain('carpool.members.weeklyTotal $1408.72')
    wrapper.unmount()
  })
})
