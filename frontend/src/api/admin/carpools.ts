/**
 * Admin Carpool API (系统管理员：建车、座位、成员)
 */

import { apiClient } from '../client'
import type {
  Carpool,
  CarpoolAccountAction,
  CarpoolDetail,
  CarpoolQuotaRequest,
  CarpoolRole
} from '../carpools'

export interface CarpoolMemberRequest {
  user_id?: number
  email?: string
  role?: CarpoolRole
}

export interface CreateCarpoolRequest {
  name: string
  account_id: number
  /** 留空则跟随自动预估 */
  weekly_quota_usd?: number
  seats: number
  reserve_ratio: number
  validity_days: number
  notes?: string
  exclusive_account: boolean
  members: CarpoolMemberRequest[]
}

export interface UpdateCarpoolRequest extends CarpoolQuotaRequest {
  name?: string
  notes?: string
  seats?: number
  validity_days?: number
}

export async function list(): Promise<Carpool[]> {
  const { data } = await apiClient.get<Carpool[]>('/admin/carpools')
  return data
}

export async function getDetail(id: number): Promise<CarpoolDetail> {
  const { data } = await apiClient.get<CarpoolDetail>(`/admin/carpools/${id}`)
  return data
}

export async function create(req: CreateCarpoolRequest): Promise<Carpool> {
  const { data } = await apiClient.post<Carpool>('/admin/carpools', req)
  return data
}

export async function update(id: number, req: UpdateCarpoolRequest): Promise<Carpool> {
  const { data } = await apiClient.put<Carpool>(`/admin/carpools/${id}`, req)
  return data
}

/** 管理员调额度复用 update，便于与成员侧共用详情面板 */
export async function updateQuota(id: number, req: CarpoolQuotaRequest): Promise<Carpool> {
  return update(id, req)
}

export async function remove(id: number): Promise<void> {
  await apiClient.delete(`/admin/carpools/${id}`)
}

export async function addMember(id: number, req: CarpoolMemberRequest): Promise<void> {
  await apiClient.post(`/admin/carpools/${id}/members`, req)
}

export async function setMemberRole(id: number, userId: number, role: CarpoolRole): Promise<void> {
  await apiClient.put(`/admin/carpools/${id}/members/${userId}`, { role })
}

export async function removeMember(id: number, userId: number): Promise<void> {
  await apiClient.delete(`/admin/carpools/${id}/members/${userId}`)
}

export async function renew(id: number): Promise<void> {
  await apiClient.post(`/admin/carpools/${id}/renew`)
}

export async function accountAction(id: number, action: CarpoolAccountAction, schedulable?: boolean): Promise<void> {
  await apiClient.post(`/admin/carpools/${id}/account/action`, { action, schedulable })
}

export const carpoolsAdminAPI = {
  list,
  getDetail,
  create,
  update,
  updateQuota,
  remove,
  addMember,
  setMemberRole,
  removeMember,
  renew,
  accountAction
}
export default carpoolsAdminAPI
