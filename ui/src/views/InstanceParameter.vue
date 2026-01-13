<template>
  <div class="page-container">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px">
      <h2>Instance 参数管理</h2>
      <el-button type="primary" @click="handleAdd">添加参数</el-button>
    </div>

    <!-- Tabs 显示各个 Instance 参数 -->
    <el-tabs v-model="activeParameter" type="card" closable @tab-remove="handleRemoveTab">
      <el-tab-pane
        v-for="paramName in parameterNames"
        :key="paramName"
        :label="paramName"
        :name="paramName"
      >
        <div style="padding: 20px">
          <el-alert
            v-if="yamlErrors[paramName]"
            :title="yamlErrors[paramName]"
            type="error"
            :closable="false"
            style="margin-bottom: 15px"
          />

          <el-input
            v-model="yamlContents[paramName]"
            type="textarea"
            :rows="20"
            placeholder="请输入 YAML 格式的参数配置"
            style="font-family: 'Courier New', monospace; font-size: 13px; margin-bottom: 15px"
            @input="validateYaml(paramName)"
          />

          <div style="display: flex; gap: 10px">
            <el-button
              type="primary"
              :disabled="!!yamlErrors[paramName]"
              @click="handleSave(paramName)"
            >
              保存
            </el-button>
            <el-button @click="handleCancel(paramName)">取消</el-button>
            <el-button type="danger" @click="handleDelete(paramName)">删除参数</el-button>
          </div>
        </div>
      </el-tab-pane>

      <el-empty
        v-if="parameterNames.length === 0"
        description="暂无 Instance 参数，请添加"
        style="padding: 60px 0"
      />
    </el-tabs>

    <!-- 添加参数对话框 -->
    <el-dialog v-model="addDialogVisible" title="添加 Instance 参数" width="500px">
      <el-form :model="addForm" label-width="100px">
        <el-form-item label="参数名称">
          <el-input
            v-model="addForm.parameterName"
            placeholder="请输入参数名称（如：tencentcloud_beijing5_10-2-13-x）"
          />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="addDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleConfirmAdd">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { parse, stringify } from 'yaml'
import {
  getInstanceParameters,
  updateInstanceParameter,
  deleteInstanceParameter
} from '@/api/instanceParameter'
import type { InstanceParameter } from '@/types'

const parametersData = ref<InstanceParameter>({})
const activeParameter = ref<string>('')

// 参数名称列表
const parameterNames = computed(() => {
  return Object.keys(parametersData.value)
})

// 每个参数的 YAML 内容
const yamlContents = reactive<Record<string, string>>({})
// 每个参数的 YAML 错误信息
const yamlErrors = reactive<Record<string, string>>({})
// 每个参数的原始 YAML 内容（用于取消操作）
const originalYamls = reactive<Record<string, string>>({})

// 添加参数对话框
const addDialogVisible = ref(false)
const addForm = reactive({
  parameterName: ''
})

// 验证 YAML 语法
const validateYaml = (paramName: string) => {
  try {
    if (yamlContents[paramName]?.trim()) {
      parse(yamlContents[paramName])
    }
    yamlErrors[paramName] = ''
  } catch (error: any) {
    yamlErrors[paramName] = `YAML 格式错误: ${error.message}`
  }
}

// 初始化参数的 YAML 内容
const initParameterYaml = () => {
  // 清空旧数据
  Object.keys(yamlContents).forEach((key) => delete yamlContents[key])
  Object.keys(yamlErrors).forEach((key) => delete yamlErrors[key])
  Object.keys(originalYamls).forEach((key) => delete originalYamls[key])

  // 遍历所有参数
  for (const [paramName, paramData] of Object.entries(parametersData.value)) {
    yamlContents[paramName] = stringify(paramData)
    originalYamls[paramName] = yamlContents[paramName]
    yamlErrors[paramName] = ''
  }

  // 设置第一个参数为激活状态
  // 如果当前激活的参数已不存在，或者没有激活的参数，则设置第一个为激活
  if (parameterNames.value.length > 0) {
    if (!activeParameter.value || !parameterNames.value.includes(activeParameter.value)) {
      activeParameter.value = parameterNames.value[0]
    }
  } else {
    activeParameter.value = ''
  }
}

// 打开添加参数对话框
const handleAdd = () => {
  addForm.parameterName = ''
  addDialogVisible.value = true
}

// 确认添加参数
const handleConfirmAdd = () => {
  if (!addForm.parameterName.trim()) {
    ElMessage.warning('请输入参数名称')
    return
  }

  // 检查参数是否已存在
  if (parameterNames.value.includes(addForm.parameterName)) {
    ElMessage.error('参数名称已存在')
    return
  }

  // 初始化新参数的 YAML 内容
  yamlContents[addForm.parameterName] = ''
  yamlErrors[addForm.parameterName] = ''
  originalYamls[addForm.parameterName] = ''

  // 添加到参数数据中（空对象）
  parametersData.value[addForm.parameterName] = {}

  // 激活新添加的参数
  activeParameter.value = addForm.parameterName

  addDialogVisible.value = false
  ElMessage.success('参数已添加，请编辑配置后保存')
}

// 保存参数
const handleSave = async (paramName: string) => {
  if (!yamlContents[paramName]?.trim()) {
    ElMessage.warning('请输入参数配置')
    return
  }

  if (yamlErrors[paramName]) {
    ElMessage.error('YAML 格式错误，请检查后重试')
    return
  }

  try {
    await ElMessageBox.confirm('确定要保存此参数配置吗？', '保存确认', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    const paramData = parse(yamlContents[paramName])

    const data: InstanceParameter = {
      [paramName]: paramData
    }

    await updateInstanceParameter(data)
    ElMessage.success('保存成功')
    originalYamls[paramName] = yamlContents[paramName]
    await loadData()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error(`保存失败: ${error.message || error}`)
    }
  }
}

// 删除参数
const handleDelete = async (paramName: string) => {
  try {
    await ElMessageBox.confirm(`确定要删除参数 "${paramName}" 吗？此操作不可恢复！`, '删除确认', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    await deleteInstanceParameter(paramName)
    ElMessage.success('删除成功')

    // 清理数据
    delete yamlContents[paramName]
    delete yamlErrors[paramName]
    delete originalYamls[paramName]

    await loadData()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error(`删除失败: ${error.message || error}`)
    }
  }
}

// 通过 Tab 关闭按钮删除参数
const handleRemoveTab = (paramName: string) => {
  handleDelete(paramName)
}

// 取消编辑
const handleCancel = (paramName: string) => {
  yamlContents[paramName] = originalYamls[paramName] || ''
  yamlErrors[paramName] = ''
}

// 加载数据
const loadData = async () => {
  try {
    const { data } = await getInstanceParameters()
    parametersData.value = data.data || {}
    initParameterYaml()
  } catch (error) {
    console.error('加载参数失败:', error)
    ElMessage.error('加载参数失败')
  }
}

onMounted(() => {
  loadData()
})
</script>
