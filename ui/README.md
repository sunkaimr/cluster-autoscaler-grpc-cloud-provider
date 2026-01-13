# Cluster Autoscaler UI

基于 Vue 3 + TypeScript + Element Plus 的 Cluster Autoscaler 管理界面。

## 功能特性

- **仪表盘**: 展示 NodeGroup 和 Instance 的统计信息
- **云账号管理**: 支持 AWS 和阿里云账号的 YAML 配置管理
- **Instance 参数管理**: 管理 Instance 的参数配置
- **NodeGroup 管理**: 编辑 NodeGroup 的大小配置和 AutoscalingOptions
- **Instance 管理**: 查看和管理所有 Instance，支持按多种条件筛选

## 技术栈

- Vue 3
- TypeScript
- Vite
- Element Plus
- Vue Router
- Pinia
- Axios
- YAML

## 开发环境设置

### 前置要求

- Node.js >= 16
- npm 或 yarn

### 安装依赖

```bash
cd ui
npm install
```

### 启动开发服务器

```bash
npm run dev
```

开发服务器将在 `http://localhost:3000` 启动。

API 请求会自动代理到 `http://localhost:8080`，请确保后端服务已启动。

### 构建生产版本

```bash
npm run build
```

构建产物将输出到 `dist` 目录。

### 类型检查

```bash
npm run type-check
```

## 项目结构

```
ui/
├── public/              # 静态资源
├── src/
│   ├── api/            # API 接口
│   │   ├── index.ts
│   │   ├── account.ts
│   │   ├── instance.ts
│   │   ├── instanceParameter.ts
│   │   └── nodegroup.ts
│   ├── components/     # 公共组件
│   │   └── StatusTag.vue
│   ├── router/         # 路由配置
│   ├── stores/         # Pinia 状态管理
│   ├── types/          # TypeScript 类型定义
│   ├── utils/          # 工具函数
│   ├── views/          # 页面组件
│   │   ├── Dashboard.vue
│   │   ├── AccountManage.vue
│   │   ├── InstanceParameter.vue
│   │   ├── NodeGroupList.vue
│   │   └── InstanceList.vue
│   ├── App.vue
│   ├── main.ts
│   └── style.css
├── index.html
├── package.json
├── tsconfig.json
└── vite.config.ts
```

## API 接口

所有 API 请求的 baseURL 为 `/api/v1`，与后端服务的接口对应。

### 接口列表

- `GET /nodegroup-status` - 获取 NodeGroup 状态
- `GET /accounts` - 获取云账号配置
- `POST /accounts` - 更新云账号配置
- `DELETE /accounts` - 删除云账号
- `GET /instance-parameters` - 获取 Instance 参数
- `POST /instance-parameters` - 更新 Instance 参数
- `DELETE /instance-parameters` - 删除 Instance 参数
- `GET /nodegroups` - 获取 NodeGroup 列表
- `POST /nodegroups` - 更新 NodeGroup
- `GET /instances` - 获取 Instance 列表
- `POST /instances` - 更新 Instance 状态

## 嵌入 Go 二进制

UI 构建完成后，可以使用 Go 的 embed 功能将静态文件嵌入到二进制文件中。

在 Go 代码中添加：

```go
//go:embed ui/dist/*
var staticFiles embed.FS

// 在路由中添加
e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(staticFiles))))
```

## 开发说明

### YAML 编辑器

云账号管理和 Instance 参数管理页面使用了 YAML 格式的文本编辑器，支持：

- 实时语法验证
- 错误提示
- 自动格式化

### 状态管理

使用 Pinia 管理全局状态，目前主要用于侧边栏折叠状态的管理。

### 路由配置

使用 Vue Router 进行页面路由管理，所有路由都配置了懒加载以优化性能。

## 许可证

与主项目保持一致
