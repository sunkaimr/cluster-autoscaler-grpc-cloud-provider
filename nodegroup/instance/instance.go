package instance

import (
	"time"

	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/pkg/utils"
)

type Status string

const (
	StatusPending         Status = "Pending"         // 等待创建instance
	StatusCreating        Status = "Creating"        // 已经调用了云厂商接口创建了instance, 等待运行起来
	StatusCreated         Status = "Created"         // instance状态已经运行起来，等待执行AfterCratedHook
	StatusRunning         Status = "Running"         // instance状态已经运行起来,AfterCratedHook执行成功
	StatusPendingDeletion Status = "PendingDeletion" // 删除instance前等待执行BeforeDeleteHook
	StatusDeleting        Status = "Deleting"        // BeforeDeleteHook执行成功等待调用云厂商接口删除instance
	StatusDeleted         Status = "Deleted"         // 云厂商instance成功，instance记录保留一段时间后删除记录
	StatusFailed          Status = "Failed"          // instance状态流转过程中失败了
	StatusUnknown         Status = "Unknown"         // 查询不到instance的状态
)

type Instance struct {
	ID         string    `json:"id" yaml:"id"`
	Name       string    `json:"name" yaml:"name"`                             // 节点在kubernetes中的名称
	IP         string    `json:"ip" yaml:"ip"`                                 // 节点的IP地址
	ProviderID string    `json:"providerID" yaml:"providerID"`                 // 节点在云上的ID，格式如：externalgrpc://<provider>/<account>/<region>/<instanceID>
	Status     Status    `json:"status" yaml:"status"`                         // 节点当前的状态
	ErrorMsg   string    `json:"errorMsg,omitempty" yaml:"errorMsg,omitempty"` // 操作失败时的详细信息
	UpdateTime time.Time `json:"updateTime" yaml:"updateTime"`                 // 信息更新时间
}

type InstanceList []*Instance

func (c *InstanceList) Add(ins *Instance) {
	for _, v := range *c {
		if v.ID == ins.ID {
			return
		}
	}
	*c = append(*c, ins)
}

func (c *InstanceList) Delete(id string) {
	for i, v := range *c {
		if v.ID == id {
			if i == len(*c) {
				*c = (*c)[:i]
				return
			}
			*c = append((*c)[:i], (*c)[i+1:]...)
			return
		}
	}
}

func (c *InstanceList) Find(id string) *Instance {
	for i, v := range *c {
		if v.ID == id {
			return (*c)[i]
		}
	}
	return nil
}

func (c *InstanceList) FindByName(insName string) *Instance {
	for i, v := range *c {
		if v.Name != "" && v.Name == insName {
			return (*c)[i]
		}
	}
	return nil
}

// DecreasePending 减少N个Pending的instance
func (c *InstanceList) DecreasePending(num int) int /*实际减少的数量*/ {
	deleteIdx := make([]int, 0, len(*c))
	for i, v := range *c {
		if len(deleteIdx) >= num {
			break
		}
		if (v.Status == StatusPending) || (v.Status == StatusFailed && v.ProviderID == "") {
			deleteIdx = append(deleteIdx, i)
		}
	}

	if len(deleteIdx) == 0 {
		return 0
	}

	newIns := make([]*Instance, 0, len(*c)-len(deleteIdx))
	for i, v := range *c {
		if utils.ElementExist(i, deleteIdx) {
			continue
		}
		newIns = append(newIns, v)
	}
	*c = newIns
	return len(deleteIdx)
}
