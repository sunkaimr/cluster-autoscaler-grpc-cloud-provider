package server

import (
	"context"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup/instance"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/provider"
	"k8s.io/klog/v2"
)

//go:embed ui/dist
var embeddedFiles embed.FS

type Response struct {
	ServiceCode
	Error string `json:"error,omitempty"` // 错误信息
	Data  any    `json:"data,omitempty"`  // 返回数据
}

type ServiceCode struct {
	Status int    `json:"status"`        // 返回码
	Msg    string `json:"msg,omitempty"` // 返回信息
}

var (
	CodeOk        = ServiceCode{2000000, ""}
	CodeParaError = ServiceCode{4000000, "参数解析失败"}
	CodeServerErr = ServiceCode{5000000, "服务器内部错误"}

	CodeUpdateProviderAccountErr = ServiceCode{4001401, "修改provider账号失败"}
	CodeDeleteProviderAccountErr = ServiceCode{4001402, "删除provider账号失败"}

	CodeDeleteInstanceParameterErr = ServiceCode{4002401, "删除Instance参数失败"}
	CodeModifyInstanceParameterErr = ServiceCode{4002402, "修改Instance参数失败"}
	CodeInstanceParameterNotFound  = ServiceCode{4042401, "Instance参数不存在"}

	CodeModifyNodeGroupErr = ServiceCode{4003401, "修改Nodegroup失败"}
	CodeNodeGroupNotFound  = ServiceCode{4043401, "NodeGroup不存在"}
	CodeInstanceNotFound   = ServiceCode{4043402, "Instance不存在"}
)

func ServiceCode2HttpCode(r ServiceCode) int {
	if r.Status == 0 {
		return http.StatusOK
	}
	return r.Status / 10000
}

func HttpServer(ctx context.Context, addr string) {
	e := echo.New()
	e.HidePort = true
	e.HideBanner = true
	e.Use(
		middleware.Recover(),
		middleware.LoggerWithConfig(middleware.LoggerConfig{Output: io.Discard}),
		addLogger,
	)

	// Serve embedded UI files
	uiFS, err := fs.Sub(embeddedFiles, "ui/dist")
	if err != nil {
		klog.Errorf("failed to get embedded UI files: %s", err)
	} else {
		e.GET("/*", echo.WrapHandler(http.FileServer(http.FS(uiFS))))
	}

	v1 := e.Group("/api/v1")
	v1.GET("/nodegroup-status", GetNodeGroupStatusHandler)

	// 查看账户列表
	v1.GET("/cloud-provider-option/account", GetCloudProviderOptionAccountHandler)
	// 添加或更新账户
	v1.POST("/cloud-provider-option/account", UpdateCloudProviderOptionAccountHandler)
	// 删除账户
	v1.DELETE("/cloud-provider-option/provider/:provider/account/:account", DeleteCloudProviderOptionAccountHandler)
	// 删除供应商
	v1.DELETE("/cloud-provider-option/provider/:provider", DeleteCloudProviderHandler)

	// 查看Instance参数
	v1.GET("/cloud-provider-option/instance-parameter", GetCloudProviderOptionInstanceParameterHandler)
	// 添加或更新Instance参数
	v1.POST("/cloud-provider-option/instance-parameter", UpdateCloudProviderOptionInstanceParameterHandler)
	// 删除Instance参数
	v1.DELETE("/cloud-provider-option/instance-parameter/:name", DeleteCloudProviderOptionInstanceParameterHandler)

	v1.GET("/nodegroup", GetNodeGroupHandler)
	v1.POST("/nodegroup", UpdateNodeGroupHandler)

	// 增加或减少节点组内节点数量，需要2个参数：节点组ID、增加节点的数量（正为增加，负为减少）
	// /nodegroup/:nodegroup?delta=1 为增加一个
	// /nodegroup/:nodegroup?delta=-1 为减少一个
	v1.PATCH("/nodegroup/:nodegroup", ChangeNodeGroupSizeHandler)
	// 删除节点组中的节点
	v1.DELETE("/nodegroup/:nodegroup/nodename/:nodename", DeleteNodeGroupNodeHandler)
	//v1.DELETE("/nodegroup/:nodegroup", DeleteNodeGroupHandler) // 暂时不提供这个接口，有需要直接修改config-map

	v1.GET("/nodegroup/instance", GetNodeGroupInstanceHandler)
	v1.POST("/nodegroup/instance", UpdateNodeGroupInstanceHandler)

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

func GetNodeGroupStatusHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data})
}

func GetCloudProviderOptionAccountHandler(c echo.Context) error {
	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.Accounts})
}

// UpdateCloudProviderOptionAccountHandler 添加云账号，没有则创建有则更新
func UpdateCloudProviderOptionAccountHandler(c echo.Context) error {
	addProviders := map[string]provider.Provider{}
	err := c.Bind(&addProviders)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeParaError), Response{ServiceCode: CodeParaError, Error: err.Error()})
	}

	err = nodegroup.GetNodeGroups().UpdateCloudProviderAccount(addProviders)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeUpdateProviderAccountErr), Response{ServiceCode: CodeUpdateProviderAccountErr, Error: err.Error()})
	}

	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}

	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.Accounts})
}

