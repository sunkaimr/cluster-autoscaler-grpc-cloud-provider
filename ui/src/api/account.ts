import request from './index'
import type { ApiResponse, Accounts } from '@/types'

// 获取云账号列表
export function getAccounts() {
  return request.get<ApiResponse<Accounts>>('/cloud-provider-option/account')
}

// 更新云账号
export function updateAccount(data: Accounts) {
  return request.post<ApiResponse<Accounts>>('/cloud-provider-option/account', data)
}

// 删除云账号
export function deleteAccount(provider: string, account: string) {
  return request.delete<ApiResponse<Accounts>>(`/cloud-provider-option/provider/${provider}/account/${account}`)
}

// 删除云服务商（包括其下所有账号）
export function deleteProvider(provider: string) {
  return request.delete<ApiResponse<Accounts>>(`/cloud-provider-option/provider/${provider}`)
}
