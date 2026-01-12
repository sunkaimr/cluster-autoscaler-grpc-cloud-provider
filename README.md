# Cluster Autoscaler gRPC Cloud Provider

### 项目简介

**cluster-autoscaler-grpc-cloud-provider** 是一个基于 gRPC 的 Kubernetes 集群自动伸缩（Cluster Autoscaler）云服务提供商实现。它通过 gRPC 接口与 Kubernetes Cluster Autoscaler 集成，实现节点的自动扩缩容管理，目前已支持腾讯云，并提供可扩展的架构以支持其他云平台。

### 核心特性

- **gRPC 服务架构**：实现 Kubernetes CloudProvider 接口，通过 gRPC 与 Cluster Autoscaler 通信
- **多云平台支持**：可扩展架构，当前支持腾讯云，易于集成其他云平台
- **完整的实例生命周期管理**：8 阶段状态机管理实例从创建到删除的全生命周期
- **并发控制**：可配置的并行创建/删除机制，提高扩缩容效率
- **Hook 机制**：支持通过 SSH 执行自定义脚本，灵活处理节点初始化和清理
- **高可用设计**：基于 Kubernetes Leader Election 实现主备切换
- **状态持久化**：使用 ConfigMap 存储节点组状态，支持热加载配置
- **HTTP 管理接口**：提供 REST API 用于运行时配置管理
- **Prometheus 监控**：内置 Metrics 端点，支持监控集成
- **智能节点匹配**：基于标签、注解和污点的节点组匹配算法

### 系统架构

#### 三层服务架构

```
┌─────────────────────────────────────────────────────────┐
│                  Cluster Autoscaler                      │
│              (Kubernetes 原生组件)                        │
└─────────────────────┬───────────────────────────────────┘
                      │ gRPC :8086
                      ↓
┌─────────────────────────────────────────────────────────┐
│     cluster-autoscaler-grpc-cloud-provider              │
├─────────────────────────────────────────────────────────┤
│  • gRPC Server (8086)    - CloudProvider 接口实现        │
│  • HTTP Server (8080)    - 管理 REST API                │
│  • Metrics Server (8087) - Prometheus 指标              │
└─────────────────────┬───────────────────────────────────┘
                      │
        ┌─────────────┼─────────────┐
        ↓             ↓             ↓
┌──────────────┐ ┌──────────┐ ┌────────────┐
│ 云厂商 API    │ │ SSH Hook │ │ Kubernetes │
│ (腾讯云 CVM)  │ │  执行引擎 │ │   API      │
└──────────────┘ └──────────┘ └────────────┘
```

#### 实例生命周期状态机

```
Pending → Creating → Created → Joined → Running
                                           ↓
               Deleted ← Deleting ← PendingDeletion
```

**状态说明：**
- **Pending**: 实例记录已创建，等待云平台创建实例
- **Creating**: 已调用云平台 API，等待实例启动
- **Created**: 实例运行中，等待执行 SSH Hook
- **Joined**: after_created_hook.sh 执行完成，等待加入 Kubernetes
- **Running**: 节点已加入集群，正常运行
- **PendingDeletion**: 标记删除，等待执行 before_delete_hook.sh
- **Deleting**: Hook 执行完成，调用云平台删除 API
- **Deleted**: 实例已删除，记录保留 15 天后清理

### 核心组件

#### 1. gRPC 服务层 (wrapper/)
实现 Kubernetes CloudProvider 接口的 9 个核心方法：
- `NodeGroups()` - 获取所有节点组
- `NodeGroupForNode()` - 查找节点所属节点组
- `NodeGroupIncreaseSize()` - 扩容节点组
- `NodeGroupDeleteNodes()` - 删除指定节点
- `NodeGroupDecreaseTargetSize()` - 缩减目标大小
- `NodeGroupNodes()` - 获取节点组中的节点
- `NodeGroupTemplateNodeInfo()` - 获取节点模板信息
- `NodeGroupGetOptions()` - 获取节点组配置选项
- `Refresh()` - 刷新云提供商缓存

#### 2. 节点组管理引擎 (nodegroup/)
- **node_group.go** (2195 行)：节点组生命周期管理核心
- **node_controller.go**：监听 Kubernetes 节点事件，同步节点到节点组
- **状态同步**：定期将状态持久化到 ConfigMap `nodegroup-status`
- **队列处理**：基于队列的并发实例创建/删除机制

