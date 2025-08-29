package tencentcloud

import (
	"fmt"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/instance"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/provider/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
	cvm "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm/v20170312"
	"strings"
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

func (c *Client) InstanceStatus(instance *Instance) (*Instance, error) {
	filterIP := "private-ip-address"
	req := cvm.NewDescribeInstancesRequest()
	filter := &cvm.Filter{
		Name:   &filterIP,
		Values: []*string{},
	}

	req.Filters = []*cvm.Filter{filter}

	_, err := c.client.DescribeInstances(req)
	if err != nil {
		return nil, fmt.Errorf("fail to DescribeInstances, %s", err)
	}

	return nil, nil
}

func (c *Client) InstancesStatus(instances ...*Instance) ([]*Instance, error) {
	var limit int64 = int64(len(instances))

	req := cvm.NewDescribeInstancesRequest()
	req.Limit = &limit
	for _, ins := range instances {
		_, _, _, instanceID, _ := ExtractProviderID(ins.ProviderID)
		req.InstanceIds = append(req.InstanceIds, &instanceID)
	}

	resp, err := c.client.DescribeInstances(req)
	if err != nil {
		return nil, fmt.Errorf("fail to DescribeInstances, %s", err)
	}

	newIns := make([]*Instance, 0, len(resp.Response.InstanceSet))
	for _, respIns := range resp.Response.InstanceSet {
		for _, ins := range instances {
			_, _, _, instanceID, _ := ExtractProviderID(ins.ProviderID)
			if instanceID != *respIns.InstanceId {
				continue
			}

			newIns = append(newIns, &Instance{
				Name:       ins.Name,
				IP:         ins.IP,
				ProviderID: ins.ProviderID,
				Status:     stateMapping(*respIns.InstanceState),
			})
			break
		}
	}

	// 有些instance没有查找到
	if len(instances) != len(newIns) {
		notfoudIns := make([]string, 0, len(instances)-len(newIns))
		for _, ins1 := range instances {
			exist := false
			for _, ins2 := range newIns {
				if ins1.ProviderID == ins2.ProviderID {
					exist = true
					break
				}
			}
			if !exist {
				notfoudIns = append(notfoudIns, fmt.Sprintf("%s:%s", ins1.Name, ins1.ProviderID))
			}
		}

		return newIns, fmt.Errorf("instance not found: %s", strings.Join(notfoudIns, ","))
	}

	return newIns, nil
}

func (c *Client) CreateInstance(instance *Instance, para interface{}) (*Instance, error) {

	return nil, nil
}

func (c *Client) DeleteInstance(instance *Instance, para interface{}) (*Instance, error) {

	return nil, nil
}

// instance状态：https://cloud.tencent.com/document/api/213/15753#InstanceStatus
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
func stateMapping(status string) Status {
	switch status {
	case "PENDING", "LAUNCH_FAILED":
		return StatusCreating
	case "RUNNING", "STOPPED", "STARTING", "STOPPING", "REBOOTING", "SHUTDOWN",
		"ENTER_RESCUE_MODE", "RESCUE_MODE", "EXIT_RESCUE_MODE", "ENTER_SERVICE_LIVE_MIGRATE",
		"SERVICE_LIVE_MIGRATE", "EXIT_SERVICE_LIVE_MIGRATE":
		return StatusRunning
	case "TERMINATING":
		return StatusDeleting
	default:
		return StatusUnknown
	}
}
