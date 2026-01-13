// API 响应格式
export interface ApiResponse<T = unknown> {
  status: number
  msg: string
  error?: string
  data?: T
}

// Instance 阶段
export type Stage =
  | 'Pending'
  | 'Creating'
  | 'Created'
  | 'Joined'
  | 'Running'
  | 'PendingDeletion'
  | 'Deleting'
  | 'Deleted'

// Instance 状态
export type Status = 'Init' | 'InProcess' | 'Success' | 'Failed' | 'Unknown'

// Instance 实例
export interface Instance {
  id: string
  name: string
  ip: string
  providerID: string
  stage: Stage
  status: Status
  error?: string
  updateTime: string
}

// Kubernetes Taint
export interface Taint {
  key: string
  value: string
  effect: string
}

// NodeTemplate
export interface NodeTemplate {
  labels: Record<string, string>
  annotations: Record<string, string>
  capacity: Record<string, string>
  allocatable?: Record<string, string>
  taints?: Taint[]
}

// AutoscalingOptions
export interface AutoscalingOptions {
  scaleDownUtilizationThreshold?: number
  scaleDownGpuUtilizationThreshold?: number
  scaleDownUnneededTime?: number
  scaleDownUnreadyTime?: number
}

// NodeGroup
export interface NodeGroup {
  id: string
  minSize: number
  maxSize: number
  targetSize: number
  autoscalingOptions?: AutoscalingOptions
  nodeTemplate?: NodeTemplate
  instanceParameter: string
  instances?: Instance[]
}

// 云账号凭证
export interface Credential {
  secretId: string
  secretKey: string
}

// Provider 类型 (provider -> account -> credential)
export type Provider = Record<string, Credential>

// Accounts 类型 (provider -> accounts)
export type Accounts = Record<string, Provider>

// Instance 参数
export interface InstanceParameter {
  providerIdTemplate: string
  parameter: Record<string, unknown>
}

// InstanceParameter Map
export type InstanceParameterMap = Record<string, InstanceParameter>

// CloudProviderOption
export interface CloudProviderOption {
  accounts: Accounts
  instanceParameter: InstanceParameterMap
}

// NodeGroupsConfig 完整配置
export interface NodeGroupsConfig {
  cloudProviderOption: CloudProviderOption
  nodeGroups: NodeGroup[]
}

// 状态统计
export interface StageStats {
  stage: Stage
  count: number
}

export interface StatusStats {
  status: Status
  count: number
}
