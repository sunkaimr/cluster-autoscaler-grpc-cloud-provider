<template>
  <div class="page-container">
    <div style="display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px">
      <h2>云账号管理</h2>
      <div style="display: flex; gap: 10px">
        <el-button type="success" @click="handleAddProvider">添加云服务商</el-button>
        <el-button type="primary" @click="handleAddAccount">添加账号</el-button>
      </div>
    </div>

    <!-- 外层 Tabs: 云服务商 -->
    <el-tabs v-model="activeProvider" type="border-card" closable @tab-remove="handleRemoveProvider">
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
          @tab-remove="handleRemoveAccount"
        >
          <el-tab-pane
            v-for="accountName in getProviderAccounts(provider.value)"
            :key="accountName"
            :label="accountName"
            :name="accountName"
          >
            <div style="padding: 20px">
              <el-alert
                v-if="yamlErrors[getAccountKey(provider.value, accountName)]"
                :title="yamlErrors[getAccountKey(provider.value, accountName)]"
                type="error"
                :closable="false"
                style="margin-bottom: 15px"
              />

              <el-input
                v-model="yamlContents[getAccountKey(provider.value, accountName)]"
                type="textarea"
                :rows="20"
                placeholder="请输入 YAML 格式的账号配置"
                style="font-family: 'Courier New', monospace; font-size: 13px; margin-bottom: 15px"
                @input="validateYaml(getAccountKey(provider.value, accountName))"
              />

              <div style="display: flex; gap: 10px">
                <el-button
                  type="primary"
                  :disabled="!!yamlErrors[getAccountKey(provider.value, accountName)]"
                  @click="handleSave(provider.value, accountName)"
                >
                  保存
                </el-button>
                <el-button @click="handleCancel(provider.value, accountName)">取消</el-button>
                <el-button type="danger" @click="handleDeleteAccount(provider.value, accountName)">删除账号</el-button>
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

      <el-empty
        v-if="providers.length === 0"
        description="暂无云服务商，请添加"
        style="padding: 60px 0"
      />
    </el-tabs>

    <!-- 添加云服务商对话框 -->
    <el-dialog v-model="addProviderDialogVisible" title="添加云服务商" width="500px">
      <el-form :model="addProviderForm" label-width="120px">
        <el-form-item label="云服务商名称">
          <el-input
            v-model="addProviderForm.providerName"
            placeholder="请输入云服务商名称（如: aws, ali, tencentcloud）"
          />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="addProviderDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleConfirmAddProvider">确定</el-button>
      </template>
    </el-dialog>

    <!-- 添加账号对话框 -->
    <el-dialog v-model="addAccountDialogVisible" title="添加账号" width="500px">
      <el-form :model="addAccountForm" label-width="100px">
        <el-form-item label="云服务商">
          <el-select v-model="addAccountForm.provider" placeholder="请选择云服务商" style="width: 100%">
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
            v-model="addAccountForm.accountName"
            placeholder="请输入账号名称（字母、数字、下划线、横线）"
          />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="addAccountDialogVisible = false">取消</el-button>
        <el-button type="primary" @click="handleConfirmAddAccount">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, reactive, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { parse, stringify } from 'yaml'
import { getAccounts, updateAccount, deleteAccount, deleteProvider } from '@/api/account'
import type { CloudProviderOption, Accounts } from '@/types'

const activeProvider = ref<string>('')
const accountsData = ref<CloudProviderOption>({})

// 动态获取云服务商列表（直接使用后端返回的 key）
const providers = computed(() => {
  return Object.keys(accountsData.value).map((key) => ({
    label: key,
    value: key
  }))
})

// 每个云服务商当前激活的账号 Tab
const activeAccount = reactive<Record<string, string>>({})

// 每个账号的 YAML 内容（使用 provider:account 作为 key）
const yamlContents = reactive<Record<string, string>>({})
// 每个账号的 YAML 错误信息
const yamlErrors = reactive<Record<string, string>>({})
// 每个账号的原始 YAML 内容（用于取消操作）
const originalYamls = reactive<Record<string, string>>({})

// 添加云服务商对话框
const addProviderDialogVisible = ref(false)
const addProviderForm = reactive({
  providerName: ''
})

// 添加账号对话框
const addAccountDialogVisible = ref(false)
const addAccountForm = reactive({
  provider: '',
  accountName: ''
})

// 生成账号的唯一 key（provider:account）
const getAccountKey = (provider: string, accountName: string) => {
  return `${provider}:${accountName}`
}

// 获取指定云服务商的所有账号列表
const getProviderAccounts = (provider: string) => {
  const accounts = accountsData.value[provider as keyof CloudProviderOption] || {}
  return Object.keys(accounts)
}