#### 3. 云平台适配层 (provider)
- **接口抽象**：定义统一的云平台接口
- **腾讯云实现**：完整的腾讯云 CVM 操作实现
- **可扩展设计**：易于添加其他云平台支持（阿里云、AWS 等）

#### 4. HTTP 管理服务 (server/)
提供 REST API 用于运行时管理：
- `GET/POST /v1/cloud-provider-option/account` - 管理云账户
- `GET/POST /v1/cloud-provider-option/instance-parameter` - 管理实例模板
- `GET/POST /v1/nodegroup` - 管理节点组
- `GET/POST /v1/nodegroup/instance` - 管理实例
- `GET /v1/nodegroup-status` - 查看当前状态

### 技术栈

- **语言**: Go 1.23.0+
- **Kubernetes**: v0.25.0 client-go
- **gRPC**: Google gRPC
- **HTTP 框架**: Echo v4
- **云 SDK**: 腾讯云 SDK
- **SSH**: golang.org/x/crypto
- **监控**: Prometheus client
- **配置**: Viper + YAML

### 快速开始

#### 前置条件

- Kubernetes 集群 1.25+
- Go 1.23.0+（仅开发需要）
- 云平台账户（当前支持腾讯云）
- SSH 访问节点的凭据

#### 安装步骤

##### 1. 克隆项目

```bash
git clone https://github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider.git
cd cluster-autoscaler-grpc-cloud-provider
```

##### 2. 构建二进制文件

```bash
make build
```

或者构建 Docker 镜像：

```bash
make docker-build
```

##### 3. 配置文件准备

编辑 `nodegroup-config.yaml` 配置云账户和节点组：

```yaml
cloudProviderOption:
  tencentcloud:
    your-account-name:
      secretId: "YOUR_SECRET_ID"
      secretKey: "YOUR_SECRET_KEY"
  instanceParameter:
    template-name:
      providerIdTemplate: "externalgrpc://tencentcloud/your-account-name/region/{{InstanceID}}"
      parameter:
        InstanceChargeType: "PREPAID"
        Placement:
          Zone: "ap-beijing-5"
        InstanceType: "S5.2XLARGE16"
        ImageId: "img-xxx"
        # ... 更多参数见配置文件

nodeGroups:
  - id: "worker-group"
    minSize: 1
    maxSize: 10
    targetSize: 2
    instanceParameter: "template-name"
    nodeTemplate:
      labels:
        node-role.kubernetes.io/worker: ""
      capacity:
        cpu: "8"
        memory: "16G"
```

##### 4. 准备 Hook 脚本

`hooks/after_created_hook.sh` - 节点创建后执行：

```bash
#!/bin/bash
# 可用环境变量：NODE_NAME, NODE_IP, PROVIDER_ID

# 设置主机名
hostnamectl set-hostname ${NODE_NAME}

# 加入 Kubernetes 集群
kubeadm join <api-server>:6443 --token <token> \
    --discovery-token-ca-cert-hash sha256:<hash>
```

`hooks/before_delete_hook.sh` - 节点删除前执行：

```bash
#!/bin/bash
# 驱逐 Pod
kubectl drain ${NODE_NAME} --ignore-daemonsets --delete-emptydir-data

# 从集群移除
kubeadm reset -f
```

##### 5. 部署到 Kubernetes

###### 5.1 创建 SSH 凭据 Secret

```bash
kubectl create secret generic kube-node-ssh-auth \
  -n kube-system \
  --from-literal=KUBE_NODE_SSH_USER=root \
  --from-literal=KUBE_NODE_SSH_PASSWD=your-password
```

###### 5.2 创建配置 ConfigMap

```bash
kubectl create configmap cluster-autoscaler-grpc-cloud-provider-cm \
  -n kube-system \
  --from-file=cloud-config=cloud-config.cfg \
  --from-file=nodegroup-config=nodegroup-config.yaml \
  --from-file=after_created_hook.sh=hooks/after_created_hook.sh \
  --from-file=before_delete_hook.sh=hooks/before_delete_hook.sh
```

###### 5.3 部署 gRPC Cloud Provider

```bash
kubectl apply -f deploy/cluster-autoscaler-grpc-cloud-provider.yaml
```

###### 5.4 部署 Cluster Autoscaler

```bash
kubectl apply -f deploy/cluster-autoscaler.yaml
```

