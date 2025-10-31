package instance

import (
	"time"

	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/pkg/utils"
)

type Stage string

const (
	StagePending         Stage = "Pending"         // 等待创建instance
	StageCreating        Stage = "Creating"        // 已经调用了云厂商接口创建了instance, 等待运行起来
	StageCreated         Stage = "Created"         // instance状态已经运行起来，等待执行AfterCratedHook
	StageJoined          Stage = "Joined"          // AfterCratedHook执行成功,等待加入集群后执行打标签和污点
	StageRunning         Stage = "Running"         // instance成功加入集群
	StagePendingDeletion Stage = "PendingDeletion" // 删除instance前等待执行BeforeDeleteHook
	StageDeleting        Stage = "Deleting"        // BeforeDeleteHook执行成功等待调用云厂商接口删除instance
	StageDeleted         Stage = "Deleted"         // 云厂商instance成功，instance记录保留一段时间后删除记录
)

type Status string

const (
	StatusInit      = "Init"      // 初始状态
	StatusInProcess = "InProcess" // 正在处理中
	StatusSuccess   = "Success"   // 执行成功
	StatusFailed    = "Failed"    // 执行失败
	StatusUnknown   = "Unknown"   // 未知状态，只有Running时会用到
)

type Instance struct {
	ID         string    `json:"id" yaml:"id"`
	Name       string    `json:"name" yaml:"name"`                       // 节点在kubernetes中的名称
	IP         string    `json:"ip" yaml:"ip"`                           // 节点的IP地址
	ProviderID string    `json:"providerID" yaml:"providerID"`           // 节点在云上的ID，格式如：externalgrpc://<provider>/<account>/<region>/<instanceID>
	Stage      Stage     `json:"stage" yaml:"stage"`                     // 节点处于那个阶段
	Status     Status    `json:"status" yaml:"status"`                   // 该阶段的执行结果
	Error      string    `json:"error,omitempty" yaml:"error,omitempty"` // 操作失败时的详细信息
	UpdateTime time.Time `json:"updateTime" yaml:"updateTime"`           // 信息更新时间
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
			*c = append((*c)[:i], (*c)[i+1:]...)
			return
		}
	}
}

func (c *InstanceList) DeleteByName(name string) {
	for i, v := range *c {
		if v.Name == name {
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

func (c *InstanceList) FindByProviderID(providerId string) *Instance {
	for i, v := range *c {
		if v.ProviderID != "" && v.ProviderID == providerId {
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
		if v.Stage == StagePending {
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

func (c *InstanceList) Len() int {
	return len(*c)
}

func (c *InstanceList) Less(i, j int) bool {
	scoringFn := func(i *Instance) int {
		stageScoreMap := map[Stage]int{
			StageCreating: 10,
			StageCreated:  20,
			StageJoined:   30,
			StageRunning:  40,
			StagePending:  50,
			StageDeleting: 60,
			StageDeleted:  70,
		}
		statusScoreMap := map[Status]int{
			StatusInit:      1,
			StatusInProcess: 2,
			StatusSuccess:   3,
			StatusFailed:    4,
			StatusUnknown:   5,
		}
		return stageScoreMap[i.Stage] + statusScoreMap[i.Status]
	}

	iIns, jIns := (*c)[i], (*c)[j]
	iScore, jScore := scoringFn(iIns), scoringFn(jIns)
	if iScore < jScore {
		return true
	} else if iScore == jScore {
		if iIns.IP < jIns.IP {
			return true
		}
	}
	return false
}
func (c *InstanceList) Swap(i, j int) {
	(*c)[i], (*c)[j] = (*c)[j], (*c)[i]
}
