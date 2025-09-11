package tencentcloud

import (
	"context"
	"fmt"
	"strings"

	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/instance"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/provider/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	"k8s.io/apimachinery/pkg/util/json"
)

const (
	ProviderName = "tencentcloud"
)

type Client struct {
	client *cvm.Client
}

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

func (c *Client) InstanceStatus(ctx context.Context, ins *Instance) (*Instance, error) {
	var limit int64 = int64(100)

	req := cvm.NewDescribeInstancesRequest()
	req.Limit = &limit
	_, _, _, instanceID, _ := ExtractProviderID(ins.ProviderID)
	req.InstanceIds = append(req.InstanceIds, &instanceID)

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		ins.ErrorMsg = err.Error()
		return ins, fmt.Errorf("DescribeInstances failed, %s", err)
	}

	ins.ErrorMsg = "unknown status"
	for _, respIns := range resp.Response.InstanceSet {
		if instanceID == *respIns.InstanceId {
			ins.Status = stateMapping(*respIns.InstanceState)
			ins.ErrorMsg = ""

			// 如果instance状态正常则更新IP地址
			if ins.IP == "" && len(respIns.PrivateIpAddresses) > 0 {
				ins.IP = *respIns.PrivateIpAddresses[0]
			}
			break
		}
	}
	return ins, nil
}

func (c *Client) InstancesStatus(ctx context.Context, instances ...*Instance) ([]*Instance, error) {
	var limit int64 = int64(len(instances))

	req := cvm.NewDescribeInstancesRequest()
	req.Limit = &limit
	for _, ins := range instances {
		_, _, _, instanceID, _ := ExtractProviderID(ins.ProviderID)
		req.InstanceIds = append(req.InstanceIds, &instanceID)
	}

	for _, ins := range instances {
		ins.Status = StatusUnknown
		ins.ErrorMsg = "unknown status"
	}

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return instances, fmt.Errorf("DescribeInstances failed, %s", err)
	}

	for _, respIns := range resp.Response.InstanceSet {
		for _, ins := range instances {
			_, _, _, instanceID, _ := ExtractProviderID(ins.ProviderID)
			if instanceID == *respIns.InstanceId {
				ins.Status = stateMapping(*respIns.InstanceState)
				ins.ErrorMsg = ""
				break
			}
		}
	}

	// 有些instance没有查找到
	notFoudIns := make([]string, 0, len(instances))
	for _, ins := range instances {
		if ins.Status == StatusUnknown {
			_, _, _, instanceID, _ := ExtractProviderID(ins.ProviderID)
			notFoudIns = append(notFoudIns, instanceID)
		}
	}

	if len(notFoudIns) != 0 {
		return instances, fmt.Errorf("instance status unknown: %s", strings.Join(notFoudIns, ","))
	}

	return instances, nil
}

func (c *Client) CreateInstance(ctx context.Context, instance *Instance, para interface{}) (*Instance, error) {
	req := cvm.NewRunInstancesRequest()

	paraRaw, err := json.Marshal(para)
	if err != nil {
		err = fmt.Errorf("marshal instance(%s).instanceParameter failde, %s", instance.ID, err)
		return instance, err
	}

	err = req.FromJsonString(string(paraRaw))
	if err != nil {
		err = fmt.Errorf("check instance(%s).instanceParameter failde, %s", instance.ID, err)
		return instance, err
	}

	resp, err := c.client.RunInstancesWithContext(ctx, req)
	if err != nil {
		err = fmt.Errorf("create instance(%s) failed, %s", instance.ID, err)
		return instance, err
	}

	if len(resp.Response.InstanceIdSet) > 0 {
		instance.ProviderID = *resp.Response.InstanceIdSet[0]
	}

	return instance, nil
}

func (c *Client) DeleteInstance(ctx context.Context, instance *Instance, _ interface{}) (*Instance, error) {
	// 先查询如果已经处于待回收状态则返回成功
	req := cvm.NewDescribeInstancesRequest()
	var limit int64 = 100
	req.Limit = &limit
	provider, account, region, instanceID, _ := ExtractProviderID(instance.ProviderID)
	req.InstanceIds = append(req.InstanceIds, &instanceID)

	resp, err := c.client.DescribeInstancesWithContext(ctx, req)
	if err != nil {
		return instance, fmt.Errorf("DescribeInstances failed, %s", err)
	}

	// 找不到对应的实例
	if len(resp.Response.InstanceSet) == 0 {
		return instance, fmt.Errorf("instance %s/%s/%s/%s not found", provider, account, region, instanceID)
	}

	for _, respIns := range resp.Response.InstanceSet {
		if stateMapping(*respIns.InstanceState) == StatusDeleted {
			return instance, nil
		}
	}

	req1 := cvm.NewTerminateInstancesRequest()
	_, _, _, instanceID, _ = ExtractProviderID(instance.ProviderID)
	releasePrepaidDataDisks := true // 释放挂载的数据盘
	req1.InstanceIds = []*string{&instanceID}
	req1.ReleasePrepaidDataDisks = &releasePrepaidDataDisks

	_, err = c.client.TerminateInstances(req1)
	if err != nil {
		return instance, fmt.Errorf("fail to TerminateInstances, %s", err)
	}
	return instance, nil
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
func stateMapping(status string) Status {
	switch status {
	case "PENDING":
		return StatusCreating
	case "LAUNCH_FAILED":
		return StatusFailed
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
		return StatusRunning
	case "TERMINATING", "SHUTDOWN":
		return StatusDeleted
	default:
		return StatusUnknown
	}
}
