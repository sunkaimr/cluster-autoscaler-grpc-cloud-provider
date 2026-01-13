import request from './index'
import type { ApiResponse, NodeGroup, NodeGroupsConfig } from '@/types'

// 获取完整状态
export function getNodeGroupStatus() {
  return request.get<ApiResponse<NodeGroupsConfig>>('/nodegroup-status')
}

// 获取 NodeGroup 列表
export function getNodeGroups(id?: string) {
  return request.get<ApiResponse<NodeGroup | NodeGroup[]>>('/nodegroup', {
    params: { id }
  })
}

// 更新 NodeGroup
export function updateNodeGroup(data: NodeGroup) {
  return request.post<ApiResponse<NodeGroup>>('/nodegroup', data)
}