// DeleteCloudProviderOptionAccountHandler 删除云账号
func DeleteCloudProviderOptionAccountHandler(c echo.Context) error {
	delProvider := c.Param("provider")
	delAccount := c.Param("account")

	err := nodegroup.GetNodeGroups().DeleteCloudProviderAccount(delProvider, delAccount)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeDeleteProviderAccountErr), Response{ServiceCode: CodeDeleteProviderAccountErr, Error: err.Error()})
	}

	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}

	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.Accounts})
}

// DeleteCloudProviderHandler 删除云服务商（包括其下所有账号）
func DeleteCloudProviderHandler(c echo.Context) error {
	delProvider := c.Param("provider")

	err := nodegroup.GetNodeGroups().DeleteCloudProvider(delProvider)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeDeleteProviderAccountErr), Response{ServiceCode: CodeDeleteProviderAccountErr, Error: err.Error()})
	}

	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}

	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.Accounts})
}

// GetCloudProviderOptionInstanceParameterHandler 查询Instance创建参数
func GetCloudProviderOptionInstanceParameterHandler(c echo.Context) error {
	name := c.QueryParam("name")
	ngc, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	if name == "" {
		return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &ngc.CloudProviderOption.InstanceParameter})
	}

	if para, ok := ngc.CloudProviderOption.InstanceParameter[name]; ok {
		data := map[string]provider.InstanceParameter{name: para}
		return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: data})
	} else {
		return c.JSON(ServiceCode2HttpCode(CodeInstanceParameterNotFound), Response{ServiceCode: CodeInstanceParameterNotFound, Error: fmt.Sprintf("cloudProviderOption.instanceParameter.%s not exist", name)})
	}
}

// UpdateCloudProviderOptionInstanceParameterHandler 添加或更新Instance创建参数
func UpdateCloudProviderOptionInstanceParameterHandler(c echo.Context) error {
	addInstanceParameter := map[string]provider.InstanceParameter{}
	err := c.Bind(&addInstanceParameter)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeParaError), Response{ServiceCode: CodeParaError, Error: err.Error()})
	}

	err = nodegroup.GetNodeGroups().UpdateInstanceParameter(addInstanceParameter)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeModifyInstanceParameterErr), Response{ServiceCode: CodeModifyInstanceParameterErr, Error: err.Error()})
	}

	ngc, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &ngc.CloudProviderOption.InstanceParameter})
}

// DeleteCloudProviderOptionInstanceParameterHandler 删除Instance创建参数
func DeleteCloudProviderOptionInstanceParameterHandler(c echo.Context) error {
	name := c.Param("name")

	err := nodegroup.GetNodeGroups().DeleteInstanceParameter(name)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeDeleteInstanceParameterErr), Response{ServiceCode: CodeDeleteInstanceParameterErr, Error: err.Error()})
	}

	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}

	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.CloudProviderOption.InstanceParameter})
}

func GetNodeGroupHandler(c echo.Context) error {
	id := c.QueryParam("id")

	data, err := nodegroup.GetNodeGroups().Status()
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}

	if id != "" {
		for i := 0; i < len(data.NodeGroups); i++ {
			if data.NodeGroups[i].Id == id {
				data.NodeGroups[i].Instances = nil
				return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.NodeGroups[i]})
			}
		}
		return c.JSON(ServiceCode2HttpCode(CodeNodeGroupNotFound), Response{ServiceCode: CodeNodeGroupNotFound})
	}

	for i := 0; i < len(data.NodeGroups); i++ {
		data.NodeGroups[i].Instances = nil
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &data.NodeGroups})
}

// UpdateNodeGroupHandler 添加或更新NodeGroup
func UpdateNodeGroupHandler(c echo.Context) error {
	ng := &nodegroup.NodeGroup{}
	err := c.Bind(ng)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeParaError), Response{ServiceCode: CodeParaError, Error: err.Error()})
	}

	err = nodegroup.GetNodeGroups().UpdateNodeGroup(ng)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeModifyNodeGroupErr), Response{ServiceCode: CodeModifyNodeGroupErr, Error: err.Error()})
	}

	data, err := nodegroup.GetNodeGroups().FindNodeGroupById(ng.Id)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: data})
}

// DeleteNodeGroupHandler 删除NodeGroup
//func DeleteNodeGroupHandler(c echo.Context) error {
//	name := c.Param("name")
//	err := nodegroup.GetNodeGroups().DeleteNodeGroup(name)
//	if err != nil {
//		return c.JSON(http.StatusBadRequest, Response{ServiceCode: CodeUpdateProviderAccountErr, Error: err.Error()})
//	}
//
//	ngc, err := nodegroup.GetNodeGroups().Status()
//	if err != nil {
//		return c.JSON(http.StatusInternalServerError, Response{ServiceCode: CodeServerErr, Error: err.Error()})
//	}
//	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &ngc.CloudProviderOption.InstanceParameter})
//}