##### 6. 验证部署

```bash
# 检查 Pod 状态
kubectl get pods -n kube-system -l app=cluster-autoscaler-grpc-cloud-provider
kubectl get pods -n kube-system -l app=cluster-autoscaler

# 查看日志
kubectl logs -n kube-system -l app=cluster-autoscaler-grpc-cloud-provider -f
kubectl logs -n kube-system -l app=cluster-autoscaler -f

# 查看节点组状态
kubectl get cm nodegroup-status -n kube-system -o yaml
```

### 配置说明

#### 命令行参数

```bash
--http-address=":8080"           # HTTP API 监听地址
--grpc-address=":8086"           # gRPC 服务监听地址
--metrics-address=":8087"        # Prometheus 指标地址
--metrics-path="/metrics"        # 指标路径
--cloud-provider="externalgrpc"  # 云提供商类型（固定）
--cloud-config="cloud-config.cfg" # 云配置文件路径
--kubeconfig=""                  # Kubernetes 配置文件
--namespace="kube-system"        # 运行命名空间
--nodegroup-status-cm="nodegroup-status" # 状态 ConfigMap 名称
--nodegroup-config="nodegroup-config.yaml" # 节点组配置文件
--hooks-path="./hooks"           # Hook 脚本目录
--create-parallelism=5           # 最大并发创建数
--delete-parallelism=1           # 最大并发删除数
--leader-elect=true              # 启用 Leader Election
--leader-elect-lease-duration=15s # Leader 租约时长
```

#### 节点组配置详解

```yaml
nodeGroups:
  - id: "unique-id"              # 节点组唯一标识
    minSize: 1                   # 最小节点数
    maxSize: 10                  # 最大节点数
    targetSize: 2                # 目标节点数
    instanceParameter: "template-name" # 实例模板引用
    autoscalingOptions:          # 自动伸缩选项（可选）
      scaleDownUtilizationThreshold: 0.5      # 缩容资源利用率阈值
      scaleDownGpuUtilizationThreshold: 0.5   # GPU 利用率阈值
      scaleDownUnneededTime: "40m"            # 节点空闲多久后可缩容
      scaleDownUnreadyTime: "10m"             # 节点未就绪多久后可缩容
    nodeTemplate:                # 节点模板
      labels:                    # 节点标签
        node-role.kubernetes.io/worker: ""
        custom-label: "value"
      annotations:               # 节点注解
        custom-annotation: "value"
      capacity:                  # 节点容量
        cpu: "8"
        memory: "16G"
        ephemeral-storage: "200G"
      allocatable:               # 可分配资源
        cpu: "7.5"
        memory: "15G"
        ephemeral-storage: "180G"
      taints:                    # 节点污点
        - key: "dedicated"
          value: "worker"
          effect: "NoSchedule"
```

### 运维指南

#### 日志查看

```bash
# gRPC Provider 日志
kubectl logs -n kube-system deploy/cluster-autoscaler-grpc-cloud-provider -f

# Cluster Autoscaler 日志
kubectl logs -n kube-system deploy/cluster-autoscaler -f
```

#### 调整日志级别

```bash
# 修改部署，添加 --v=5（1-10，数字越大越详细）
kubectl edit deploy cluster-autoscaler-grpc-cloud-provider -n kube-system
```

#### 查看节点组状态

```bash
# 通过 ConfigMap 查看
kubectl get cm nodegroup-status -n kube-system -o yaml

# 通过 HTTP API 查看
kubectl port-forward -n kube-system svc/cluster-autoscaler-grpc-cloud-provider 8080:8080
curl http://localhost:8080/v1/nodegroup-status | jq
```

#### 动态调整节点组

```bash
# 通过 HTTP API 修改节点组
curl -X POST http://localhost:8080/v1/nodegroup \
  -H "Content-Type: application/json" \
  -d '{
    "id": "worker-group",
    "minSize": 2,
    "maxSize": 20,
    "targetSize": 5
  }'
```

#### 监控集成

Prometheus 抓取配置：

```yaml
- job_name: 'cluster-autoscaler-grpc-provider'
  kubernetes_sd_configs:
    - role: pod
      namespaces:
        names:
          - kube-system
  relabel_configs:
    - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape]
      action: keep
      regex: true
    - source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_port]
      action: replace
      target_label: __address__
      regex: ([^:]+)(?::\d+)?;(\d+)
      replacement: $1:$2
```

