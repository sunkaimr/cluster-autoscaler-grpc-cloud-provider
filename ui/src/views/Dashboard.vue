<template>
  <div class="page-container">
    <h2 style="margin-bottom: 20px">仪表盘</h2>

    <el-row :gutter="20" style="margin-bottom: 20px">
      <el-col :span="6">
        <el-card shadow="hover">
          <div style="display: flex; align-items: center; justify-content: space-between">
            <div>
              <div style="color: #909399; font-size: 14px">NodeGroup 总数</div>
              <div style="font-size: 32px; font-weight: bold; margin-top: 10px">
                {{ stats.nodeGroupCount }}
              </div>
            </div>
            <el-icon :size="48" color="#409eff">
              <Coin />
            </el-icon>
          </div>
        </el-card>
      </el-col>

      <el-col :span="6">
        <el-card shadow="hover">
          <div style="display: flex; align-items: center; justify-content: space-between">
            <div>
              <div style="color: #909399; font-size: 14px">Instance 总数</div>
              <div style="font-size: 32px; font-weight: bold; margin-top: 10px">
                {{ stats.instanceCount }}
              </div>
            </div>
            <el-icon :size="48" color="#67c23a">
              <Monitor />
            </el-icon>
          </div>
        </el-card>
      </el-col>

      <el-col :span="6">
        <el-card shadow="hover">
          <div style="display: flex; align-items: center; justify-content: space-between">
            <div>
              <div style="color: #909399; font-size: 14px">运行中</div>
              <div style="font-size: 32px; font-weight: bold; margin-top: 10px; color: #67c23a">
                {{ stats.runningCount }}
              </div>
            </div>
            <el-icon :size="48" color="#67c23a">
              <SuccessFilled />
            </el-icon>
          </div>
        </el-card>
      </el-col>

      <el-col :span="6">
        <el-card shadow="hover">
          <div style="display: flex; align-items: center; justify-content: space-between">
            <div>
              <div style="color: #909399; font-size: 14px">失败</div>
              <div style="font-size: 32px; font-weight: bold; margin-top: 10px; color: #f56c6c">
                {{ stats.failedCount }}
              </div>
            </div>
            <el-icon :size="48" color="#f56c6c">
              <CircleCloseFilled />
            </el-icon>
          </div>
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="20">
      <el-col :span="12">
        <el-card shadow="hover">
          <template #header>
            <div style="font-weight: 500">NodeGroup 实例统计</div>
          </template>
          <el-table :data="nodeGroupStats" style="width: 100%" max-height="400">
            <el-table-column prop="id" label="NodeGroup ID" width="200" />
            <el-table-column prop="instanceCount" label="Instance 数量" align="center" />
            <el-table-column prop="targetSize" label="目标大小" align="center" />
            <el-table-column label="大小范围" align="center">
              <template #default="{ row }">
                {{ row.minSize }} ~ {{ row.maxSize }}
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>

      <el-col :span="12">
        <el-card shadow="hover">
          <template #header>
            <div style="font-weight: 500">Instance 状态分布</div>
          </template>
          <div style="height: 400px; display: flex; flex-direction: column; gap: 10px">
            <div
              v-for="item in stageStats"
              :key="item.stage"
              style="display: flex; align-items: center; justify-content: space-between; padding: 10px; background: #f5f7fa; border-radius: 4px"
            >
              <div style="display: flex; align-items: center; gap: 10px">
                <StatusTag :value="item.stage" type="stage" />
                <span style="color: #606266">{{ item.count }} 个</span>
              </div>
              <el-progress
                :percentage="getPercentage(item.count)"
                :stroke-width="16"
                :show-text="false"
                style="width: 200px"
              />
            </div>
          </div>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Coin, Monitor, SuccessFilled, CircleCloseFilled } from '@element-plus/icons-vue'
import { getNodeGroupStatus } from '@/api/nodegroup'
import type { NodeGroup, Instance, Stage } from '@/types'
import StatusTag from '@/components/StatusTag.vue'

const stats = ref({
  nodeGroupCount: 0,
  instanceCount: 0,
  runningCount: 0,
  failedCount: 0
})

const nodeGroupStats = ref<
  Array<{
    id: string
    instanceCount: number
    targetSize: number
    minSize: number
    maxSize: number
  }>
>([])

const stageStats = ref<Array<{ stage: Stage; count: number }>>([])

const getPercentage = (count: number) => {
  if (stats.value.instanceCount === 0) return 0
  return Math.round((count / stats.value.instanceCount) * 100)
}

const loadData = async () => {
  try {
    const { data } = await getNodeGroupStatus()
    const nodeGroups: NodeGroup[] = data.data?.nodeGroups || []

    // 统计 NodeGroup 数量
    stats.value.nodeGroupCount = nodeGroups.length

    // 统计每个 NodeGroup 的 Instance 数量
    nodeGroupStats.value = nodeGroups.map((ng) => ({
      id: ng.id,
      instanceCount: ng.instances?.length || 0,
      targetSize: ng.targetSize,
      minSize: ng.minSize,
      maxSize: ng.maxSize
    }))

    // 收集所有 Instance
    const allInstances: Instance[] = []
    nodeGroups.forEach((ng) => {
      if (ng.instances) {
        allInstances.push(...ng.instances)
      }
    })

    stats.value.instanceCount = allInstances.length

    // 统计 Running 和 Failed 数量
    stats.value.runningCount = allInstances.filter((ins) => ins.stage === 'Running').length
    stats.value.failedCount = allInstances.filter((ins) => ins.status === 'Failed').length

    // 按 Stage 统计
    const stageMap: Record<Stage, number> = {
      Pending: 0,
      Creating: 0,
      Created: 0,
      Joined: 0,
      Running: 0,
      PendingDeletion: 0,
      Deleting: 0,
      Deleted: 0
    }

    allInstances.forEach((ins) => {
      stageMap[ins.stage] = (stageMap[ins.stage] || 0) + 1
    })

    stageStats.value = Object.entries(stageMap).map(([stage, count]) => ({
      stage: stage as Stage,
      count
    }))
  } catch (error) {
    console.error('加载数据失败:', error)
  }
}

onMounted(() => {
  loadData()
})
</script>
