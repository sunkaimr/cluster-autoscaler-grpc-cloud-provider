# Cluster Autoscaler GRPC Provider Web UI 开发计划

## 一、项目概述

为 cluster-autoscaler-grpc-provider 项目开发一个 Web 管理界面，使用 Vue 3 + Element Plus 实现对后端 HTTP 接口的可视化操作。

## 二、现有 HTTP 接口分析

### 2.1 接口列表

| 模块 | 方法 | 路径 | 功能 |
|------|------|------|------|
| NodeGroup状态 | GET | /api/v1/nodegroup-status | 获取完整状态 |
| 云账号管理 | GET | /api/v1/cloud-provider-option/account | 查看账户列表 |
| 云账号管理 | POST | /api/v1/cloud-provider-option/account | 添加或更新账户 |
| 云账号管理 | DELETE | /api/v1/cloud-provider-option/account/:provider/:account | 删除账户 |
| Instance参数 | GET | /api/v1/cloud-provider-option/instance-parameter | 查看Instance参数 |
| Instance参数 | POST | /api/v1/cloud-provider-option/instance-parameter | 添加或更新Instance参数 |
| Instance参数 | DELETE | /api/v1/cloud-provider-option/instance-parameter/:name | 删除Instance参数 |
| NodeGroup | GET | /api/v1/nodegroup | 查看NodeGroup列表 |
| NodeGroup | POST | /api/v1/nodegroup | 更新NodeGroup |
| Instance | GET | /api/v1/nodegroup/instance | 查询Instance状态 |
| Instance | POST | /api/v1/nodegroup/instance | 更新Instance状态 |

### 2.2 核心数据结构

#### 响应格式
```json
{
  "status": 2000000,
  "msg": "",
  "error": "",
  "data": {}
}
```

#### CloudProviderOption
```typescript
interface CloudProviderOption {
  accounts: {
    [provider: string]: {
      [account: string]: {
        secretId: string;
        secretKey: string;
      }
    }
  };
  instanceParameter: {
    [name: string]: {
      providerIdTemplate: string;
      parameter: any;
    }
  };
}
```

#### NodeGroup
```typescript
interface NodeGroup {
  id: string;
  minSize: number;
  maxSize: number;
  targetSize: number;
  autoscalingOptions?: {
    scaleDownUtilizationThreshold?: number;
    scaleDownGpuUtilizationThreshold?: number;
    scaleDownUnneededTime?: number;
    scaleDownUnreadyTime?: number;
  };
  nodeTemplate?: {
    labels: Record<string, string>;
    annotations: Record<string, string>;
    capacity: Record<string, string>;
    allocatable?: Record<string, string>;
    taints?: Array<{key: string; value: string; effect: string}>;
  };
  instanceParameter: string;
  instances?: Instance[];
}
```

#### Instance
```typescript
interface Instance {
  id: string;
  name: string;
  ip: string;
  providerID: string;
  stage: 'Pending' | 'Creating' | 'Created' | 'Joined' | 'Running' | 'PendingDeletion' | 'Deleting' | 'Deleted';
  status: 'Init' | 'InProcess' | 'Success' | 'Failed' | 'Unknown';
  error?: string;
  updateTime: string;
}
```

## 三、技术栈选型

| 类别 | 技术 | 版本 |
|------|------|------|
| 框架 | Vue | 3.x |
| UI组件库 | Element Plus | 2.x |
| 构建工具 | Vite | 5.x |
| 状态管理 | Pinia | 2.x |
| 路由 | Vue Router | 4.x |
| HTTP客户端 | Axios | 1.x |
| 语言 | TypeScript | 5.x |

## 四、项目结构

```
ui/
├── public/                    # 静态资源
│   └── favicon.ico
├── src/
│   ├── api/                   # API 接口封装
│   │   ├── index.ts           # Axios 实例配置
│   │   ├── account.ts         # 云账号相关接口
│   │   ├── instanceParameter.ts # Instance参数相关接口
│   │   ├── nodegroup.ts       # NodeGroup相关接口
│   │   └── instance.ts        # Instance相关接口
│   ├── components/            # 公共组件
│   │   ├── JsonEditor.vue     # JSON编辑器组件
│   │   └── StatusTag.vue      # 状态标签组件
│   ├── views/                 # 页面视图
│   │   ├── Dashboard.vue      # 仪表盘/概览页
│   │   ├── AccountManage.vue  # 云账号管理
│   │   ├── InstanceParameter.vue # Instance参数管理
│   │   ├── NodeGroupList.vue  # NodeGroup列表
│   │   ├── NodeGroupDetail.vue # NodeGroup详情/编辑
│   │   └── InstanceList.vue   # Instance列表
│   ├── router/                # 路由配置
│   │   └── index.ts
│   ├── stores/                # Pinia 状态管理
│   │   └── app.ts
│   ├── types/                 # TypeScript 类型定义
│   │   └── index.ts
│   ├── utils/                 # 工具函数
│   │   └── index.ts
│   ├── App.vue                # 根组件
│   ├── main.ts                # 入口文件
│   └── style.css              # 全局样式
├── index.html
├── package.json
├── tsconfig.json
├── vite.config.ts
└── README.md
```

