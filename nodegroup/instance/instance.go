package instance

import (
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/pkg/utils"
	"time"
)

type Status string

const (
	StatusPending         Status = "Pending"
	StatusCreating        Status = "Creating"
	StatusCreated         Status = "Created"
	StatusRegistering     Status = "Registering"
	StatusRegistered      Status = "Registered"
	StatusRunning         Status = "Running"
	StatusPendingDeletion Status = "PendingDeletion"
	StatusDeleting        Status = "Deleting"
	StatusDeleted         Status = "Deleted"
	StatusFailed          Status = "Failed"
	StatusUnknown         Status = "Unknown"
)

type Instance struct {
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
		if v.Name != "" && v.Name == ins.Name {
			return
		}
	}
	*c = append(*c, ins)
}

func (c *InstanceList) Delete(insName string) {
	for i, v := range *c {
		if v.Name != "" && v.Name == insName {
			*c = append((*c)[:i], (*c)[i:]...)
			return
		}
	}
}

func (c *InstanceList) Find(insName string) *Instance {
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
		if v.Name == "" && v.Status == StatusPending {
			deleteIdx = append(deleteIdx, i)
		}
		if len(deleteIdx) >= num {
			break
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
