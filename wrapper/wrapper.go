/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package wrapper

import (
	"context"
	"fmt"

	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/instance"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider/externalgrpc/protos"
	"k8s.io/klog/v2"
)

// Wrapper implements protos.CloudProviderServer.
type Wrapper struct {
	protos.UnimplementedCloudProviderServer

	provider cloudprovider.CloudProvider
}

// NewCloudProviderGrpcWrapper creates a grpc wrapper for a cloud provider implementation.
func NewCloudProviderGrpcWrapper(provider cloudprovider.CloudProvider) *Wrapper {
	return &Wrapper{
		provider: provider,
	}
}

func pbNodeGroup(ng *nodegroup.NodeGroup) *protos.NodeGroup {
	return &protos.NodeGroup{
		Id:      ng.Id,
		MaxSize: int32(ng.MaxSize),
		MinSize: int32(ng.MinSize),
		Debug:   ng.Debug(),
	}
}

func debug(req fmt.Stringer) {
	klog.V(6).Infof("got gRPC request: %T %s", req, req)
}

// NodeGroups is the wrapper for the cloud provider NodeGroups method.
// 返回所有可伸缩节点组列表, 需返回节点组的唯一 ID（如 nodegroup-1）
func (_ *Wrapper) NodeGroups(_ context.Context, req *protos.NodeGroupsRequest) (*protos.NodeGroupsResponse, error) {
	debug(req)

	ngs := nodegroup.GetNodeGroups()
	pbNgs := make([]*protos.NodeGroup, ngs.Length())

	for i, ng := range ngs.List() {
		pbNgs[i] = pbNodeGroup(&ng)
	}

	return &protos.NodeGroupsResponse{
		NodeGroups: pbNgs,
	}, nil
}

// NodeGroupForNode is the wrapper for the cloud provider NodeGroupForNode method.
// 根据节点信息（如 ProviderID）查找所属节点组, 需正确映射节点与节点组的关系（未找到返回 NotFound）
func (_ *Wrapper) NodeGroupForNode(_ context.Context, req *protos.NodeGroupForNodeRequest) (*protos.NodeGroupForNodeResponse, error) {
	debug(req)

	node := req.GetNode()
	if node == nil {
		return nil, fmt.Errorf("request fields were nil")
	}

	ng, err := nodegroup.GetNodeGroups().FindNodeGroupByNodeName(node.Name)
	if err != nil {
		return &protos.NodeGroupForNodeResponse{}, err
	}

	return &protos.NodeGroupForNodeResponse{NodeGroup: pbNodeGroup(&ng)}, nil
}

// PricingNodePrice is the wrapper for the cloud provider Pricing NodePrice method.
// （可选）提供节点价格信息（用于成本优化）
func (_ *Wrapper) PricingNodePrice(_ context.Context, req *protos.PricingNodePriceRequest) (*protos.PricingNodePriceResponse, error) {
	debug(req)
	return &protos.PricingNodePriceResponse{}, nil
}

// PricingPodPrice is the wrapper for the cloud provider Pricing PodPrice method.
func (_ *Wrapper) PricingPodPrice(_ context.Context, req *protos.PricingPodPriceRequest) (*protos.PricingPodPriceResponse, error) {
	debug(req)
	return &protos.PricingPodPriceResponse{}, nil
}

// GPULabel is the wrapper for the cloud provider GPULabel method.
func (_ *Wrapper) GPULabel(_ context.Context, req *protos.GPULabelRequest) (*protos.GPULabelResponse, error) {
	debug(req)
	return &protos.GPULabelResponse{}, nil
}

// GetAvailableGPUTypes is the wrapper for the cloud provider GetAvailableGPUTypes method.
func (_ *Wrapper) GetAvailableGPUTypes(_ context.Context, req *protos.GetAvailableGPUTypesRequest) (*protos.GetAvailableGPUTypesResponse, error) {
	debug(req)
	return &protos.GetAvailableGPUTypesResponse{}, nil
}

// Cleanup is the wrapper for the cloud provider Cleanup method.
// 在 Autoscaler 退出时释放资源,关闭云 API 连接池等
func (_ *Wrapper) Cleanup(_ context.Context, req *protos.CleanupRequest) (*protos.CleanupResponse, error) {
	debug(req)
	return &protos.CleanupResponse{}, nil
}

// Refresh is the wrapper for the cloud provider Refresh method.
// 强制刷新所有节点组状态（如配置更新后）,在 Autoscaler 循环开始时调用
func (_ *Wrapper) Refresh(_ context.Context, req *protos.RefreshRequest) (*protos.RefreshResponse, error) {
	debug(req)
	return &protos.RefreshResponse{}, nil
}

