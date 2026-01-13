<template>
  <div class="page-container">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px">
      <h2>云账号管理</h2>
      <el-button type="primary" @click="handleAdd">添加账号</el-button>
    </div>

    <!-- 外层 Tabs: 云服务商 -->
    <el-tabs v-model="activeProvider" type="border-card">
      <el-tab-pane
        v-for="provider in providers"
        :key="provider.value"
        :label="provider.label"
        :name="provider.value"
      >
        <!-- 内层 Tabs: 账号列表 -->
        <el-tabs
          v-model="activeAccount[provider.value]"
          type="card"
          closable
          @tab-remove="handleRemoveTab"
        >
          <el-tab-pane
            v-for="accountName in getProviderAccounts(provider.value)"
            :key="accountName"
            :label="accountName"
            :name="accountName"
          >
            <div style="padding: 20px">
              <el-alert
                v-if="yamlErrors[accountName]"
                :title="yamlErrors[accountName]"
                type="error"
                :closable="false"
                style="margin-bottom: 15px"
              />

              <el-input
                v-model="yamlContents[accountName]"
                type="textarea"
                :rows="20"
                placeholder="请输入 YAML 格式的账号配置"
                style="font-family: 'Courier New', monospace; font-size: 13px; margin-bottom: 15px"
                @input="validateYaml(accountName)"
              />

              <div style="display: flex; gap: 10px">
                <el-button
                  type="primary"
                  :disabled="!!yamlErrors[accountName]"
                  @click="handleSave(accountName)"
                >
                  保存
                </el-button>
                <el-button @click="handleCancel(accountName)">取消</el-button>
                <el-button type="danger" @click="handleDelete(accountName)">删除账号</el-button>
              </div>
            </div>
          </el-tab-pane>

          <el-empty
            v-if="getProviderAccounts(provider.value).length === 0"
            description="暂无账号，请添加"
            style="padding: 60px 0"
          />
        </el-tabs>
      </el-tab-pane>
    </el-tabs>

    <!-- 添加账号对话框 -->
    <el-dialog v-model="addDialogVisible" title="添加账号" width="500px">
      <el-form :model="addForm" label-width="100px">
        <el-form-item label="云服务商">
          <el-select v-model="addForm.provider" placeholder="请选择云服务商" style="width: 100%">
            <el-option
              v-for="provider in providers"
              :key="provider.value"
              :label="provider.label"
              :value="provider.value"
            />
          </el-select>
        </el-form-item>
        <el-form-item label="账号名称">
          <el-input
            v-model="addForm.accountName"
            placeholder="请输入账号名称（字母、数字、下划线、横线）"
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
import { getAccounts, updateAccount, deleteAccount } from '@/api/account'
import type { CloudProviderOption, Accounts } from '@/types'

// 云服务商中文名称映射
const providerNameMap: Record<string, string> = {
  aws: 'AWS',
  ali: '阿里云',
  tencentcloud: '腾讯云',
  huaweicloud: '华为云',
  azure: 'Azure',
  gcp: 'Google Cloud'
}

const activeProvider = ref<string>('')
const accountsData = ref<CloudProviderOption>({})

// 动态获取云服务商列表
const providers = computed(() => {
  return Object.keys(accountsData.value).map((key) => ({
    label: providerNameMap[key] || key,
    value: key
  }))
})

// 每个云服务商当前激活的账号 Tab
const activeAccount = reactive<Record<string, string>>({})

// 每个账号的 YAML 内容
const yamlContents = reactive<Record<string, string>>({})
// 每个账号的 YAML 错误信息
const yamlErrors = reactive<Record<string, string>>({})
// 每个账号的原始 YAML 内容（用于取消操作）
const originalYamls = reactive<Record<string, string>>({})

// 添加账号对话框
const addDialogVisible = ref(false)
const addForm = reactive({
  provider: '',
  accountName: ''
})

// 获取指定云服务商的所有账号列表
const getProviderAccounts = (provider: string) => {
  const accounts = accountsData.value[provider as keyof CloudProviderOption] || {}
  return Object.keys(accounts)
}

// 验证 YAML 语法
const validateYaml = (accountName: string) => {
  try {
    if (yamlContents[accountName]?.trim()) {
      parse(yamlContents[accountName])
    }
    yamlErrors[accountName] = ''
  } catch (error: any) {
    yamlErrors[accountName] = `YAML 格式错误: ${error.message}`
  }
}

