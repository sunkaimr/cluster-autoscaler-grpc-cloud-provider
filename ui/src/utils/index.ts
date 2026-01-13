import type { Stage, Status } from '@/types'

// Stage 颜色映射
export const stageColorMap: Record<Stage, string> = {
  Pending: 'info',
  Creating: 'warning',
  Created: 'warning',
  Joined: 'warning',
  Running: 'success',
  PendingDeletion: 'danger',
  Deleting: 'danger',
  Deleted: 'info'
}

// Status 颜色映射
export const statusColorMap: Record<Status, string> = {
  Init: 'info',
  InProcess: 'warning',
  Success: 'success',
  Failed: 'danger',
  Unknown: 'info'
}

// Stage 中文映射
export const stageNameMap: Record<Stage, string> = {
  Pending: '等待创建',
  Creating: '创建中',
  Created: '已创建',
  Joined: '已加入',
  Running: '运行中',
  PendingDeletion: '等待删除',
  Deleting: '删除中',
  Deleted: '已删除'
}

// Status 中文映射
export const statusNameMap: Record<Status, string> = {
  Init: '初始化',
  InProcess: '处理中',
  Success: '成功',
  Failed: '失败',
  Unknown: '未知'
}

// 格式化时间
export function formatTime(time: string): string {
  if (!time) return '-'
  const date = new Date(time)
  return date.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit'
  })
}

// 深拷贝
export function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}
