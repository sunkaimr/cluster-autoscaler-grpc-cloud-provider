<template>
  <div class="page-container">
    <h2 style="margin-bottom: 20px">NodeGroup 管理</h2>

    <el-table :data="nodeGroups" style="width: 100%; height: calc(100vh - 150px)" border>
      <el-table-column prop="id" label="ID" min-width="200" />
      <el-table-column prop="minSize" label="MinSize" min-width="100" align="center" />
      <el-table-column prop="maxSize" label="MaxSize" min-width="100" align="center" />
      <el-table-column prop="targetSize" label="TargetSize" min-width="120" align="center" />
      <el-table-column prop="instanceParameter" label="Instance 参数" min-width="200" />
      <el-table-column label="操作" min-width="180" align="center" fixed="right">
        <template #default="{ row }">
          <el-button type="info" size="small" @click="handleShowMore(row)">更多</el-button>
          <el-button type="primary" size="small" @click="handleEdit(row)">编辑</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 更多信息对话框（只读） -->
    <el-dialog v-model="moreDialogVisible" title="NodeGroup 详细信息" width="900px" draggable>
      <div style="margin-bottom: 20px">
        <h3 style="margin-bottom: 10px">AutoscalingOptions</h3>
        <el-input
          v-model="moreInfo.autoscalingOptionsYaml"
          type="textarea"
          :rows="5"
          readonly
          style="font-family: 'Courier New', monospace; font-size: 13px"
        />
      </div>

      <div>
        <h3 style="margin-bottom: 10px">NodeTemplate</h3>
        <el-input
          v-model="moreInfo.nodeTemplateYaml"
          type="textarea"
          :rows="15"
          readonly
          style="font-family: 'Courier New', monospace; font-size: 13px"
        />
      </div>

      <template #footer>
        <el-button @click="moreDialogVisible = false">关闭</el-button>
      </template>
    </el-dialog>

    <!-- 编辑对话框 -->
    <el-dialog v-model="editDialogVisible" title="编辑 NodeGroup" width="700px" @close="handleDialogClose">
      <el-alert
        v-if="yamlError"
        :title="yamlError"
        type="error"
        :closable="false"
        style="margin-bottom: 15px"
      />

      <el-input
        v-model="editYaml"
        type="textarea"
        :rows="20"
        placeholder="请输入 YAML 格式的 NodeGroup 配置"
        style="font-family: 'Courier New', monospace; font-size: 13px"
        @input="validateYaml"
      />

      <template #footer>
        <el-button @click="editDialogVisible = false">取消</el-button>
        <el-button type="primary" :disabled="!!yamlError" @click="handleSave">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { parse, stringify } from 'yaml'
import { getNodeGroups, updateNodeGroup } from '@/api/nodegroup'
import type { NodeGroup } from '@/types'

const nodeGroups = ref<NodeGroup[]>([])

// 更多信息对话框（只读）
const moreDialogVisible = ref(false)
const moreInfo = ref({
  autoscalingOptionsYaml: '',
  nodeTemplateYaml: ''
})

// 编辑对话框
const editDialogVisible = ref(false)
const editYaml = ref('')
const yamlError = ref('')
const currentNodeGroup = ref<NodeGroup | null>(null)

// 显示更多信息（只读）
const handleShowMore = (row: NodeGroup) => {
  moreInfo.value = {
    autoscalingOptionsYaml: row.autoscalingOptions ? stringify(row.autoscalingOptions) : '# 未配置',
    nodeTemplateYaml: row.nodeTemplate ? stringify(row.nodeTemplate) : '# 未配置'
  }
  moreDialogVisible.value = true
}

// 验证 YAML 语法
const validateYaml = () => {
  try {
    if (editYaml.value.trim()) {
      parse(editYaml.value)
    }
    yamlError.value = ''
  } catch (error: any) {
    yamlError.value = `YAML 格式错误: ${error.message}`
  }
}

// 编辑
const handleEdit = (row: NodeGroup) => {
  currentNodeGroup.value = row
  // 将整个 NodeGroup 对象转为 YAML
  editYaml.value = stringify(row)
  yamlError.value = ''
  editDialogVisible.value = true
}

// 保存
const handleSave = async () => {
  if (yamlError.value) {
    ElMessage.error('YAML 格式错误，请检查后重试')
    return
  }

  if (!editYaml.value.trim()) {
    ElMessage.warning('请输入配置内容')
    return
  }

  try {
    const data = parse(editYaml.value) as NodeGroup

    // 确保 id 字段存在
    if (!data.id) {
      ElMessage.error('NodeGroup ID 不能为空')
      return
    }

    await updateNodeGroup(data)
    ElMessage.success('保存成功')
    editDialogVisible.value = false
    await loadData()
  } catch (error: any) {
    ElMessage.error(`保存失败: ${error.message || error}`)
  }
}

// 对话框关闭
const handleDialogClose = () => {
  editYaml.value = ''
  yamlError.value = ''
  currentNodeGroup.value = null
}

// 加载数据
const loadData = async () => {
  try {
    const { data } = await getNodeGroups()
    nodeGroups.value = data.data || []
  } catch (error) {
    console.error('加载 NodeGroup 失败:', error)
    ElMessage.error('加载 NodeGroup 失败')
  }
}

onMounted(() => {
  loadData()
})
</script>
