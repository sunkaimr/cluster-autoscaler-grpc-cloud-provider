<template>
  <div class="page-container">
    <h2 style="margin-bottom: 20px">Instance 管理</h2>

    <!-- 筛选条件 -->
    <el-card shadow="never" style="margin-bottom: 20px">
      <el-form :inline="true" :model="filterForm">
        <el-form-item label="NodeGroup">
          <el-select v-model="filterForm.nodegroup" placeholder="全部" clearable style="width: 200px">
            <el-option
              v-for="ng in nodeGroups"
              :key="ng.id"
              :label="ng.id"
              :value="ng.id"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="Stage">
          <el-select v-model="filterForm.stage" placeholder="全部" clearable style="width: 150px">
            <el-option
              v-for="stage in stages"
              :key="stage"
              :label="stageNameMap[stage]"
              :value="stage"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="Status">
          <el-select v-model="filterForm.status" placeholder="全部" clearable style="width: 150px">
            <el-option
              v-for="status in statuses"
              :key="status"
              :label="statusNameMap[status]"
              :value="status"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="搜索">
          <el-input
            v-model="filterForm.keyword"
            placeholder="ID/IP/Name"
            clearable
            style="width: 200px"
          />
        </el-form-item>

        <el-form-item>
          <el-button type="primary" @click="handleFilter">查询</el-button>
          <el-button @click="handleReset">重置</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <!-- Instance 表格 -->
    <el-table :data="instances" style="width: 100%" border>
      <el-table-column prop="id" label="Instance ID" width="120" align="center" />
      <el-table-column prop="name" label="Name" width="250" align="center" />
      <el-table-column prop="ip" label="IP" width="120" align="center" />
      <el-table-column label="Stage" width="100" align="center">
        <template #default="{ row }">
          <StatusTag :value="row.stage" type="stage" />
        </template>
      </el-table-column>
      <el-table-column label="Status" width="100" align="center">
        <template #default="{ row }">
          <StatusTag :value="row.status" type="status" />
        </template>
      </el-table-column>
      <el-table-column prop="providerID" label="Provider ID" min-width="200" align="center" />
      <el-table-column label="更新时间" width="180" align="center">
        <template #default="{ row }">
          {{ formatTime(row.updateTime) }}
        </template>
      </el-table-column>
      <el-table-column label="操作" width="120" align="center" fixed="right">
        <template #default="{ row }">
          <el-button type="primary" size="small" @click="handleEdit(row)">编辑</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 分页 -->
    <el-pagination
      v-model:current-page="pagination.page"
      v-model:page-size="pagination.pageSize"
      :page-sizes="[10, 20, 50, 100]"
      :total="pagination.total"
      layout="total, sizes, prev, pager, next, jumper"
      style="margin-top: 20px; justify-content: flex-end"
      @size-change="handleSizeChange"
      @current-change="handlePageChange"
    />

    <!-- 编辑对话框 -->
    <el-dialog v-model="dialogVisible" title="编辑 Instance" width="600px" @close="handleDialogClose">
      <el-form :model="form" label-width="120px">
        <el-form-item label="Instance ID">
          <el-input v-model="form.id" disabled />
        </el-form-item>

        <el-form-item label="Name">
          <el-input v-model="form.name" disabled />
        </el-form-item>

        <el-form-item label="IP">
          <el-input v-model="form.ip" disabled />
        </el-form-item>

        <el-form-item label="Stage">
          <el-select v-model="form.stage" style="width: 100%">
            <el-option
              v-for="stage in stages"
              :key="stage"
              :label="stageNameMap[stage]"
              :value="stage"
            />
          </el-select>
        </el-form-item>

        <el-form-item label="Status">
          <el-select v-model="form.status" style="width: 100%">
            <el-option
              v-for="status in statuses"
              :key="status"
              :label="statusNameMap[status]"
              :value="status"
            />
          </el-select>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleSave">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { getInstances, updateInstanceStatus } from '@/api/instance'
import { getNodeGroups } from '@/api/nodegroup'
import type { Instance, NodeGroup, Stage, Status } from '@/types'
import { stageNameMap, statusNameMap, formatTime } from '@/utils'
import StatusTag from '@/components/StatusTag.vue'

interface InstanceWithNodeGroup extends Instance {
  nodeGroupId: string
}

const stages: Stage[] = [
  'Pending',
  'Creating',
  'Created',
  'Joined',
  'Running',
  'PendingDeletion',
  'Deleting',
  'Deleted'
]

const statuses: Status[] = ['Init', 'InProcess', 'Success', 'Failed', 'Unknown']

