/**
 * Carpool API (车内成员视角)
 * 只能看到自己所在的拼车；manager 可调整本车账号与额度。
 */

import { apiClient } from './client'

export type CarpoolRole = 'manager' | 'member'
export type CarpoolQuotaSource = 'auto' | 'manual'
export type CarpoolAccountAction = 'set_schedulable' | 'clear_error' | 'clear_rate_limit' | 'refresh_usage'

export interface Carpool {
  id: number
  name: string
  account_id: number
  group_id: number
  /** 当前生效的账号周额度；0 表示尚未预估 */
  weekly_quota_usd: number
  quota_source: CarpoolQuotaSource
  seats: number
  reserve_ratio: number
  per_seat_weekly_usd: number
  validity_days: number
  notes: string
  status: string
  member_count: number
  created_at: string
  my_role?: CarpoolRole
}

export interface CarpoolUsageWindow {
  utilization: number
  resets_at: string | null
  remaining_seconds: number
}

export interface CarpoolAccount {
  id: number
  name: string
  platform: string
  type: string
  status: string
  schedulable: boolean
  error_message?: string
  rate_limit_reset_at?: string
  temp_unschedulable_until?: string
  overload_until?: string
  last_used_at?: string
  five_hour?: CarpoolUsageWindow
  seven_day?: CarpoolUsageWindow
}

/** 周额度预估：样本成本 ÷ 样本期间 7 天窗口上涨比例，由后台任务随使用持续更新 */
export interface CarpoolEstimate {
  /** 0 表示尚未产生有效预估 */
  estimated_weekly_usd: number
  sample_cost_usd: number
  sample_used_percent: number
  sample_start?: string
  window_reset_at?: string
  min_sample_percent: number
  applied_weekly_usd: number
  per_seat_weekly_usd: number
}

export interface CarpoolMember {
  user_id: number
  email: string
  username: string
  role: CarpoolRole
  is_me: boolean
  joined_at: string
  subscription_id?: number
  subscription_status?: string
  expires_at?: string
  weekly_usage_usd: number
  /** 0 表示尚未预估，暂不限额 */
  weekly_limit_usd: number
  weekly_resets_at?: string
  daily_usage_usd: number
  monthly_usage_usd: number
  requests_7d: number
  tokens_7d: number
  cost_7d_usd: number
  last_used_at?: string
}

export interface CarpoolDetail {
  carpool: Carpool
  account: CarpoolAccount | null
  estimate: CarpoolEstimate
  members: CarpoolMember[]
}

export interface CarpoolQuotaRequest {
  /** 'auto' 恢复跟随预估 */
  quota_source?: 'auto'
  /** 手动指定账号周额度 */
  weekly_quota_usd?: number
  reserve_ratio?: number
}

/** 与后端 CarpoolPerSeatWeeklyUSD 一致 */
export function perSeatWeekly(weekly: number, reserveRatio: number, seats: number): number {
  if (seats <= 0 || weekly <= 0) return 0
  return Math.round(((weekly * (1 - reserveRatio)) / seats) * 10000) / 10000
}

export async function listMine(): Promise<Carpool[]> {
  const { data } = await apiClient.get<Carpool[]>('/carpools')
  return data
}

export async function getDetail(id: number): Promise<CarpoolDetail> {
  const { data } = await apiClient.get<CarpoolDetail>(`/carpools/${id}`)
  return data
}

export async function updateQuota(id: number, req: CarpoolQuotaRequest): Promise<Carpool> {
  const { data } = await apiClient.put<Carpool>(`/carpools/${id}/quota`, req)
  return data
}

export async function accountAction(id: number, action: CarpoolAccountAction, schedulable?: boolean): Promise<void> {
  await apiClient.post(`/carpools/${id}/account/action`, { action, schedulable })
}

export const carpoolsAPI = { listMine, getDetail, updateQuota, accountAction }
export default carpoolsAPI