// 初始化账号的 YAML 内容
const initAccountYaml = () => {
  // 清空旧数据
  Object.keys(yamlContents).forEach(key => delete yamlContents[key])
  Object.keys(yamlErrors).forEach(key => delete yamlErrors[key])
  Object.keys(originalYamls).forEach(key => delete originalYamls[key])
  Object.keys(activeAccount).forEach(key => delete activeAccount[key])

  // 遍历所有云服务商
  for (const provider of Object.keys(accountsData.value)) {
    const accounts = accountsData.value[provider as keyof CloudProviderOption] || {}
    for (const [accountName, accountData] of Object.entries(accounts)) {
      yamlContents[accountName] = stringify(accountData)
      originalYamls[accountName] = yamlContents[accountName]
      yamlErrors[accountName] = ''
    }

    // 设置第一个账号为激活状态
    const accountNames = Object.keys(accounts)
    if (accountNames.length > 0) {
      activeAccount[provider] = accountNames[0]
    }
  }

  // 设置第一个云服务商为激活状态
  const providerKeys = Object.keys(accountsData.value)
  if (providerKeys.length > 0 && !activeProvider.value) {
    activeProvider.value = providerKeys[0]
  }
}

// 打开添加账号对话框
const handleAdd = () => {
  addForm.provider = activeProvider.value
  addForm.accountName = ''
  addDialogVisible.value = true
}

// 确认添加账号
const handleConfirmAdd = () => {
  if (!addForm.accountName.trim()) {
    ElMessage.warning('请输入账号名称')
    return
  }

  if (!/^[a-zA-Z0-9_-]+$/.test(addForm.accountName)) {
    ElMessage.error('账号名称只能包含字母、数字、下划线和横线')
    return
  }

  // 检查账号是否已存在
  const existingAccounts = getProviderAccounts(addForm.provider)
  if (existingAccounts.includes(addForm.accountName)) {
    ElMessage.error('账号名称已存在')
    return
  }

  // 初始化新账号的 YAML 内容
  yamlContents[addForm.accountName] = ''
  yamlErrors[addForm.accountName] = ''
  originalYamls[addForm.accountName] = ''

  // 添加到账号数据中（空对象）
  const accounts = accountsData.value[addForm.provider as keyof CloudProviderOption] || {}
  accounts[addForm.accountName] = {}
  accountsData.value[addForm.provider as keyof CloudProviderOption] = { ...accounts }

  // 激活新添加的账号
  activeProvider.value = addForm.provider
  activeAccount[addForm.provider] = addForm.accountName

  addDialogVisible.value = false
  ElMessage.success('账号已添加，请编辑配置后保存')
}

// 保存账号
const handleSave = async (accountName: string) => {
  if (!yamlContents[accountName]?.trim()) {
    ElMessage.warning('请输入账号配置')
    return
  }

  if (yamlErrors[accountName]) {
    ElMessage.error('YAML 格式错误，请检查后重试')
    return
  }

  try {
    const accountData = parse(yamlContents[accountName])

    // 构造完整的账号数据
    const updatedAccounts: Accounts = {
      ...accountsData.value[activeProvider.value as keyof CloudProviderOption],
      [accountName]: accountData
    }

    await updateAccount({
      [activeProvider.value]: updatedAccounts
    })

    ElMessage.success('保存成功')
    originalYamls[accountName] = yamlContents[accountName]
    await loadData()
  } catch (error: any) {
    ElMessage.error(`保存失败: ${error.message || error}`)
  }
}

// 删除账号
const handleDelete = async (accountName: string) => {
  try {
    await ElMessageBox.confirm(`确定要删除账号 "${accountName}" 吗？`, '删除确认', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    await deleteAccount(activeProvider.value, accountName)
    ElMessage.success('删除成功')

    // 清理数据
    delete yamlContents[accountName]
    delete yamlErrors[accountName]
    delete originalYamls[accountName]

    await loadData()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error(`删除失败: ${error.message || error}`)
    }
  }
}

// 通过 Tab 关闭按钮删除账号
const handleRemoveTab = (accountName: string) => {
  handleDelete(accountName)
}

// 取消编辑
const handleCancel = (accountName: string) => {
  yamlContents[accountName] = originalYamls[accountName] || ''
  yamlErrors[accountName] = ''
}

// 加载数据
const loadData = async () => {
  try {
    const { data } = await getAccounts()
    accountsData.value = data.data || { aws: {}, ali: {} }
    initAccountYaml()
  } catch (error) {
    console.error('加载账号数据失败:', error)
    ElMessage.error('加载账号数据失败')
  }
}

onMounted(() => {
  loadData()
})
</script>
