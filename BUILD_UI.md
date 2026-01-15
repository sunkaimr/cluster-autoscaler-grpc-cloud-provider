# UI集成构建指南

本项目已将Vue.js管理UI集成到Go服务器中,UI文件会被嵌入到二进制文件中。

## 快速开始

### Windows系统
```bash
.\build-with-ui.bat
```

### Linux/Mac系统
```bash
chmod +x build-with-ui.sh
./build-with-ui.sh
```

## 手动构建步骤

如果需要手动构建,请按以下步骤操作:

### 1. 编译UI项目
```bash
cd ui
npm install  # 首次构建需要安装依赖
npm run build
cd ..
```

### 2. 复制UI文件到server目录
```bash
# Linux/Mac
mkdir -p server/ui
cp -r ui/dist server/ui/

# Windows
mkdir server\ui
xcopy /E /I /Y ui\dist server\ui\dist
```

### 3. 编译Go项目
```bash
# 简单编译
go build -o cluster-autoscaler-grpc-provider main.go

# 或使用Makefile (带版本信息)
make build
```

## 工作原理

### 代码修改说明

在 `server/server.go` 中:

1. **导入embed包**:
```go
import (
    "embed"
    "io/fs"
    // ...
)
```

2. **嵌入UI文件**:
```go
//go:embed ui/dist
var embeddedFiles embed.FS
```

3. **提供静态文件服务**:
```go
// Serve embedded UI files
uiFS, err := fs.Sub(embeddedFiles, "ui/dist")
if err != nil {
    klog.Errorf("failed to get embedded UI files: %s", err)
} else {
    e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(uiFS))))
}
```

### 路由说明

- **UI界面**: `http://localhost:port/` (根路径)
- **API接口**: `http://localhost:port/api/v1/*`

API接口会优先匹配,因为它们在代码中注册在UI路由之后。

## 开发模式

开发UI时可以分别启动前后端:

### 启动后端服务
```bash
go run main.go
```

### 启动前端开发服务器
```bash
cd ui
npm run dev
```

前端开发服务器会通过vite的proxy配置自动代理API请求到后端。

## 注意事项

1. **内存要求**: 由于项目依赖较多,编译需要较大内存(建议4GB以上可用内存)
2. **构建时间**: 完整构建可能需要几分钟时间
3. **UI更新**: 每次修改UI后需要重新运行 `npm run build` 并重新编译Go项目
4. **embed路径**: Go的embed指令路径是相对于包所在目录,因此UI文件需要复制到 `server/ui/dist`

## 故障排除

### 编译内存不足
如果遇到 "out of memory" 错误:
- 关闭其他应用程序释放内存
- 在内存更大的机器上编译
- 使用Docker构建

### UI文件未嵌入
确保:
1. `server/ui/dist` 目录存在且包含文件
2. embed指令格式正确: `//go:embed ui/dist`
3. 重新编译整个项目

### 访问UI显示404
检查:
1. 服务器日志中是否有embed相关错误
2. 确认访问的端口和地址正确
3. 检查是否有防火墙阻止