const nodeGroups = ref<NodeGroup[]>([])
const instances = ref<InstanceWithNodeGroup[]>([])
const dialogVisible = ref(false)

const filterForm = ref({
  nodegroup: '',
  stage: '',
  status: '',
  keyword: ''
})

// 分页状态
const pagination = ref({
  page: 1,
  pageSize: 20,
  total: 0
})

const form = ref({
  id: '',
  nodeGroupId: '',
  name: '',
  ip: '',
  stage: '' as Stage,
  status: '' as Status
})

const handleFilter = async () => {
  try {
    // 重置到第一页
    pagination.value.page = 1
    // 调用后端接口筛选
    await loadInstancesWithFilter()
  } catch (error) {
    console.error('筛选失败:', error)
  }
}

const handleReset = async () => {
  filterForm.value = {
    nodegroup: '',
    stage: '',
    status: '',
    keyword: ''
  }
  // 重置到第一页
  pagination.value.page = 1
  // 重置后重新加载所有数据
  await loadInstancesWithFilter()
}

// 分页大小改变
const handleSizeChange = async (pageSize: number) => {
  pagination.value.pageSize = pageSize
  pagination.value.page = 1
  await loadInstancesWithFilter()
}

// 当前页改变
const handlePageChange = async (page: number) => {
  pagination.value.page = page
  await loadInstancesWithFilter()
}

const handleEdit = (row: InstanceWithNodeGroup) => {
  form.value = {
    id: row.id,
    nodeGroupId: row.nodeGroupId,
    name: row.name || '',
    ip: row.ip || '',
    stage: row.stage,
    status: row.status
  }
  dialogVisible.value = true
}

const handleSave = async () => {
  try {
    // 根据 API 定义，updateInstanceStatus 需要传递数组
    await updateInstanceStatus([
      {
        nodeGroupId: form.value.nodeGroupId,
        id: form.value.id,
        stage: form.value.stage,
        status: form.value.status
      }
    ])

    ElMessage.success('保存成功')
    dialogVisible.value = false
    await loadData()
  } catch (error: any) {
    ElMessage.error(`保存失败: ${error.message || error}`)
  }
}

const handleDialogClose = () => {
  form.value = {
    id: '',
    nodeGroupId: '',
    name: '',
    ip: '',
    stage: '' as Stage,
    status: '' as Status
  }
}

// 加载 Instances（带筛选参数）
const loadInstancesWithFilter = async () => {
  try {
    // 构建查询参数
    const params: any = {}

    if (filterForm.value.nodegroup) params.nodegroup = filterForm.value.nodegroup
    if (filterForm.value.stage) params.stage = filterForm.value.stage
    if (filterForm.value.status) params.status = filterForm.value.status

    // 关键字智能检测
    if (filterForm.value.keyword) {
      const keyword = filterForm.value.keyword.trim()
      if (keyword.includes('.')) {
        params.ip = keyword
      } else if (keyword.includes('-')) {
        params.name = keyword
      } else {
        params.id = keyword
      }
    }

    const instancesRes = await getInstances(params)
    const instancesList = instancesRes.data.data || []

    // 创建 instance ID 到 nodeGroup ID 的映射
    const instanceToNodeGroupMap = new Map<string, string>()

    nodeGroups.value.forEach((ng) => {
      if (ng.instances && Array.isArray(ng.instances)) {
        ng.instances.forEach((instance) => {
          instanceToNodeGroupMap.set(instance.id, ng.id)
        })
      }
    })

    // 将 Instance 与 NodeGroup 关联
    const allInstances: InstanceWithNodeGroup[] = instancesList.map((instance) => ({
      ...instance,
      nodeGroupId: instanceToNodeGroupMap.get(instance.id) || '未知'
    }))

    // 更新总数
    pagination.value.total = allInstances.length

    // 客户端分页
    const start = (pagination.value.page - 1) * pagination.value.pageSize
    const end = start + pagination.value.pageSize
    instances.value = allInstances.slice(start, end)
  } catch (error) {
    console.error('加载 Instances 失败:', error)
    ElMessage.error('加载 Instances 失败')
  }
}

// 初始化加载数据
const loadData = async () => {
  try {
    // 先加载 NodeGroups
    const ngRes = await getNodeGroups()
    nodeGroups.value = ngRes.data.data || []

    // 再加载所有 Instances
    await loadInstancesWithFilter({})
  } catch (error) {
    console.error('加载数据失败:', error)
    ElMessage.error('加载数据失败')
  }
}

onMounted(() => {
  loadData()
})
</script>