### 故障排查

#### 实例卡在 Creating 状态

**可能原因：**
- 云平台配额不足
- 云平台 API 调用失败
- 网络问题

**排查方法：**
```bash
# 查看详细日志
kubectl logs -n kube-system deploy/cluster-autoscaler-grpc-cloud-provider --tail=100

# 检查云平台配额和账户状态
# 检查节点组状态
kubectl get cm nodegroup-status -n kube-system -o yaml
```

#### 节点创建后未加入集群

**可能原因：**
- after_created_hook.sh 执行失败
- SSH 连接问题
- kubeadm join 失败

**排查方法：**
```bash
# 查看 SSH 日志
kubectl logs -n kube-system deploy/cluster-autoscaler-grpc-cloud-provider | grep SSH

# 手动登录节点检查
# 检查 Hook 脚本内容是否正确
kubectl get cm cluster-autoscaler-grpc-cloud-provider-cm -n kube-system -o yaml
```

#### Leader Election 失败

**可能原因：**
- RBAC 权限不足
- 网络问题

**排查方法：**
```bash
# 检查 Lease 对象
kubectl get lease cluster-autoscaler-grpc-cloud-provider -n kube-system

# 检查 RBAC
kubectl auth can-i create leases --as=system:serviceaccount:kube-system:cluster-autoscaler-grpc-cloud-provider -n kube-system
```

### 开发指南

#### 本地开发运行

```bash
# 设置 Kubeconfig
export KUBECONFIG=/path/to/kubeconfig

# 运行
go run main.go \
  --kubeconfig=$KUBECONFIG \
  --nodegroup-config=nodegroup-config.yaml \
  --hooks-path=./hooks \
  --v=5
```

#### 代码结构

```
.
├── deploy/                 # Kubernetes 部署文件
├── hooks/                  # Hook 脚本示例
├── nodegroup/             # 节点组管理核心
│   ├── node_group.go      # 节点组引擎
│   ├── node_controller.go # 节点监控器
│   └── metrics/           # Prometheus 指标
├── provider/              # 云平台适配层
│   ├── interface.go       # 云平台接口定义
│   └── tencentcloud/      # 腾讯云实现
├── server/                # HTTP 管理服务
├── wrapper/               # gRPC 接口实现
├── pkg/                   # 公共工具库
├── main.go                # 程序入口
└── README.md             # 本文档
```

#### 添加新云平台支持

1. 在 `provider/` 下创建新的云平台目录
2. 实现 `CloudProvider` 接口
3. 在 `nodegroup/node_group.go` 中注册新提供商
4. 更新配置文件格式

#### 运行测试

```bash
# 运行所有测试
go test ./...

# 运行特定包测试
go test ./nodegroup -v

# 代码格式化
make fmt

# 代码静态检查
make vet
```

### 性能调优

#### 并发控制

根据云平台 API 限流调整并发参数：

```bash
--create-parallelism=10  # 创建实例并发数
--delete-parallelism=5   # 删除实例并发数
```

#### 状态同步频率

修改代码中的同步间隔（默认 30 秒）：

```go
// nodegroup/node_group.go
const syncInterval = 30 * time.Second
```

### 安全建议

1. **密钥管理**：使用 Kubernetes Secret 存储云平台密钥，不要硬编码
2. **SSH 凭据**：定期轮换 SSH 密码，考虑使用密钥认证
3. **网络隔离**：限制 gRPC 服务仅集群内部访问
4. **RBAC 最小权限**：仅授予必要的 Kubernetes 权限
5. **审计日志**：启用操作审计日志

### 常见问题

**Q: 支持哪些云平台？**
A: 当前支持腾讯云，架构上支持扩展至任何云平台。

**Q: 可以同时管理多个云平台的节点吗？**
A: 可以，通过配置多个云账户和节点组实现。

**Q: Hook 脚本执行失败会怎样？**
A: 实例会卡在对应状态，不会继续流转，需要手动干预或重试。

**Q: 如何实现跨可用区扩容？**
A: 创建多个节点组，每个节点组对应不同的可用区。

**Q: 支持 Spot 实例吗？**
A: 支持，在 instanceParameter 中配置相应的实例计费类型。

### 许可证

本项目采用 MIT 许可证 - 详见 [LICENSE](LICENSE) 文件。
