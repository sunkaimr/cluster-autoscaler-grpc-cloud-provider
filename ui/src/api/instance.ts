import request from './index'
import type { ApiResponse, Instance } from '@/types'

export interface InstanceQuery {
  id?: string
  name?: string
  ip?: string
  stage?: string
  status?: string
  nodegroup?: string
}

// 获取 Instance 列表
export function getInstances(params?: InstanceQuery) {
  return request.get<ApiResponse<Instance[]>>('/nodegroup/instance', { params })
}

// 更新 Instance 状态
export function updateInstanceStatus(data: any) {
  return request.post<ApiResponse<any>>('/nodegroup/instance', data)
}