// 验证 YAML 语法
const validateYaml = (accountKey: string) => {
  try {
    if (yamlContents[accountKey]?.trim()) {
      parse(yamlContents[accountKey])
    }
    yamlErrors[accountKey] = ''
  } catch (error: any) {
    yamlErrors[accountKey] = `YAML 格式错误: ${error.message}`
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
      const accountKey = getAccountKey(provider, accountName)
      yamlContents[accountKey] = stringify(accountData)
      originalYamls[accountKey] = yamlContents[accountKey]
      yamlErrors[accountKey] = ''
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

// 打开添加云服务商对话框
const handleAddProvider = () => {
  addProviderForm.providerName = ''
  addProviderDialogVisible.value = true
}

// 确认添加云服务商
const handleConfirmAddProvider = async () => {
  if (!addProviderForm.providerName.trim()) {
    ElMessage.warning('请输入云服务商名称')
    return
  }

  if (!/^[a-zA-Z0-9_-]+$/.test(addProviderForm.providerName)) {
    ElMessage.error('云服务商名称只能包含字母、数字、下划线和横线')
    return
  }

  // 检查云服务商是否已存在
  if (accountsData.value[addProviderForm.providerName as keyof CloudProviderOption]) {
    ElMessage.error('云服务商已存在')
    return
  }

  try {
    // 添加空的云服务商
    await updateAccount({
      [addProviderForm.providerName]: {}
    })

    ElMessage.success('云服务商已添加')
    addProviderDialogVisible.value = false
    await loadData()

    // 激活新添加的云服务商
    activeProvider.value = addProviderForm.providerName
  } catch (error: any) {
    ElMessage.error(`添加失败: ${error.message || error}`)
  }
}

// 删除云服务商
const handleRemoveProvider = async (providerName: string) => {
  try {
    await ElMessageBox.confirm(
      `确定要删除云服务商 "${providerName}" 及其下所有账号吗？`,
      '删除确认',
      {
        confirmButtonText: '确定',
        cancelButtonText: '取消',
        type: 'warning'
      }
    )

    await deleteProvider(providerName)
    ElMessage.success('删除成功')
    await loadData()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error(`删除失败: ${error.message || error}`)
    }
  }
}

// 打开添加账号对话框
const handleAddAccount = () => {
  addAccountForm.provider = activeProvider.value
  addAccountForm.accountName = ''
  addAccountDialogVisible.value = true
}

// 确认添加账号
const handleConfirmAddAccount = () => {
  if (!addAccountForm.provider) {
    ElMessage.warning('请选择云服务商')
    return
  }

  if (!addAccountForm.accountName.trim()) {
    ElMessage.warning('请输入账号名称')
    return
  }

  if (!/^[a-zA-Z0-9_-]+$/.test(addAccountForm.accountName)) {
    ElMessage.error('账号名称只能包含字母、数字、下划线和横线')
    return
  }

  // 检查账号是否已存在
  const existingAccounts = getProviderAccounts(addAccountForm.provider)
  if (existingAccounts.includes(addAccountForm.accountName)) {
    ElMessage.error('账号名称已存在')
    return
  }

  // 初始化新账号的 YAML 内容
  const accountKey = getAccountKey(addAccountForm.provider, addAccountForm.accountName)
  yamlContents[accountKey] = ''
  yamlErrors[accountKey] = ''
  originalYamls[accountKey] = ''

  // 添加到账号数据中（空对象）
  const accounts = accountsData.value[addAccountForm.provider as keyof CloudProviderOption] || {}
  accounts[addAccountForm.accountName] = {}
  accountsData.value[addAccountForm.provider as keyof CloudProviderOption] = { ...accounts }

  // 激活新添加的账号
  activeProvider.value = addAccountForm.provider
  activeAccount[addAccountForm.provider] = addAccountForm.accountName

  addAccountDialogVisible.value = false
  ElMessage.success('账号已添加，请编辑配置后保存')
}

// 保存账号
const handleSave = async (provider: string, accountName: string) => {
  const accountKey = getAccountKey(provider, accountName)

  if (!yamlContents[accountKey]?.trim()) {
    ElMessage.warning('请输入账号配置')
    return
  }

  if (yamlErrors[accountKey]) {
    ElMessage.error('YAML 格式错误，请检查后重试')
    return
  }

  try {
    const accountData = parse(yamlContents[accountKey])

    // 构造完整的账号数据
    const updatedAccounts: Accounts = {
      ...accountsData.value[provider as keyof CloudProviderOption],
      [accountName]: accountData
    }

    await updateAccount({
      [provider]: updatedAccounts
    })

    ElMessage.success('保存成功')
    originalYamls[accountKey] = yamlContents[accountKey]
    await loadData()
  } catch (error: any) {
    ElMessage.error(`保存失败: ${error.message || error}`)
  }
}

// 删除账号
const handleDeleteAccount = async (provider: string, accountName: string) => {
  try {
    await ElMessageBox.confirm(`确定要删除账号 "${accountName}" 吗？`, '删除确认', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning'
    })

    await deleteAccount(provider, accountName)
    ElMessage.success('删除成功')

    // 清理数据
    const accountKey = getAccountKey(provider, accountName)
    delete yamlContents[accountKey]
    delete yamlErrors[accountKey]
    delete originalYamls[accountKey]

    await loadData()
  } catch (error: any) {
    if (error !== 'cancel') {
      ElMessage.error(`删除失败: ${error.message || error}`)
    }
  }
}

// 通过 Tab 关闭按钮删除账号
const handleRemoveAccount = (accountName: string) => {
  handleDeleteAccount(activeProvider.value, accountName)
}

// 取消编辑
const handleCancel = (provider: string, accountName: string) => {
  const accountKey = getAccountKey(provider, accountName)
  yamlContents[accountKey] = originalYamls[accountKey] || ''
  yamlErrors[accountKey] = ''
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
