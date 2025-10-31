package server

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup"
	"k8s.io/klog/v2"
)

type Response struct {
	ServiceCode
	Error string `json:"error,omitempty"` // 错误信息
	Data  any    `json:"data,omitempty"`  // 返回数据
}

type ServiceCode struct {
	Status int    `json:"status"` // 返回码
	Msg    string `json:"msg"`    // 返回信息
}

var (
	CodeOk        = ServiceCode{2000000, ""}
	CodeServerErr = ServiceCode{5000000, "服务器内部错误"}
)

func HttpServer(ctx context.Context, addr string) {
	e := echo.New()
	e.HidePort = true
	e.HideBanner = true
	e.Use(
		middleware.Recover(),
		middleware.LoggerWithConfig(middleware.LoggerConfig{Output: io.Discard}),
		addLogger,
	)

	v1 := e.Group("/api/v1")
	v1.GET("/nodegroup-status", getNodeGroupStatusHandler)

	// 查看账户列表
	v1.GET("/cloud-provider-option/account", getCloudProviderOptionAccountHandler)
	// 添加账户
	v1.POST("/cloud-provider-option/account", nil)
	// 修改账户
	v1.PUT("/cloud-provider-option/account", nil)
	// 删除账户
	v1.DELETE("/cloud-provider-option/account", nil)

	// 查看参数
	v1.GET("/cloud-provider-option/instance-parameter", getCloudProviderOptionInstanceParameterHandler)
	// 添加参数
	v1.POST("/cloud-provider-option/instance-parameter", nil)
	// 修改参数
	v1.PUT("/cloud-provider-option/instance-parameter", nil)
	// 删除参数
	v1.DELETE("/cloud-provider-option/instance-parameter", nil)

	v1.GET("/nodegroup", getNodeGroupHandler)
	v1.POST("/nodegroup", nil)
	v1.PUT("/nodegroup", nil)
	v1.DELETE("/nodegroup", nil)

	v1.GET("/nodegroup/instance", getNodeGroupInstanceHandler)
	v1.PUT("/nodegroup/instance", nil)

	// Start server in a goroutine
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

func addLogger(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		req := c.Request()
		klog.V(1).Infof("request: Method:%s Path:%s RemoteAddr:%s, UserAgent:%s BodyLength:%d",
			req.Method,
			req.URL.Path,
			req.RemoteAddr,
			req.UserAgent(),
			req.ContentLength,
		)

		err := next(c)

		res := c.Response()
		klog.V(1).Infof("response: Method:%s Path:%s Status:%d BodyLength:%d Cost:%s",
			req.Method,
			req.URL.Path,
			res.Status,
			res.Size,
			time.Since(start),
		)

		return err
	}
}

func getNodeGroupStatusHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data})
}

func getCloudProviderOptionAccountHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.Accounts})
}

func getCloudProviderOptionInstanceParameterHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.InstanceParameter})
}

func getNodeGroupHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	for i := 0; i < len(data.NodeGroups); i++ {
		data.NodeGroups[i].Instances = nil
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.NodeGroups})
}

func getNodeGroupInstanceHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.InstanceParameter})
}
