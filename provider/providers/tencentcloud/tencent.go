package tencentcloud

import (
	"context"
	"encoding/json"
	"fmt"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/provider/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
)

const (
	ProviderName = "tencentcloud"
)

type Client struct {
	client *cvm.Client
}

// BuildTencentCloudProvider 返回一个Client实例
func BuildTencentCloudProvider(paras map[string]string) (*Client, error) {
	if v, ok := paras["secretId"]; !ok || v == "" {
		return nil, fmt.Errorf("init cloudprovider(%s) client failed, missing para 'secretId'", ProviderName)
	}
	if v, ok := paras["secretKey"]; !ok || v == "" {
		return nil, fmt.Errorf("init cloudprovider(%s) client failed, missing para 'secretKey'", ProviderName)
	}
	if v, ok := paras["region"]; !ok || v == "" {
		return nil, fmt.Errorf("init cloudprovider(%s) client failed, missing para 'region'", ProviderName)
	}

	client, err := cvm.NewClient(
		common.NewCredential(paras["secretId"], paras["secretKey"]),
		paras["region"],
		profile.NewClientProfile(),
	)
	if err != nil {
		return nil, fmt.Errorf("init cloudprovider(%s) client failed, %s", ProviderName, err)
	}
	return &Client{client: client}, nil
}

// CreateInstance 创建一个instance
// 返回instanceID
func (c *Client) CreateInstance(ctx context.Context, para interface{}) (string, error) {
	req := cvm.NewRunInstancesRequest()

	paraRaw, err := json.Marshal(para)
	if err != nil {
		err = fmt.Errorf("marshal instance.instanceParameter failde, %s", err)
		return "", err
	}

	err = req.FromJsonString(string(paraRaw))
	if err != nil {
		err = fmt.Errorf("check instance.instanceParameter failde, %s", err)
		return "", err
	}

	// 确保只创建一个
	var count int64 = 1
	req.InstanceCount = &count

	resp, err := c.client.RunInstancesWithContext(ctx, req)
	if err != nil {
		err = fmt.Errorf("create instance failed, %s", err)
		return "", err
	}

	switch len(resp.Response.InstanceIdSet) {
	case 0:
		return "", fmt.Errorf("not found instance id")
	case 1:
		return *resp.Response.InstanceIdSet[0], nil
	default:
		return "", fmt.Errorf("got too many instance: %v", resp.Response.InstanceIdSet)
	}
}

// InstanceStatus 查询某个实例的运行状态
func (c *Client) InstanceStatus(ctx context.Context, insId string) (InstanceStatus, error) {
	var limit = int64(100)

	req := cvm.NewDescribeInstancesRequest()
	req.Limit = &limit
	req.InstanceIds = append(req.InstanceIds, &insId)

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return "", fmt.Errorf("DescribeInstances failed, %s", err)
	}

	for _, respIns := range resp.Response.InstanceSet {
		if insId == *respIns.InstanceId {
			return statusMapping(*respIns.InstanceState), nil
		}
	}
	return InstanceStatusUnknown, fmt.Errorf("not found instance id")
}

// InstanceIp 查询某个实例的IP地址
func (c *Client) InstanceIp(ctx context.Context, insId string) (string, error) {
	var limit = int64(100)

	req := cvm.NewDescribeInstancesRequest()
	req.Limit = &limit
	req.InstanceIds = append(req.InstanceIds, &insId)

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return "", fmt.Errorf("DescribeInstances failed, %s", err)
	}

	for _, respIns := range resp.Response.InstanceSet {
		if insId == *respIns.InstanceId {
			if len(respIns.PrivateIpAddresses) > 0 {
				return *respIns.PrivateIpAddresses[0], nil
			}
		}
	}
	return "", fmt.Errorf("not found instance id")
}

// InstancesStatus 查询多个instance的状态
// 查不到的instance状态要设置为Unknown
func (c *Client) InstancesStatus(ctx context.Context, insIds ...string) (map[string] /*insId*/ InstanceStatus, error) {
	var limit = int64(len(insIds))

	req := cvm.NewDescribeInstancesRequest()
	req.Limit = &limit

	res := make(map[string]InstanceStatus, len(insIds))
	for i, insId := range insIds {
		req.InstanceIds = append(req.InstanceIds, &insIds[i])
		res[insId] = InstanceStatusUnknown
	}

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("DescribeInstances failed, %s", err)
	}

	for _, respIns := range resp.Response.InstanceSet {
		res[*respIns.InstanceId] = statusMapping(*respIns.InstanceState)
	}
	return res, nil
}

func (c *Client) DeleteInstance(ctx context.Context, insId string, _ interface{}) error {
	// 先查询如果已经处于待回收状态则返回成功
	req := cvm.NewDescribeInstancesRequest()
	var limit int64 = 100
	req.Limit = &limit
	req.InstanceIds = append(req.InstanceIds, &insId)

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return fmt.Errorf("DescribeInstances failed, %s", err)
	}

	// 找不到对应的实例
	if len(resp.Response.InstanceSet) == 0 {
		return fmt.Errorf("instance %s not found", insId)
	}

	for _, respIns := range resp.Response.InstanceSet {
		if statusMapping(*respIns.InstanceState) == InstanceStatusDeleted {
			return nil
		}
	}

	req1 := cvm.NewTerminateInstancesRequest()
	releasePrepaidDataDisks := true // 释放挂载的数据盘
	req1.InstanceIds = []*string{&insId}
	req1.ReleasePrepaidDataDisks = &releasePrepaidDataDisks

	_, err = c.client.TerminateInstances(req1)
	if err != nil {
		return fmt.Errorf("fail to TerminateInstances, %s", err)
	}
	return nil
}

// InstanceStatus: instance状态：https://cloud.tencent.com/document/api/213/15753#InstanceStatus
// PENDING：表示创建中
// LAUNCH_FAILED：表示创建失败
// RUNNING：表示运行中
// STOPPED：表示关机
// STARTING：表示开机中
// STOPPING：表示关机中
// REBOOTING：表示重启中
// SHUTDOWN：表示停止待销毁
// TERMINATING：表示销毁中
// ENTER_RESCUE_MODE：表示进入救援模式
// RESCUE_MODE：表示在救援模式中
// EXIT_RESCUE_MODE：表示退出救援模式
// ENTER_SERVICE_LIVE_MIGRATE：表示进入在线服务迁移
// SERVICE_LIVE_MIGRATE：表示在线服务迁移中
// EXIT_SERVICE_LIVE_MIGRATE：表示退出在线服务迁移。
// ==============================
// RestrictState实例业务状态。取值范围：
// NORMAL：表示正常状态的实例
// EXPIRED：表示过期的实例
// PROTECTIVELY_ISOLATED：表示被安全隔离的实例
func statusMapping(status string) InstanceStatus {
	switch status {
	case "PENDING":
		return InstanceStatusCreating
	case "LAUNCH_FAILED":
		return InstanceStatusFailed
	case "RUNNING",
		"STOPPED",
		"STARTING",
		"STOPPING",
		"REBOOTING",
		"ENTER_RESCUE_MODE",
		"RESCUE_MODE",
		"EXIT_RESCUE_MODE",
		"ENTER_SERVICE_LIVE_MIGRATE",
		"SERVICE_LIVE_MIGRATE",
		"EXIT_SERVICE_LIVE_MIGRATE":
		return InstanceStatusRunning
	case "TERMINATING", "SHUTDOWN":
		return InstanceStatusDeleted
	default:
		return InstanceStatusUnknown
	}
}