// GetNodeGroupInstanceHandler 查询Instance状态
// nodegroup、id、name、IP、Stage、Status查询
func GetNodeGroupInstanceHandler(c echo.Context) error {
	id, name, ip, stage, status, ng := c.QueryParam("id"), c.QueryParam("name"), c.QueryParam("ip"), c.QueryParam("stage"), c.QueryParam("status"), c.QueryParam("nodegroup")

	instances := make([]*instance.Instance, 0, 100)
	ngs := nodegroup.GetNodeGroups().List()
	if id != "" {
		for _, v := range ngs {
			if ins := v.Instances.Find(id); ins != nil {
				instances = append(instances, ins)
				return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: instances})
			}
		}
		return c.JSON(ServiceCode2HttpCode(CodeInstanceNotFound), Response{ServiceCode: CodeInstanceNotFound})
	} else if name != "" {
		for _, v := range ngs {
			if ins := v.Instances.FindByName(name); ins != nil {
				instances = append(instances, ins)
				return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: instances})
			}
		}
		return c.JSON(ServiceCode2HttpCode(CodeInstanceNotFound), Response{ServiceCode: CodeInstanceNotFound})
	} else if ip != "" {
		for _, v := range ngs {
			if ins := v.Instances.FindByIp(ip); ins != nil {
				instances = append(instances, ins)
				return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: instances})
			}
		}
		return c.JSON(ServiceCode2HttpCode(CodeInstanceNotFound), Response{ServiceCode: CodeInstanceNotFound})
	} else if stage != "" {
		ins := nodegroup.GetNodeGroups().FilterInstanceByStages(instance.Stage(stage))
		return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: ins})
	} else if status != "" {
		ins := nodegroup.GetNodeGroups().FilterInstanceByStatus(instance.Status(status))
		return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: ins})
	} else if ng != "" {
		v, _ := nodegroup.GetNodeGroups().FindNodeGroupById(ng)
		return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: v.Instances})
	}

	for _, v := range ngs {
		instances = append(instances, v.Instances...)
	}

	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &instances})
}

func UpdateNodeGroupInstanceHandler(c echo.Context) error {
	var newIns []*instance.Instance
	err := c.Bind(&newIns)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeParaError), Response{ServiceCode: CodeParaError, Error: err.Error()})
	}

	for _, v := range newIns {
		if ins := nodegroup.GetNodeGroups().FindInstance(v.ID); ins != nil {
			ins.Status = v.Status
			ins.Stage = v.Stage
			ins.Error = v.Error
			nodegroup.GetNodeGroups().UpdateInstancesStatus(ins)
		}
	}

	instances := make([]*instance.Instance, 0, 100)
	for _, v := range newIns {
		if ins := nodegroup.GetNodeGroups().FindInstance(v.ID); ins != nil {
			instances = append(instances, ins)
		}
	}

	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk, Data: &instances})
}

func ChangeNodeGroupSizeHandler(c echo.Context) error {
	id := c.Param("nodegroup")
	deltaStr := c.QueryParam("delta")
	delta, err := strconv.Atoi(deltaStr)
	if err != nil {
		return c.JSON(ServiceCode2HttpCode(CodeParaError), Response{ServiceCode: CodeParaError, Error: err.Error()})
	}

	if delta >= 0 {
		klog.V(0).Infof("got NodeGroupIncreaseSize request: nodegroup(%s) request add %d node", id, delta)
		actuality, err := nodegroup.GetNodeGroups().NodeGroupIncreaseSize(id, delta)
		if err != nil {
			err = fmt.Errorf("nodegroup(%s) increase %d node failed, %s", id, delta, err)
			klog.Error(err)
			return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
		}
		klog.V(0).Infof("nodegroup(%s) request increase %d node actual increase %d node", id, delta, actuality)
	} else {
		// 如果还有未加入集群的节点则不再加入并走回收节点流程
		actuality, err := nodegroup.GetNodeGroups().DecreaseNodeGroupTargetSize(id, -delta)
		if err != nil {
			err = fmt.Errorf("nodegroup(%s) decrease %d node failed, %s", id, -delta, err)
			klog.Error(err)
			return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
		}
		klog.V(0).Infof("nodegroup(%s) request decrease %d node actual decrease %d node", id, -delta, actuality)
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk})
}

func DeleteNodeGroupNodeHandler(c echo.Context) error {
	id := c.Param("nodegroup")
	nodename := c.Param("nodename")
	err := nodegroup.GetNodeGroups().DeleteNodesInNodeGroupByNodeName(id, nodename)
	if err != nil {
		err = fmt.Errorf("nodegroup(%s) delete %s node failed, %s", id, nodename, err)
		klog.Error(err)
		return c.JSON(ServiceCode2HttpCode(CodeServerErr), Response{ServiceCode: CodeServerErr, Error: err.Error()})
	}
	return c.JSON(http.StatusOK, Response{ServiceCode: CodeOk})
}
