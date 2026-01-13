# UI 静态文件嵌入 Go 二进制指南

本文档说明如何将构建好的 UI 静态文件嵌入到 Go 二进制文件中。

## 步骤 1: 构建 UI

首先需要构建 UI 生成静态文件：

```bash
cd ui
npm install
npm run build
```

构建完成后，静态文件将生成在 `ui/dist` 目录下。

## 步骤 2: 修改 server.go

在 `server/server.go` 文件中添加以下内容：

### 2.1 导入 embed 包

在文件顶部的 import 部分添加：

```go
import (
	"embed"
	"io/fs"
	"net/http"
	// ... 其他导入
)
```

### 2.2 添加 embed 指令和变量

在 package 声明后，添加：

```go
//go:embed all:../ui/dist
var uiFS embed.FS
```

### 2.3 添加静态文件服务路由

在 `HttpServer` 函数中，在现有的 API 路由之后，添加静态文件服务：

```go
func HttpServer(ctx context.Context, addr string) {
	e := echo.New()
	e.HidePort = true
	e.HideBanner = true
	e.Use(
		middleware.Recover(),
		middleware.LoggerWithConfig(middleware.LoggerConfig{Output: io.Discard}),
		addLogger,
	)

	// API 路由
	v1 := e.Group("/api/v1")
	// ... 现有的 API 路由 ...

	// 静态文件服务
	distFS, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		klog.Fatalf("failed to create sub filesystem: %v", err)
	}

	// 处理 SPA 路由
	e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(distFS))))
	e.File("/", "ui/dist/index.html")

	// ... 其余代码保持不变 ...
}
```

### 完整示例

修改后的 `HttpServer` 函数应该类似这样：

```go
package server

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	// ... 其他导入
)

//go:embed all:../ui/dist
var uiFS embed.FS

type Response struct {
	ServiceCode
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

// ... 其他代码 ...

func HttpServer(ctx context.Context, addr string) {
	e := echo.New()
	e.HidePort = true
	e.HideBanner = true
	e.Use(
		middleware.Recover(),
		middleware.LoggerWithConfig(middleware.LoggerConfig{Output: io.Discard}),
		addLogger,
	)

	// API 路由
	v1 := e.Group("/api/v1")
	v1.GET("/nodegroup-status", GetNodeGroupStatusHandler)
	v1.GET("/cloud-provider-option/account", GetCloudProviderOptionAccountHandler)
	v1.POST("/cloud-provider-option/account", UpdateCloudProviderOptionAccountHandler)
	v1.DELETE("/cloud-provider-option/account/:provider/:account", DeleteCloudProviderOptionAccountHandler)
	v1.GET("/cloud-provider-option/instance-parameter", GetCloudProviderOptionInstanceParameterHandler)
	v1.POST("/cloud-provider-option/instance-parameter", UpdateCloudProviderOptionInstanceParameterHandler)
	v1.DELETE("/cloud-provider-option/instance-parameter/:name", DeleteCloudProviderOptionInstanceParameterHandler)
	v1.GET("/nodegroup", GetNodeGroupHandler)
	v1.POST("/nodegroup", UpdateNodeGroupHandler)
	v1.GET("/nodegroup/instance", GetNodeGroupInstanceHandler)
	v1.POST("/nodegroup/instance", UpdateNodeGroupInstanceHandler)

	// 静态文件服务
	distFS, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		klog.Fatalf("failed to create sub filesystem: %v", err)
	}

	// 所有非 API 路径都服务静态文件
	e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(distFS))))

	// Start server
	go func() {
		if err := e.Start(addr); err != nil {
			klog.Errorf("server listen error: %s", err)
		}
	}()
	klog.V(1).Infof("http server listen at: %s", addr)

	// Graceful shutdown
	<-ctx.Done()
	klog.V(1).Infof("shutdown http server...")
	if err := e.Shutdown(ctx); err != nil {
		klog.Errorf("http server shutdown failed, err:%s", err)
	}
}
```

## 步骤 3: 重新编译项目

```bash
# 回到项目根目录
cd ..

# 编译项目
go build -o cluster-autoscaler-grpc-provider .
```

## 步骤 4: 运行和访问

运行编译后的二进制文件：

```bash
./cluster-autoscaler-grpc-provider
```

然后在浏览器中访问：`http://localhost:8080`

## 注意事项

1. **embed 指令**：`//go:embed` 必须紧邻变量声明，中间不能有空行
2. **路径问题**：embed 的路径是相对于包含 embed 指令的 Go 文件的相对路径
3. **SPA 路由**：由于 Vue 使用客户端路由，所有非 API 请求都应该返回 index.html
4. **构建顺序**：每次修改 UI 后，需要先 `npm run build` 再重新编译 Go 项目
5. **开发模式**：开发时建议使用 `npm run dev` 启动独立的前端服务器，使用代理访问后端 API

## 开发 vs 生产

### 开发模式
- UI: `npm run dev` 运行在 `http://localhost:3000`
- 后端: 运行 Go 服务在 `http://localhost:8080`
- 前端通过 Vite 代理访问后端 API

### 生产模式
- 构建 UI: `npm run build`
- 编译 Go 项目（包含嵌入的静态文件）
- 运行单个二进制文件，同时提供 API 和 UI

## 可选优化

如果希望在开发时也能通过 Go 服务访问 UI，可以添加构建标签：

```go
//go:build !dev
// +build !dev

package server

import "embed"

//go:embed all:../ui/dist
var uiFS embed.FS
```

然后在开发时使用：
```bash
go run -tags dev .
```

生产构建时：
```bash
go build -o cluster-autoscaler-grpc-provider .
```