// NodeGroupTargetSize is the wrapper for the cloud provider NodeGroup TargetSize method.
// 获取指定节点组的期望节点数（如 AWS ASG 的 DesiredCapacity)	数值必须与底层基础设施状态一致
func (_ *Wrapper) NodeGroupTargetSize(_ context.Context, req *protos.NodeGroupTargetSizeRequest) (*protos.NodeGroupTargetSizeResponse, error) {
	debug(req)

	id := req.GetId()

	size, err := nodegroup.GetNodeGroups().GetNodeGroupTargetSize(id)
	if err != nil {
		return &protos.NodeGroupTargetSizeResponse{}, err
	}
	return &protos.NodeGroupTargetSizeResponse{
		TargetSize: int32(size),
	}, nil
}

// NodeGroupIncreaseSize is the wrapper for the cloud provider NodeGroup IncreaseSize method.
// 扩容：增加节点组大小。调用云厂商接口增加节点后，应增加TargetSize并立即返回，整个过程不应超过5s
func (_ *Wrapper) NodeGroupIncreaseSize(_ context.Context, req *protos.NodeGroupIncreaseSizeRequest) (*protos.NodeGroupIncreaseSizeResponse, error) {
	debug(req)

	id := req.GetId()

	klog.V(0).Infof("got NodeGroupIncreaseSize request: nodegroup(%s) request add %d node", id, req.GetDelta())

	actuality, err := nodegroup.GetNodeGroups().NodeGroupIncreaseSize(id, int(req.GetDelta()))
	if err != nil {
		return &protos.NodeGroupIncreaseSizeResponse{}, err
	}

	klog.V(0).Infof("nodegroup(%s) request increase %d node actual increase %d node", id, req.GetDelta(), actuality)

	return &protos.NodeGroupIncreaseSizeResponse{}, err
}

// NodeGroupDeleteNodes is the wrapper for the cloud provider NodeGroup DeleteNodes method.
// 缩容：删除指定节点（CA会自己完成pod的驱逐）。这里只用调用云厂商删除instance接口，应减少TargetSize并立即返回，整个过程不应超过5s
func (_ *Wrapper) NodeGroupDeleteNodes(_ context.Context, req *protos.NodeGroupDeleteNodesRequest) (*protos.NodeGroupDeleteNodesResponse, error) {
	debug(req)

	id := req.GetId()

	nodes := make([]string, 0, 10)
	for _, v := range req.GetNodes() {
		nodes = append(nodes, v.Name)
	}

	klog.V(0).Infof("got NodeGroupDeleteNodes request: nodegroup(%s) request delete node: %v", id, nodes)

	err := nodegroup.GetNodeGroups().DeleteNodesInNodeGroup(id, nodes...)
	if err != nil {
		klog.Error(err)
		return &protos.NodeGroupDeleteNodesResponse{}, err
	}

	return &protos.NodeGroupDeleteNodesResponse{}, nil
}

// NodeGroupDecreaseTargetSize is the wrapper for the cloud provider NodeGroup DecreaseTargetSize method.
// 功能：减少目标容量（不删除节点）
// 特殊场景：节点组有扩容请求但节点尚未创建完成时，集群负载下降不再需要这些额外容量
func (_ *Wrapper) NodeGroupDecreaseTargetSize(_ context.Context, req *protos.NodeGroupDecreaseTargetSizeRequest) (*protos.NodeGroupDecreaseTargetSizeResponse, error) {
	debug(req)

	id := req.GetId()
	ngs := nodegroup.GetNodeGroups()

	klog.V(0).Infof("got NodeGroupDecreaseTargetSize request: nodegroup(%s) decrease %d node", id, req.GetDelta())

	// 如果还有未加入集群的节点则不再加入并走回收节点流程
	actuality, err := ngs.DecreaseNodeGroupTargetSize(id, int(req.GetDelta()))
	if err != nil {
		err = fmt.Errorf("nodegroup(%s) decrease %d node failed, %s", id, req.GetDelta(), err)
		klog.Error(err)
		return &protos.NodeGroupDecreaseTargetSizeResponse{}, err
	}

	klog.V(0).Infof("nodegroup(%s) request decrease %d node actual decrease %d node", id, req.GetDelta(), actuality)

	return &protos.NodeGroupDecreaseTargetSizeResponse{}, nil
}

