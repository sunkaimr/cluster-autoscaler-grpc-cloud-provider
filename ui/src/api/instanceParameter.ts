import request from './index'
import type { ApiResponse, InstanceParameterMap } from '@/types'

// 获取 Instance 参数列表
export function getInstanceParameters(name?: string) {
  return request.get<ApiResponse<InstanceParameterMap>>('/cloud-provider-option/instance-parameter', {
    params: { name }
  })
}

// 更新 Instance 参数
export function updateInstanceParameter(data: InstanceParameterMap) {
  return request.post<ApiResponse<InstanceParameterMap>>('/cloud-provider-option/instance-parameter', data)
}

// 删除 Instance 参数
export function deleteInstanceParameter(name: string) {
  return request.delete<ApiResponse<InstanceParameterMap>>(`/cloud-provider-option/instance-parameter/${name}`)
}