## 五、功能模块设计

### 5.1 仪表盘 (Dashboard)

- 显示系统整体状态概览
- NodeGroup 数量统计
- Instance 各状态数量统计（饼图/柱状图）
- 最近操作日志

### 5.2 云账号管理 (AccountManage)

- 账号列表展示（按 Provider 分组）
- 新增账号（表单：Provider、Account名称、SecretId、SecretKey）
- 编辑账号（更新 SecretId、SecretKey）
- 删除账号（二次确认，提示是否有关联的 InstanceParameter）

### 5.3 Instance参数管理 (InstanceParameter)

- 参数列表展示
- 新增参数（表单：名称、ProviderIdTemplate、Parameter JSON编辑）
- 编辑参数
- 删除参数（二次确认，提示是否有关联的 NodeGroup）

### 5.4 NodeGroup管理 (NodeGroup)

- NodeGroup 列表展示
  - 显示：ID、MinSize、MaxSize、TargetSize、InstanceParameter、Instance数量
- NodeGroup 详情/编辑
  - 可编辑：MinSize、MaxSize、InstanceParameter、AutoscalingOptions
  - 只读：NodeTemplate、Instances
- Instance 列表（嵌入 NodeGroup 详情页）
  - 支持按 Stage、Status 筛选
  - 显示：ID、Name、IP、ProviderID、Stage、Status、Error、UpdateTime

### 5.5 Instance管理 (InstanceList)

- 全局 Instance 列表（跨 NodeGroup）
- 多维度筛选：NodeGroup、ID、Name、IP、Stage、Status
- Instance 状态修改（仅限调试用途）

## 六、页面布局设计

采用经典后台管理布局：

```
+----------------------------------------------------------+
|                       Header                              |
+----------+-----------------------------------------------+
|          |                                               |
|          |                                               |
|  Sidebar |                  Main Content                 |
|   Menu   |                                               |
|          |                                               |
|          |                                               |
+----------+-----------------------------------------------+
```

### 侧边栏菜单结构

```
- Dashboard (仪表盘)
- 云账号管理
- Instance参数管理
- NodeGroup管理
  - NodeGroup列表
- Instance管理
```

## 七、开发计划

### 阶段一：项目初始化
1. 使用 Vite 创建 Vue 3 + TypeScript 项目
2. 安装依赖：Element Plus、Vue Router、Pinia、Axios
3. 配置项目结构和基础文件
4. 配置 Vite 代理解决开发环境跨域问题

### 阶段二：基础框架搭建
1. 实现布局组件（Header、Sidebar、Main）
2. 配置路由
3. 封装 Axios 请求（统一错误处理、响应拦截）
4. 定义 TypeScript 类型

### 阶段三：功能页面开发
1. Dashboard 仪表盘
   - 显示nodegroup的总数，以及每个nodegroup中node的个数
   - 显示各个状态的instance的个数
2. 云账号管理（增删改查）
   - 可以考虑tabs组件显示不同的云账号
   - 云账号的显示和修改通过yaml格式的文本编辑器展示，并支持语法校验
3. Instance参数管理（增删改查）
   - 按nodegroup分组展示instances信息，比如相同nodegroup的instance放在同一个table中
   - 支持按状态，ip，id等方式筛选instance
   - 支持修改instance的stage和status
4. NodeGroup 列表与详情
   - nodegroup支持修改minSize、maxSize、autoscalingOptions这3个参数

### 阶段四：优化完善
1. 错误处理与用户提示
2. 加载状态处理
3. 表单校验
4. 响应式适配
5. 代码优化与测试

## 八、后端集成说明

### 开发环境

在 `vite.config.ts` 中配置代理：

```typescript
export default defineConfig({
  server: {
    proxy: {
      '/api': {
        target: 'http://localhost:8080', // 后端服务地址
        changeOrigin: true,
      }
    }
  }
})
```

### 生产环境

可选方案：
1. **静态文件嵌入**：将构建产物嵌入 Go 二进制文件，通过 Echo 提供静态文件服务
2. **独立部署**：前后端分离部署，通过 Nginx 反向代理

## 九、注意事项

1. 删除操作需要二次确认，并检查关联关系
5. 需要处理好接口错误状态码的映射和用户友好提示

## 十、确认事项

请确认以下问题后再开始开发：

1. **后端服务地址**：开发环境后端服务的地址和端口？（默认假设 `http://localhost:8080`）
2. **部署方式**：生产环境采用静态文件嵌入
3. **权限控制**：不需要鉴权

---

如果以上计划没有问题，请确认后我将开始代码开发。