// NodeGroupNodes is the wrapper for the cloud provider NodeGroup Nodes method.
// 获取节点组内节点详情(主要是节点的状态)
func (_ *Wrapper) NodeGroupNodes(_ context.Context, req *protos.NodeGroupNodesRequest) (*protos.NodeGroupNodesResponse, error) {
	debug(req)

	id := req.GetId()

	ng, err := nodegroup.GetNodeGroups().FindNodeGroupById(id)
	if err != nil {
		return &protos.NodeGroupNodesResponse{}, err
	}

	pbInstances := make([]*protos.Instance, 0)
	for _, ins := range ng.Instances {
		if ins.ProviderID == "" {
			continue
		}

		if ins.Stage == instance.StageDeleted {
			continue
		}

		pbInstance := new(protos.Instance)
		pbInstance.Id = ins.ProviderID
		pbInstance.Status = stateMapping(ins)
		pbInstances = append(pbInstances, pbInstance)
	}
	return &protos.NodeGroupNodesResponse{
		Instances: pbInstances,
	}, nil
}

func stateMapping(ngInstance *instance.Instance) *protos.InstanceStatus {
	pbStatus := new(protos.InstanceStatus)
	switch ngInstance.Stage {
	case instance.StagePending:
		pbStatus.InstanceState = protos.InstanceStatus_instanceCreating
	case instance.StageCreating:
		pbStatus.InstanceState = protos.InstanceStatus_instanceCreating
	case instance.StageCreated:
		pbStatus.InstanceState = protos.InstanceStatus_instanceCreating
	case instance.StageRunning:
		pbStatus.InstanceState = protos.InstanceStatus_instanceRunning
	case instance.StagePendingDeletion:
		pbStatus.InstanceState = protos.InstanceStatus_instanceDeleting
	case instance.StageDeleting:
		pbStatus.InstanceState = protos.InstanceStatus_instanceDeleting
	case instance.StageDeleted:
		pbStatus.InstanceState = protos.InstanceStatus_unspecified
	default:
		pbStatus.InstanceState = protos.InstanceStatus_unspecified
		pbStatus.ErrorInfo = &protos.InstanceErrorInfo{
			ErrorCode:    ngInstance.ErrorMsg,
			ErrorMessage: ngInstance.ErrorMsg,
		}
	}
	return pbStatus
}

// NodeGroupTemplateNodeInfo is the wrapper for the cloud provider NodeGroup TemplateNodeInfo method.
func (_ *Wrapper) NodeGroupTemplateNodeInfo(_ context.Context, req *protos.NodeGroupTemplateNodeInfoRequest) (*protos.NodeGroupTemplateNodeInfoResponse, error) {
	debug(req)
	id := req.GetId()
	ng, err := nodegroup.GetNodeGroups().FindNodeGroupById(id)
	if err != nil {
		return &protos.NodeGroupTemplateNodeInfoResponse{}, err
	}

	return &protos.NodeGroupTemplateNodeInfoResponse{
		NodeInfo: nodegroup.BuildNodeFromTemplate(&ng),
	}, nil
}

// NodeGroupGetOptions is the wrapper for the cloud provider NodeGroup GetOptions method.
func (_ *Wrapper) NodeGroupGetOptions(_ context.Context, req *protos.NodeGroupAutoscalingOptionsRequest) (*protos.NodeGroupAutoscalingOptionsResponse, error) {
	debug(req)

	pbDefaults := req.GetDefaults()
	if pbDefaults == nil {
		return nil, fmt.Errorf("request fields were nil")
	}

	id := req.GetId()
	ng, err := nodegroup.GetNodeGroups().FindNodeGroupById(id)
	if err != nil {
		return &protos.NodeGroupAutoscalingOptionsResponse{
			NodeGroupAutoscalingOptions: pbDefaults,
		}, err
	}

	if ng.AutoscalingOptions == nil {
		return &protos.NodeGroupAutoscalingOptionsResponse{
			NodeGroupAutoscalingOptions: pbDefaults,
		}, nil
	}

	return &protos.NodeGroupAutoscalingOptionsResponse{
		NodeGroupAutoscalingOptions: &protos.NodeGroupAutoscalingOptions{
			ScaleDownUtilizationThreshold:    ng.AutoscalingOptions.ScaleDownUtilizationThreshold,
			ScaleDownGpuUtilizationThreshold: ng.AutoscalingOptions.ScaleDownGpuUtilizationThreshold,
			ScaleDownUnneededTime: &metav1.Duration{
				Duration: ng.AutoscalingOptions.ScaleDownUnneededTime,
			},
			ScaleDownUnreadyTime: &metav1.Duration{
				Duration: ng.AutoscalingOptions.ScaleDownUnreadyTime,
			},
		},
	}, nil
}
