package nodegroup

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sethvargo/go-retry"
	"github.com/spf13/viper"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup/instance"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup/script_executor"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/pkg/common"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/pkg/queue"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/pkg/utils"
	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/provider"
	pcommon "github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/provider/common"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	kubeerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/utils/deletetaint"
	"k8s.io/klog/v2"
)

const (
	DefaultNodeGroup        = "default"
	ConfigMapLastUpdatedKey = "cluster-autoscaler.grpc-provider/last-updated"

	AfterCreatedScript = "after_created_hook.sh"
	BeforeDeleteScript = "before_delete_hook.sh"

	KubeNodeSshUser    = "KUBE_NODE_SSH_USER"
	KubeNodeSshPasswd  = "KUBE_NODE_SSH_PASSWD"
	KubeNodeSshKeyPath = "KUBE_NODE_SSH_KEY_PATH"
	KubeNodeName       = "NODE_NAME"
	KubeNodeIp         = "NODE_IP"
	KubeProviderId     = "PROVIDER_ID"
)

var KubeReservedTaints = []string{
	corev1.TaintNodeNotReady,
	corev1.TaintNodeUnreachable,
	corev1.TaintNodeUnschedulable,
	corev1.TaintNodeMemoryPressure,
	corev1.TaintNodeDiskPressure,
	corev1.TaintNodeNetworkUnavailable,
	corev1.TaintNodePIDPressure,
	corev1.TaintNodeOutOfService,
	deletetaint.ToBeDeletedTaint,
	deletetaint.DeletionCandidateTaint,
}

var (
	NotFoundErr        = errors.New("nodegroup not found")
	MatchedMultipleErr = errors.New("matched multiple nodegroup")

	nodeGroups = &NodeGroups{
		cache: make(nodeGroupCache, 0, 10),
		ops: &nodeGroupsOps{
			configFile:      "nodegroup-config.yaml",
			nameSpace:       "kube-system",
			statusConfigMap: "nodegroup-status",
		},
		instanceQueue: map[Stage]*queue.Queue{
			StagePending:         queue.New(),
			StageCreating:        queue.New(),
			StageCreated:         queue.New(),
			StageRunning:         queue.New(),
			StageJoined:          queue.New(),
			StagePendingDeletion: queue.New(),
			StageDeleting:        queue.New(),
			StageDeleted:         queue.New(),
		},
		matchedMultipleNodeGroup: make(map[string][]string),
	}
)

type NodeGroupsConfig struct {
	CloudProviderOption provider.CloudProviderOption `json:"cloudProviderOption" yaml:"cloudProviderOption"`
	NodeGroups          []NodeGroup                  `json:"nodeGroups" yaml:"nodeGroups"`
}

type nodeGroupCache []*NodeGroup

type NodeGroups struct {
	sync.Mutex
	instanceQueue            map[Stage]*queue.Queue
	ops                      *nodeGroupsOps
	cache                    nodeGroupCache
	matchedMultipleNodeGroup map[string][]string // 缓存哪些node匹配到了多个nodegroup
	cloudProviderOption      provider.CloudProviderOption
}

type nodeGroupsOps struct {
	configFile        string
	nameSpace         string
	statusConfigMap   string // "nodegroup-status"
	hooksPath         string
	createParallelism int
	deleteParallelism int
}

type NodeGroup struct {
	// 节点池ID
	Id string `json:"id" yaml:"id"`
	// 节点池内最小节点数量
	MinSize int `json:"minSize" yaml:"minSize"`
	// 节点池内最大节点数量
	MaxSize int `json:"maxSize" yaml:"maxSize"`
	// 节点池目标节点数量
	TargetSize int `json:"targetSize" yaml:"targetSize"`
	// 缩容的相关配置项
	AutoscalingOptions *AutoscalingOptions `json:"autoscalingOptions" yaml:"autoscalingOptions"`
	// 匹配节点组的模板
	NodeTemplate *NodeTemplate `json:"nodeTemplate" yaml:"nodeTemplate"`
	// 创建instance使用到的参数
	InstanceParameter string `json:"instanceParameter" yaml:"instanceParameter"`
	// 节点列表
	Instances InstanceList `json:"instances" yaml:"instances"`
}

type NodeTemplate struct {
	Labels      map[string]string `json:"labels" yaml:"labels"`
	Annotations map[string]string `json:"annotations" yaml:"annotations"`
	Capacity    map[string]string `json:"capacity" yaml:"capacity"`
	Allocatable map[string]string `json:"allocatable,omitempty" yaml:"allocatable"`
	Taints      []corev1.Taint    `json:"taints,omitempty" yaml:"taints"`
}

type AutoscalingOptions struct {
	ScaleDownUtilizationThreshold    float64       `json:"scaleDownUtilizationThreshold,omitempty" yaml:"scaleDownUtilizationThreshold,omitempty"`
	ScaleDownGpuUtilizationThreshold float64       `json:"scaleDownGpuUtilizationThreshold,omitempty" yaml:"scaleDownGpuUtilizationThreshold,omitempty"`
	ScaleDownUnneededTime            time.Duration `json:"scaleDownUnneededTime,omitempty" yaml:"scaleDownUnneededTime,omitempty"`
	ScaleDownUnreadyTime             time.Duration `json:"scaleDownUnreadyTime,omitempty" yaml:"scaleDownUnreadyTime,omitempty"`
}

func (c *nodeGroupCache) add(ng *NodeGroup) {
	for _, v := range *c {
		if v.Id == ng.Id {
			return
		}
	}
	*c = append(*c, ng)
}

func (c *nodeGroupCache) delete(id string) {
	for i, v := range *c {
		if v.Id == id {
			*c = append((*c)[:i], (*c)[i:]...)
			return
		}
	}
}

func (c *nodeGroupCache) find(id string) *NodeGroup {
	for i, v := range *c {
		if v.Id == id {
			return (*c)[i]
		}
	}
	return nil
}

func (ng *NodeGroup) Debug() string {
	return fmt.Sprintf("id:%s minSize:%d maxSize:%d targetSize:%d", ng.Id, ng.MinSize, ng.MaxSize, ng.TargetSize)
}

func GetNodeGroups() *NodeGroups {
	return nodeGroups
}

// Run
// watch Node 定时更新NodeGroup中Instance信息
// 定时将当前NodeGroup status持久化到config map中
// 启动Instance Controller调用云厂商接口实现对Instance控制
func (ngs *NodeGroups) Run(ctx context.Context, ops ...func() error) error {
	for _, op := range ops {
		if err := op(); err != nil {
			return err
		}
	}

	ngs.loadNodeGroups()
	// 等待node controller运行成功, 并将k8s中的Node信息同步至NodeGroup
	runNodeController(ctx)
	ngs.RefreshTargetSize()

	go ngs.runInstancesController(ctx)
	go wait.UntilWithContext(ctx, WriteNodeGroupStatusToConfigMap, time.Second*5)
	//go WatchConfigMap(ctx, ngs.ops.nameSpace, ngs.ops.statusConfigMap, SyncNodeGroupStatusFromConfigMap)

	return nil
}

func (ngs *NodeGroups) WithOpsConfigFile(f string) func() error {
	return func() error {
		ngs.ops.configFile = f
		return nil
	}
}

func (ngs *NodeGroups) WithOpsNamespace(ns string) func() error {
	return func() error {
		ngs.ops.nameSpace = ns
		return nil
	}
}

func (ngs *NodeGroups) WithOpsStatusConfigMap(cm string) func() error {
	return func() error {
		ngs.ops.statusConfigMap = cm
		return nil
	}
}

func (ngs *NodeGroups) WithOpsHooksPath(path string) func() error {
	return func() error {
		afterCreatedHook := filepath.Join(path, AfterCreatedScript)
		if _, err := os.Stat(afterCreatedHook); os.IsNotExist(err) {
			klog.Warningf("%s not exist", afterCreatedHook)
		}

		beforeDeleteHook := filepath.Join(path, BeforeDeleteScript)
		if _, err := os.Stat(afterCreatedHook); os.IsNotExist(err) {
			klog.Warningf("%s not exist", beforeDeleteHook)
		}

		ngs.ops.hooksPath = path
		return nil
	}
}

func (ngs *NodeGroups) WithCreateParallelism(para int) func() error {
	return func() error {
		ngs.ops.createParallelism = para
		return nil
	}
}

func (ngs *NodeGroups) WithDeleteParallelism(para int) func() error {
	return func() error {
		ngs.ops.deleteParallelism = para
		return nil
	}
}

func (ngs *NodeGroups) CheckKubeNodeSshUser() func() error {
	return func() error {
		if user := os.Getenv(KubeNodeSshUser); strings.TrimSpace(user) == "" {
			klog.Warningf("The environment variable '%s', "+
				"which is used for adding nodes to the Kubernetes cluster via SSH, is not set. "+
				"This will prevent the successful addition of new nodes to the Kubernetes cluster.", KubeNodeSshUser)
		}
		if passwd := os.Getenv(KubeNodeSshPasswd); strings.TrimSpace(passwd) == "" {
			klog.Warningf("The environment variable '%s', "+
				"which is used for adding nodes to the Kubernetes cluster via SSH, is not set. "+
				"This will prevent the successful addition of new nodes to the Kubernetes cluster.", KubeNodeSshPasswd)
		}

		if key := os.Getenv(KubeNodeSshKeyPath); strings.TrimSpace(key) == "" {
			klog.Warningf("The environment variable '%s', "+
				"which is used for adding nodes to the Kubernetes cluster via SSH, is not set. "+
				"This will prevent the successful addition of new nodes to the Kubernetes cluster.", KubeNodeSshKeyPath)
		}
		return nil
	}
}

func (ngs *NodeGroups) Length() int {
	return len(ngs.cache)
}

func (ngs *NodeGroups) CloudProviderOption() provider.CloudProviderOption {
	return ngs.cloudProviderOption
}

// UpdateCloudProviderAccount 没有就创建，有则更新
func (ngs *NodeGroups) UpdateCloudProviderAccount(addProviders map[string]provider.Provider) error {
	// 暂时无法校验账号的合法性
	ngs.Lock()
	existProviders := ngs.cloudProviderOption.Accounts
	for providerName, addAccounts := range addProviders {
		_, ok := existProviders[providerName]
		if !ok {
			ngs.cloudProviderOption.Accounts[providerName] = addAccounts
			continue
		}

		for accountName, credential := range addAccounts {
			ngs.cloudProviderOption.Accounts[providerName][accountName] = credential
		}
	}
	ngs.Unlock()

	// 更新到config map中
	WriteNodeGroupStatusToConfigMap(context.TODO())

	return nil
}

// DeleteCloudProviderAccount 删除云账号
func (ngs *NodeGroups) DeleteCloudProviderAccount(provider, account string) error {
	// 校验账号已不再使用
	// 1. instance的ProviderID不包含provider, account
	// 2. providerIdTemplate不包含provider, account

	ngs.Lock()
	for _, ng := range ngs.cache {
		for _, ins := range ng.Instances {
			if ins.Stage == StageDeleted || ins.Status == StatusSuccess {
				continue
			}
			insProvider, insAccount, _, _, _ := pcommon.ExtractProviderID(ins.ProviderID)
			if insProvider == provider && insAccount == account {
				ngs.Unlock()
				return fmt.Errorf("cloudProviderOption.%s.%s has used for nodegroup(%s) instance(%s)", provider, account, ng.Id, ins.ProviderID)
			}
		}
	}

	for insParaName, para := range ngs.cloudProviderOption.InstanceParameter {
		insProvider, insAccount, _, _, _ := pcommon.ExtractProviderID(para.ProviderIdTemplate)
		if insProvider == provider && insAccount == account {
			ngs.Unlock()
			return fmt.Errorf("cloudProviderOption.%s.%s has used for cloudProviderOption.instanceParameter.%s", provider, account, insParaName)
		}
	}
	ngs.Unlock()

	if _, ok := ngs.cloudProviderOption.Accounts[provider]; !ok {
		return nil
	}

	if _, ok := ngs.cloudProviderOption.Accounts[provider][account]; !ok {
		return nil
	} else {
		delete(ngs.cloudProviderOption.Accounts[provider], account)
	}

	if len(ngs.cloudProviderOption.Accounts[provider]) == 0 {
		delete(ngs.cloudProviderOption.Accounts, provider)
	}

	// 更新到config map中
	WriteNodeGroupStatusToConfigMap(context.TODO())

	return nil
}

func (ngs *NodeGroups) InstanceParameter(key string) *provider.InstanceParameter {
	if v, ok := ngs.cloudProviderOption.InstanceParameter[key]; ok {
		return &v
	}
	return nil
}

// UpdateInstanceParameter 没有就创建，有则更新
func (ngs *NodeGroups) UpdateInstanceParameter(addInsParas map[string]provider.InstanceParameter) error {
	for addInsParaName, addInsPara := range addInsParas {
		insProvider, insAccount, _, _, err := pcommon.ExtractProviderID(addInsPara.ProviderIdTemplate)
		if err != nil {
			return fmt.Errorf("unsupport providerIdTemplate format, %s", err)
		}

		if providers, ok := ngs.cloudProviderOption.Accounts[insProvider]; !ok {
			return fmt.Errorf("cloudProviderOption.accounts.%s not exist", insProvider)
		} else {
			if _, ok1 := providers[insAccount]; !ok1 {
				return fmt.Errorf("cloudProviderOption.accounts.%s.%s not exist", insProvider, insAccount)
			}
		}

		ngs.Lock()
		ngs.cloudProviderOption.InstanceParameter[addInsParaName] = addInsPara
		ngs.Unlock()
	}

	// 更新到config map中
	WriteNodeGroupStatusToConfigMap(context.TODO())

	return nil
}

// DeleteInstanceParameter 删除参数
func (ngs *NodeGroups) DeleteInstanceParameter(name string) error {
	// 校验参数已不再使用
	ngs.Lock()
	for _, ng := range ngs.cache {
		if ng.InstanceParameter == name {
			return fmt.Errorf("cloudProviderOption.instanceParameter.%s has use for nodegroup %s", name, ng.Id)
		}
	}
	delete(ngs.cloudProviderOption.InstanceParameter, name)
	ngs.Unlock()

	// 更新到config map中
	WriteNodeGroupStatusToConfigMap(context.TODO())

	return nil
}

// UpdateNodeGroup 更新
// NodeTemplate、Instances不支持热更新
func (ngs *NodeGroups) UpdateNodeGroup(newNg *NodeGroup) error {
	_, err := ngs.FindNodeGroupById(newNg.Id)
	if err != nil {
		return err
	}

	// 判断InstanceParameter是否存在
	if _, ok := ngs.cloudProviderOption.InstanceParameter[newNg.InstanceParameter]; !ok {
		return fmt.Errorf("cloudProviderOption.instanceParameter.%s not exist", newNg.InstanceParameter)
	}

	if newNg.MinSize > newNg.MaxSize {
		return fmt.Errorf("MaxSize(%d) cannot be less than MinSize(%d)", newNg.MaxSize, newNg.MinSize)
	}

	for i, v := range ngs.cache {
		if v.Id != newNg.Id {
			continue
		}
		ngs.Lock()
		ngs.cache[i].InstanceParameter = newNg.InstanceParameter
		ngs.cache[i].MinSize = newNg.MinSize
		ngs.cache[i].MaxSize = newNg.MaxSize

		if newNg.AutoscalingOptions != nil {
			ngs.cache[i].AutoscalingOptions = newNg.AutoscalingOptions
		}
		ngs.Unlock()
	}

	// 更新到config map中
	WriteNodeGroupStatusToConfigMap(context.TODO())
	return nil
}

// DeleteNodeGroup 删除NodeGroup
//func (ngs *NodeGroups) DeleteNodeGroup(id string) error {
//	// 涉及node和NodeGroup映射关系的更新
//
//	// 更新到config map中
//	WriteNodeGroupStatusToConfigMap(context.TODO())
//
//	return nil
//}

func (ngs *NodeGroups) List() []NodeGroup {
	ngs.Lock()
	defer ngs.Unlock()

	var ngCopy = make([]NodeGroup, 0, len(ngs.cache))
	for _, ng := range ngs.cache {
		ngCopy = append(ngCopy, *ng)
	}

	return ngCopy
}

// MatchNodeGroup 查找Node所属的NodeGroup
func (ngs *NodeGroups) MatchNodeGroup(node *corev1.Node) (NodeGroup, error) {
	var matchedNg []NodeGroup

	ngs.Lock()
	defer ngs.Unlock()

	genTaintKeys := func(taints []corev1.Taint) []string {
		keys := make([]string, 0, len(taints))
		for _, t := range taints {
			if utils.ElementExist(t.Key, KubeReservedTaints) {
				continue
			}
			keys = append(keys, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
		}
		return keys
	}

	for _, ng := range ngs.cache {
		if ng.Id == DefaultNodeGroup {
			continue
		}
		// 判断条件： node.label      >= nodegroup.label
		//          node.annotation >= nodegroup.annotation
		//          node.taints     <= nodegroup.taints
		if !utils.IsMapSuperset(node.Labels, ng.NodeTemplate.Labels) {
			continue
		}

		if !utils.IsMapSuperset(node.Annotations, ng.NodeTemplate.Annotations) {
			continue
		}

		if !utils.IsSubSlices(genTaintKeys(node.Spec.Taints), genTaintKeys(ng.NodeTemplate.Taints)) {
			continue
		}
		matchedNg = append(matchedNg, *ng)
	}

	switch len(matchedNg) {
	case 0:
		delete(ngs.matchedMultipleNodeGroup, node.Name)
		return *ngs.cache.find(DefaultNodeGroup), nil
	case 1:
		delete(ngs.matchedMultipleNodeGroup, node.Name)
		return matchedNg[0], nil
	default:
		var ns []string
		for _, v := range matchedNg {
			ns = append(ns, v.Id)
		}
		ngs.matchedMultipleNodeGroup[node.Name] = ns
		klog.Errorf("node(%s) matched multiple nodegroup %v", node.Name, ns)
		return matchedNg[0], MatchedMultipleErr
	}
}

func (ngs *NodeGroups) FindNodeGroupByProviderID(providerId string) (NodeGroup, error) {
	ngs.Lock()
	defer ngs.Unlock()

	var matchedNg []NodeGroup
	for _, ng := range ngs.cache {
		for _, v := range ng.Instances {
			if v.ProviderID == providerId {
				matchedNg = append(matchedNg, *ng)
			}
		}
	}

	switch len(matchedNg) {
	case 0:
		return NodeGroup{}, NotFoundErr
	case 1:
		return matchedNg[0], nil
	default:
		var ns []string
		for _, v := range matchedNg {
			ns = append(ns, v.Id)
		}
		klog.Errorf("node(%s) matched multiple nodegroup %v", providerId, ns)
		return matchedNg[0], MatchedMultipleErr
	}
}

func (ngs *NodeGroups) FindNodeGroupByNodeName(nodeName string) (NodeGroup, error) {
	ngs.Lock()
	defer ngs.Unlock()

	var matchedNg []NodeGroup
	for _, ng := range ngs.cache {
		for _, v := range ng.Instances {
			if v.Name == nodeName {
				matchedNg = append(matchedNg, *ng)
			}
		}
	}

	switch len(matchedNg) {
	case 0:
		return NodeGroup{}, NotFoundErr
	case 1:
		return matchedNg[0], nil
	default:
		var ns []string
		for _, v := range matchedNg {
			ns = append(ns, v.Id)
		}
		klog.Errorf("node(%s) matched multiple nodegroup %v", nodeName, ns)
		return matchedNg[0], MatchedMultipleErr
	}
}

func (ngs *NodeGroups) FindNodeGroupByInstanceID(id string) (NodeGroup, error) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, v := range ng.Instances {
			if v.ID == id {
				return *ng, nil
			}
		}
	}

	return NodeGroup{}, NotFoundErr
}

func (ngs *NodeGroups) FindNodeGroupById(id string /*nodegroup id*/) (NodeGroup, error) {
	ngs.Lock()
	defer ngs.Unlock()

	if ng := ngs.cache.find(id); ng != nil {
		return *ng, nil
	}

	return NodeGroup{}, NotFoundErr
}

func (ngs *NodeGroups) SetNodeGroupTargetSize(id string /*nodegroup id*/, targetSize int) error {
	ngs.Lock()
	defer ngs.Unlock()

	if ng := ngs.cache.find(id); ng == nil {
		return NotFoundErr
	} else {
		ng.TargetSize = targetSize
	}
	return nil
}

func (ngs *NodeGroups) NodeGroupIncreaseSize(id string /*nodegroup id*/, num int) (int, error) {
	ngs.Lock()
	defer ngs.Unlock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return 0, NotFoundErr
	}

	// 增加之后的TargetSize不能大于MaxSize
	increaseSize := num
	if ng.TargetSize+num > ng.MaxSize {
		increaseSize = ng.MaxSize - ng.TargetSize
		klog.Warningf("nodegroup(%s) increase instance reached MixSize(%d)", id, ng.MaxSize)
	}

	ng.TargetSize += increaseSize

	insMap := make(map[string]struct{}, 100)
	for _, n := range ngs.cache {
		for _, ins := range n.Instances {
			insMap[ins.ID] = struct{}{}
		}
	}

	for i := 0; i < increaseSize; i++ {
		insId := utils.RandStr(8)
		for {
			if _, ok := insMap[insId]; !ok {
				break
			}
			insId = utils.RandStr(8)
		}

		ng.Instances = append(ng.Instances, &Instance{
			ID:         insId, // 生成唯一ID
			Name:       "",    // instance创建好才知道
			IP:         "",    // instance创建好才知道
			ProviderID: "",    // instance创建好才知道
			Stage:      StagePending,
			Status:     StatusInit,
			Error:      "",
			UpdateTime: time.Now(),
		})
	}

	return increaseSize, nil
}

func (ngs *NodeGroups) GetNodeGroupTargetSize(id string /*nodegroup id*/) (int, error) {
	ngs.Lock()
	defer ngs.Unlock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return 0, NotFoundErr
	}
	return ng.TargetSize, nil

}

func (ngs *NodeGroups) RefreshTargetSize() {
	ngs.Lock()
	defer ngs.Unlock()

	// 刷新TargetSize
	for _, ng := range ngs.cache {
		size := 0
		for _, ins := range ng.Instances {
			if ins.Stage == StagePendingDeletion || ins.Stage == StageDeleting || ins.Stage == StageDeleted {
				continue
			}
			size++
		}
		ng.TargetSize = size
	}
}

func (ngs *NodeGroups) GetMatchedMultipleNodeGroup() map[string][]string {
	ngs.Lock()
	defer ngs.Unlock()

	return ngs.matchedMultipleNodeGroup
}

// UpdateNodeInNodeGroup 需要考虑：
// 1，首次启动时node相对应instance不存在 => 创建Instance
// 2，node标签或污点被修改导致instance所属NodeGroup发生改变
func (ngs *NodeGroups) UpdateNodeInNodeGroup(node *corev1.Node) error {
	defer ngs.RefreshTargetSize()

	// 更新NodeGroup.Instances的归属信息
	ng, err := ngs.FindNodeGroupByNodeName(node.Name)
	if err != nil {
		if errors.Is(err, NotFoundErr) {
			// 首次启动时node相对应instance不存在, 创建Instance，设置Instance的Name, ProviderID, Ip
			ng, err = ngs.MatchNodeGroup(node)
			if err != nil {
				err = fmt.Errorf("match nodegroup failed, %w", err)
				klog.Error(err)
				return err
			}

			ngs.Lock()
			if ngs.cache.find(ng.Id) == nil {
				ngs.Unlock()
				err = fmt.Errorf("NodeGroup(%s) not exist", ng.Id)
				klog.Error(err)
				return err
			} else {
				ngs.cache.find(ng.Id).Instances.Add(generateInstanceByNode(node))
				ngs.Unlock()
				klog.V(5).Infof("add node(%s) to NodeGroup(%s)", node.Name, ng.Id)
			}
			return nil
		}

		if errors.Is(err, MatchedMultipleErr) {
			// 正常不应该运行到此处
			return err
		}
	}

	// node标签或污点被修改导致instance所属NodeGroup发生改变
	matchedNg, err := ngs.MatchNodeGroup(node)
	if err != nil {
		err = fmt.Errorf("update node in nodegroup failed, %w", err)
		klog.Error(err)
	}
	// node所属nodegroup发生改变了, 删除ng中的instance，将node添加到matchedNg的instance中
	if ng.Id != matchedNg.Id {
		// 只有Running状态的Node才能调整NodeGroup，其他状态的Instance可能还没来得及打标签不能调整他所属的NodeGroup
		if ins := ngs.cache.find(ng.Id).Instances.FindByName(node.Name); ins != nil {
			if ins.Stage != StageRunning {
				return nil
			}
		}

		ngs.Lock()
		if ngs.cache.find(ng.Id) == nil {
			klog.Warningf("nodegroup(%s) not found", ng.Id)
		} else {
			ngs.cache.find(ng.Id).Instances.DeleteByName(node.Name)
		}

		if ngs.cache.find(matchedNg.Id) == nil {
			klog.Warningf("nodegroup(%s) not found", matchedNg.Id)
		} else {
			ngs.cache.find(matchedNg.Id).Instances.Add(generateInstanceByNode(node))
		}
		ngs.Unlock()
		klog.V(5).Infof("node(%s) has changed its nodegroup from %s to %s", node.Name, ng.Id, matchedNg.Id)
	}

	// instance的IP,ProviderID发生变化
	ngs.Lock()
	if ins := ngs.cache.find(matchedNg.Id).Instances.FindByName(node.Name); ins != nil {
		newIns := generateInstanceByNode(node)
		if ins.ProviderID != newIns.ProviderID || ins.IP != newIns.IP {
			ins.ProviderID = newIns.ProviderID
			ins.IP = newIns.IP
		}
	}
	ngs.Unlock()

	return nil
}

// DeleteNode 当watch到k8s中节点不存在后标记nodegroup中对应的Instance为PendingDeletion状态
func (ngs *NodeGroups) DeleteNode(nodeName string) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, ins := range ng.Instances {
			if nodeName != ins.Name {
				continue
			}

			if ins.Stage == StageDeleted {
				continue
			}

			ins.Stage = StagePendingDeletion
			ins.Status = StatusInit
			ins.Error = ""
		}
	}
}

// DeleteNodesInNodeGroupByNodeName 收到CA的删除NodeGroup中node请求，将对应的Instance标记为Deleting状态
func (ngs *NodeGroups) DeleteNodesInNodeGroupByNodeName(id string, nodeNames ...string) error {
	ngs.Lock()
	defer ngs.Unlock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return NotFoundErr
	}

	// 判断删除后的size是否小于MinSize
	maxDeletedSize := len(nodeNames)
	if ng.TargetSize-len(nodeNames) < ng.MinSize {
		maxDeletedSize = ng.TargetSize - ng.MinSize
		klog.Warningf("nodegroup(%s) delete instance reached MinSize(%d)", id, ng.MinSize)
	}

	if maxDeletedSize == 0 {
		err := fmt.Errorf("nodegroup(%s) has reached MinSize(%d)", id, ng.MinSize)
		klog.Error(err)
		return err
	}

	deleted := 0
	for _, name := range nodeNames {
		ins := ng.Instances.FindByName(name)
		if ins == nil {
			klog.Warningf("node(%s) not found in nodegroup(%s)", name, id)
			continue
		}

		if deleted >= maxDeletedSize {
			klog.Warningf("node(%s) not found in nodegroup(%s)", name, id)
			break
		}

		if ins.Stage == StagePendingDeletion || ins.Stage == StageDeleting || ins.Stage == StageDeleted {
			klog.Warningf("instance(%s) status is %s", ins.ID, ins.Stage)
			continue
		}

		klog.V(0).Infof("nodegroup(%s) node(%s) is %s and marked %s", ng.Id, ins.Name, ins.Stage, StagePendingDeletion)
		ins.Stage = StagePendingDeletion
		ins.Status = StatusInit
		ins.Error = ""
		deleted++
	}
	ng.TargetSize -= deleted
	return nil
}

// DeleteNodesInNodeGroupByProviderId 收到CA的删除NodeGroup中node请求，将对应的Instance标记为Deleting状态
func (ngs *NodeGroups) DeleteNodesInNodeGroupByProviderId(id string, providerIds ...string) error {
	ngs.Lock()
	defer ngs.Unlock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return NotFoundErr
	}

	// 判断删除后的size是否小于MinSize
	maxDeletedSize := len(providerIds)
	if ng.TargetSize-len(providerIds) < ng.MinSize {
		maxDeletedSize = ng.TargetSize - ng.MinSize
		klog.Warningf("nodegroup(%s) delete instance reached MinSize(%d)", id, ng.MinSize)
	}

	if maxDeletedSize == 0 {
		err := fmt.Errorf("nodegroup(%s) has reached MinSize(%d)", id, ng.MinSize)
		klog.Error(err)
		return err
	}

	deleted := 0
	for _, providerId := range providerIds {
		ins := ng.Instances.FindByProviderID(providerId)
		if ins == nil {
			klog.Warningf("node(%s) not found in nodegroup(%s)", providerId, id)
			continue
		}

		if deleted >= maxDeletedSize {
			klog.Warningf("node(%s) not found in nodegroup(%s)", providerId, id)
			break
		}

		if ins.Stage == StagePendingDeletion || ins.Stage == StageDeleting || ins.Stage == StageDeleted {
			klog.Warningf("instance(%s) status is %s", ins.ID, ins.Stage)
			continue
		}

		klog.V(0).Infof("nodegroup(%s) node(%s) is %s and marked %s", ng.Id, ins.Name, ins.Stage, StagePendingDeletion)
		ins.Stage = StagePendingDeletion
		ins.Status = StatusInit
		ins.Error = ""
		deleted++
	}
	ng.TargetSize -= deleted
	return nil
}

// DecreaseNodeGroupTargetSize 收到CA的调添加Node请求，减少还未创建成功的instance数量
func (ngs *NodeGroups) DecreaseNodeGroupTargetSize(id string, size int) (int, error) {
	ngs.Lock()
	defer ngs.Unlock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return 0, NotFoundErr
	}

	// 判断删除后的size是否小于MinSize
	maxDecreaseSize := size
	if ng.TargetSize-size < ng.MinSize {
		maxDecreaseSize = ng.TargetSize - ng.MinSize
		klog.Warningf("nodegroup(%s) decrease reached MinSize(%d)", id, ng.MinSize)
	}

	if maxDecreaseSize == 0 {
		return 0, nil
	}

	actuality := ng.Instances.DecreasePending(maxDecreaseSize)
	ng.TargetSize -= actuality
	return actuality, nil
}

// RemoveInstances 将instance从NodeGroup中删除
func (ngs *NodeGroups) RemoveInstances(ins ...*Instance) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, in := range ins {
		for _, ng := range ngs.cache {
			ng.Instances.Delete(in.ID)
		}
	}
}

func (ngs *NodeGroups) FindInstance(insId string) *Instance {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		if ins := ng.Instances.Find(insId); ins != nil {
			dump := *ins
			return &dump
		}
	}
	return nil
}

func generateInstanceByNode(node *corev1.Node) *Instance {
	var ins Instance

	ins.ID = utils.RandStr(8)
	ins.Name = node.Name
	ins.ProviderID = node.Spec.ProviderID

	ins.Stage = StageRunning
	ins.Status = StatusUnknown
	ins.Error = ""
	for _, v := range node.Status.Conditions {
		if v.Type == corev1.NodeReady && v.Status == corev1.ConditionTrue {
			ins.Status = StatusSuccess
			break
		}
	}
	for _, addr := range node.Status.Addresses {
		if addr.Type == "InternalIP" {
			ins.IP = addr.Address
		}
	}
	return &ins
}

func BuildNodeFromTemplate(ng *NodeGroup) *corev1.Node {
	ngName := ng.Id
	ngt := ng.NodeTemplate
	node := corev1.Node{}
	nodeName := fmt.Sprintf("%s-%d", ngName, rand.Int63())

	node.ObjectMeta = metav1.ObjectMeta{
		Name: nodeName,
		//SelfLink: fmt.Sprintf("/api/v1/nodes/%s", nodeName),
		Labels: map[string]string{},
	}

	node.Status = corev1.NodeStatus{
		Capacity: corev1.ResourceList{},
	}

	cpu, err := resource.ParseQuantity(ngt.Capacity["cpu"])
	if err != nil {
		cpu = *resource.NewQuantity(8, resource.DecimalSI)
		klog.Errorf("parse nodegroup(%s) NodeTemplate.Capacity.cpu(%s) failed, %s", ngName, ngt.Capacity["cpu"], err)
	}
	mem, err := resource.ParseQuantity(ngt.Capacity["memory"])
	if err != nil {
		mem = *resource.NewQuantity(16*1024*1024*1024, resource.DecimalSI)
		klog.Errorf("parse nodegroup(%s) NodeTemplate.Capacity.memory(%s) failed, %s", ngName, ngt.Capacity["memory"], err)
	}

	ephemeralStorage, err := resource.ParseQuantity(ngt.Capacity["ephemeral-storage"])
	if err != nil {
		ephemeralStorage = *resource.NewQuantity(200*1024*1024*1024, resource.DecimalSI)
		klog.Errorf("parse nodegroup(%s) NodeTemplate.Capacity.ephemeral-storage(%s) failed, %s", ngName, ngt.Capacity["ephemeral-storage"], err)
	}

	node.Status.Capacity[corev1.ResourcePods] = *resource.NewQuantity(110, resource.DecimalSI)
	node.Status.Capacity[corev1.ResourceCPU] = cpu
	node.Status.Capacity[corev1.ResourceMemory] = mem
	node.Status.Capacity[corev1.ResourceEphemeralStorage] = ephemeralStorage
	node.Status.Allocatable = node.Status.Capacity
	node.Labels = cloudprovider.JoinStringMaps(node.Labels, buildGenericLabels(ngt, nodeName))
	node.Annotations = ngt.Annotations
	node.Spec.Taints = ngt.Taints
	node.Status.Conditions = cloudprovider.BuildReadyConditions()
	return &node
}

func buildGenericLabels(ngt *NodeTemplate, nodeName string) map[string]string {
	result := make(map[string]string)
	result["beta.kubernetes.io/arch"] = "amd64"
	result["beta.kubernetes.io/os"] = "linux"
	result["kubernetes.io/os"] = "linux"
	//result[corev1.LabelTopologyRegion] = ngt.region
	//result[corev1.LabelTopologyZone] = ngt.zone
	result[corev1.LabelHostname] = nodeName

	// append custom node labels
	for key, value := range ngt.Labels {
		result[key] = value
	}

	return result
}

func (ngs *NodeGroups) Yaml() (string, error) {
	ngs.Lock()
	defer ngs.Unlock()

	var ngc NodeGroupsConfig
	for _, ng := range ngs.cache {
		sort.Sort(&(*ng).Instances)
		ngc.NodeGroups = append(ngc.NodeGroups, *ng)
	}
	ngc.CloudProviderOption = ngs.cloudProviderOption

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	defer func() {
		_ = encoder.Close()
	}()

	encoder.SetIndent(2)
	if err := encoder.Encode(ngc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (ngs *NodeGroups) Json() (string, error) {
	ngs.Lock()
	defer ngs.Unlock()

	var ngc NodeGroupsConfig
	for _, ng := range ngs.cache {
		sort.Sort(&(*ng).Instances)
		ngc.NodeGroups = append(ngc.NodeGroups, *ng)
	}
	ngc.CloudProviderOption = ngs.cloudProviderOption

	buf, err := json.Marshal(ngc)
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

func (ngs *NodeGroups) Status() (NodeGroupsConfig, error) {
	ngs.Lock()
	defer ngs.Unlock()

	var ngc NodeGroupsConfig
	for _, ng := range ngs.cache {
		sort.Sort(&(*ng).Instances)
		ngc.NodeGroups = append(ngc.NodeGroups, *ng)
	}
	ngc.CloudProviderOption = ngs.cloudProviderOption

	return ngc, nil
}

func WriteNodeGroupStatusToConfigMap(ctx context.Context) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	namespace := GetNodeGroups().ops.nameSpace
	configmap := GetNodeGroups().ops.statusConfigMap

	data, err := GetNodeGroups().Yaml()
	if err != nil {
		err = fmt.Errorf("marshal NodeGroup to yaml failed, %s", err)
		klog.Error(err)
		return
	}

	maps := NewKubeClient().CoreV1().ConfigMaps(namespace)
	configMap, err := maps.Get(ctx, configmap, metav1.GetOptions{})
	if err != nil && !kubeerrors.IsNotFound(err) {
		err = fmt.Errorf("get configmap(%s/%s) from apiserver failed, %s", namespace, configmap, err)
		klog.Error(err)
		return
	}

	// config map不存在则创建
	if err != nil && kubeerrors.IsNotFound(err) {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      configmap,
				Annotations: map[string]string{
					ConfigMapLastUpdatedKey: time.Now().Format(time.RFC3339),
				},
			},
			Data: map[string]string{"NodeGroupsConfig": data},
		}
		_, err = maps.Create(ctx, configMap, metav1.CreateOptions{})
		if err != nil {
			err = fmt.Errorf("create configmap(%s/%s) failed, %s", namespace, configmap, err)
			klog.Error(err)
			return
		}
		klog.V(1).Infof("create configmap(%s/%s) success", namespace, configmap)
		return
	}

	// 根据md5判断内容是否一致, 若一致则无需更新
	if md5.Sum([]byte(data)) == md5.Sum([]byte(configMap.Data["NodeGroupsConfig"])) {
		klog.V(6).Infof("configmap(%s/%s) md5 not changed", namespace, configmap)
		return
	} else {
		klog.V(5).Infof("configmap(%s/%s) md5 changed", namespace, configmap)
	}

	if configMap.ObjectMeta.Annotations == nil {
		configMap.ObjectMeta.Annotations = make(map[string]string)
	}
	configMap.Data = make(map[string]string, 2)
	configMap.Data["NodeGroupsConfig"] = data
	configMap.ObjectMeta.Annotations[ConfigMapLastUpdatedKey] = time.Now().Format(time.RFC3339)
	_, err = maps.Update(context.TODO(), configMap, metav1.UpdateOptions{})
	if err != nil {
		err = fmt.Errorf("update configmap(%s/%s) failed, %s", namespace, configmap, err)
		klog.Error(err)
		return
	}
	klog.V(3).Infof("update configmap(%s/%s) success", namespace, configmap)
	return
}

//func SyncNodeGroupStatusFromConfigMap(configMap *corev1.ConfigMap) {
//	namespace := configMap.Namespace
//	configmap := configMap.Name
//
//	// NodeGroupStatus已经发生变动，上锁禁止其他地方再修改
//	equal, err := compareConfigMapMd5(configMap)
//	if err != nil {
//		klog.Errorf("compare configmap(%s/%s) md5 failed, %s", namespace, configmap, err)
//		return
//	}
//
//	if equal {
//		klog.V(5).Infof("configmap(%s/%s) md5 not changed", namespace, configmap)
//		return
//	} else {
//		klog.V(1).Infof("configmap(%s/%s) md5 changed, need sync to NodeGroupStatus", namespace, configmap)
//	}
//
//	ngs := GetNodeGroups()
//	ngs.Lock()
//	defer ngs.Unlock()
//
//	data := configMap.Data["NodeGroupsConfig"]
//	if len(data) == 0 {
//		klog.Errorf("configmap(%s/%s) data.NodeGroupsConfig is null", namespace, configmap)
//		return
//	}
//
//	var ngc NodeGroupsConfig
//	err = yaml.Unmarshal([]byte(data), &ngc)
//	if err != nil {
//		return
//	}
//
//	ngs.cloudProviderOption = ngc.CloudProviderOption
//	ngs.cache = make(nodeGroupCache, 0, 10)
//	for _, v := range ngc.NodeGroups {
//		ng := v
//		ngs.cache.add(&ng)
//	}
//
//	klog.V(1).Infof("sync to NodeGroupStatus form configmap(%s/%s) success", namespace, configmap)
//	return
//}
//
//func compareConfigMapMd5(configMap *corev1.ConfigMap) (bool, error) {
//	namespace := configMap.Namespace
//	configmap := configMap.Name
//
//	ngs1, err := GetNodeGroups().Yaml()
//	if err != nil {
//		return false, fmt.Errorf("marshal NodeGroup to yaml failed, %s", err)
//	}
//
//	data := configMap.Data["NodeGroupsConfig"]
//	if len(data) == 0 {
//		return false, fmt.Errorf("configmap(%s/%s).NodeGroupsConfig is null", namespace, configmap)
//	}
//
//	var ngs1Copy NodeGroupsConfig
//	err = yaml.Unmarshal([]byte(ngs1), &ngs1Copy)
//	if err != nil {
//		return false, fmt.Errorf("unmarshal ngs1 to NodeGroupsConfig failed, %s", err)
//	}
//
//	var ngs2Copy NodeGroupsConfig
//	err = yaml.Unmarshal([]byte(data), &ngs2Copy)
//	if err != nil {
//		return false, fmt.Errorf("unmarshal configmap(%s/%s).NodeGroupsConfig failed, %s", namespace, configmap, err)
//	}
//
//	for i := range ngs1Copy.NodeGroups {
//		for j := range ngs1Copy.NodeGroups[i].Instances {
//			ngs1Copy.NodeGroups[i].Instances[j].UpdateTime = time.Time{}
//		}
//	}
//	for i := range ngs2Copy.NodeGroups {
//		for j := range ngs2Copy.NodeGroups[i].Instances {
//			ngs2Copy.NodeGroups[i].Instances[j].UpdateTime = time.Time{}
//		}
//	}
//
//	cleanedNgs1, err := yaml.Marshal(ngs1Copy)
//	if err != nil {
//		return false, fmt.Errorf("marshal cleaned ngs1 to yaml failed, %s", err)
//	}
//	cleanedNgs2, err := yaml.Marshal(ngs2Copy)
//	if err != nil {
//		return false, fmt.Errorf("marshal cleaned ngs2 to yaml failed, %s", err)
//	}
//	return md5.Sum(cleanedNgs1) == md5.Sum(cleanedNgs2), nil
//}

// 从config map中获取最nodegroup的状态配置
// 1, 若cm不存在则以配置文件为主
// 2, cm存在但内容为空以配置文件为主
// 3, 解析失败报错，程序无法启动
func (ngs *NodeGroups) readNodeGroupFromConfigMap() (*NodeGroupsConfig, error) {
	configMap, err := NewKubeClient().CoreV1().ConfigMaps(ngs.ops.nameSpace).Get(context.TODO(), ngs.ops.statusConfigMap, metav1.GetOptions{})
	if err != nil {
		if kubeerrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	data := configMap.Data["NodeGroupsConfig"]
	if len(data) == 0 {
		return nil, nil
	}

	var ngc NodeGroupsConfig
	err = yaml.Unmarshal([]byte(data), &ngc)
	if err != nil {
		return nil, err
	}

	return &ngc, nil
}

func (ngs *NodeGroups) readNodeGroupsFromFile() (*NodeGroupsConfig, error) {
	filePath := ngs.ops.configFile

	filename := filepath.Base(filePath)
	viper.AddConfigPath(filepath.Dir(filePath))

	f := strings.Split(filename, ".")
	switch len(f) {
	case 0, 1:
		viper.SetConfigName(filename)
	case 2:
		viper.SetConfigName(f[0])
		viper.SetConfigType(f[1])
	default:
		viper.SetConfigName(strings.Join(f[:len(f)-1], "."))
		viper.SetConfigType(f[len(f)-1])
	}

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("fatal read config from: %s", err)
	}

	var ngc NodeGroupsConfig
	if err := viper.Unmarshal(&ngc); err != nil {
		return nil, fmt.Errorf("fatal unmarshal config %s", err)
	}

	return &ngc, nil
}

func (ngs *NodeGroups) loadNodeGroups() {
	ngc, err := ngs.readNodeGroupFromConfigMap()
	if err != nil {
		klog.Fatal(err)
		return
	}

	// 从config map中获取失败，从文件中获取
	if ngc == nil {
		ngc, err = ngs.readNodeGroupsFromFile()
		if err != nil {
			klog.Fatal(err)
			return
		}
		klog.V(0).Infof("load NodeGroup from file(%s)", ngs.ops.configFile)
	} else {
		klog.V(0).Infof("load NodeGroup from config map (%s/%s)", ngs.ops.nameSpace, ngs.ops.statusConfigMap)
	}

	ngs.Lock()
	defer ngs.Unlock()
	ngs.cloudProviderOption = ngc.CloudProviderOption
	for _, v := range ngc.NodeGroups {
		ng := v
		ngs.cache.add(&ng)
	}

	// 添加兜底的节点组
	if ngs.cache.find(DefaultNodeGroup) == nil {
		ngs.cache.add(&NodeGroup{
			Id:         DefaultNodeGroup,
			MinSize:    0,
			MaxSize:    1000,
			TargetSize: 0,
			Instances:  make([]*Instance, 0),
			NodeTemplate: &NodeTemplate{
				Labels:      make(map[string]string),
				Annotations: make(map[string]string),
				Capacity: map[string]string{
					"cpu":               "8",
					"memory":            "16G",
					"ephemeral-storage": "200G",
				},
				Allocatable: map[string]string{
					"cpu":               "8",
					"memory":            "16G",
					"ephemeral-storage": "200G",
				},
				Taints: make([]corev1.Taint, 0),
			},
		})
	}
	return
}

func (ngs *NodeGroups) FilterInstanceByStages(stages ...Stage) []*Instance {
	ngs.Lock()
	defer ngs.Unlock()

	filterInstance := make([]*Instance, 0, 10)
	for _, ng := range ngs.cache {
		for _, ins := range ng.Instances {
			for _, stage := range stages {
				if ins.Stage == stage {
					incCopy := *ins
					filterInstance = append(filterInstance, &incCopy)
				}
			}
		}
	}
	return filterInstance
}

func (ngs *NodeGroups) FilterInstanceByStatus(status ...Status) []*Instance {
	ngs.Lock()
	defer ngs.Unlock()

	filterInstance := make([]*Instance, 0, 10)
	for _, ng := range ngs.cache {
		for _, ins := range ng.Instances {
			for _, s := range status {
				if ins.Status == s {
					incCopy := *ins
					filterInstance = append(filterInstance, &incCopy)
				}
			}
		}
	}
	return filterInstance
}

func (ngs *NodeGroups) FilterInstanceByStageAndStatus(stage Stage, status ...Status) []*Instance {
	ngs.Lock()
	defer ngs.Unlock()

	filterInstance := make([]*Instance, 0, 10)
	for _, ng := range ngs.cache {
		for _, ins := range ng.Instances {
			if ins.Stage != stage {
				continue
			}
			for _, s := range status {
				if ins.Status == s {
					incCopy := *ins
					filterInstance = append(filterInstance, &incCopy)
				}
			}
		}
	}
	return filterInstance
}

func (ngs *NodeGroups) UpdateInstanceStatus(ins *Instance) error {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, v := range ng.Instances {
			if v.ID == ins.ID {
				v.Stage = ins.Stage
				v.Status = ins.Status
				v.Error = ins.Error
				v.UpdateTime = time.Now()
				return nil
			}
		}
	}
	return fmt.Errorf("not found instance(Name:%s, providerID:%s)", ins.Name, ins.ProviderID)
}

func (ngs *NodeGroups) UpdateInstancesStatus(ins ...*Instance) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, cacheIns := range ng.Instances {
			for _, i := range ins {
				if cacheIns.ID == i.ID {
					cacheIns.Stage = i.Stage
					cacheIns.Status = i.Status
					cacheIns.Error = i.Error
					cacheIns.UpdateTime = time.Now()
				}
			}
		}
	}
}

func (ngs *NodeGroups) UpdateInstances(ins ...*Instance) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, cacheIns := range ng.Instances {
			for _, i := range ins {
				if cacheIns.ID == i.ID {
					cacheIns.Name = i.Name
					cacheIns.IP = i.IP
					cacheIns.ProviderID = i.ProviderID
					cacheIns.Stage = i.Stage
					cacheIns.Status = i.Status
					cacheIns.Error = i.Error
					cacheIns.UpdateTime = time.Now()
				}
			}
		}
	}
}

func (ngs *NodeGroups) runInstancesController(ctx context.Context) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	// 创建instance流程(限制创建的并发个数)
	// - 调用云厂商SDK创建instance
	// - 将Instance状态设置为Creating
	// - 根据实例ID生成instance的Id
	go wait.Until(func() { routeInstanceByStatus(ngs, StagePending /*key*/, StagePending) }, time.Second*3, ctx.Done())

	// 已经调用了云厂商接口创建了instance, 等待运行起来
	go wait.Until(func() { routeInstanceByStatus(ngs, StageCreating /*key*/, StageCreating) }, time.Second*3, ctx.Done())

	// instance状态已经运行起来，等待执行AfterCratedHook
	// - 执行AfterCreated Hook, 包含修改内核参数，修改hostname等。Hook需要支持幂等
	// - 执行kubeadm join流程
	go wait.Until(func() { routeInstanceByStatus(ngs, StageCreated /*key*/, StageCreated) }, time.Second*3, ctx.Done())

	// - 包含添加label，污点
	// - 当hook执行完成需要将instance状态更改为Running
	go wait.Until(func() { routeInstanceByStatus(ngs, StageJoined /*key*/, StageJoined) }, time.Second*3, ctx.Done())

	// 更新Instance的状态
	go wait.Until(func() { routeInstanceByStatus(ngs, StageRunning /*key*/, StageRunning, "") }, time.Minute, ctx.Done())

	// 删除instance前等待执行BeforeDeleteHook
	go wait.Until(func() { routeInstanceByStatus(ngs, StagePendingDeletion /*key*/, StagePendingDeletion) }, time.Second*3, ctx.Done())

	// BeforeDeleteHook执行成功等待调用云厂商接口删除instance
	go wait.Until(func() { routeInstanceByStatus(ngs, StageDeleting /*key*/, StageDeleting) }, time.Second*3, ctx.Done())

	// 云厂商instance成功，instance记录保留一段时间后删除记录
	go wait.Until(func() { routeInstanceByStatus(ngs, StageDeleted /*key*/, StageDeleted) }, time.Hour, ctx.Done())

	go handleRouteInstances(ctx, ngs, StagePending, ngs.ops.createParallelism, createInstance)
	go handleRouteInstances(ctx, ngs, StageCreating, ngs.ops.createParallelism, waitInstanceCreated)
	go handleRouteInstances(ctx, ngs, StageCreated, ngs.ops.createParallelism, execAfterCreatedHook)
	go handleRouteInstances(ctx, ngs, StageJoined, ngs.ops.createParallelism, waitJoined)
	go handleRouteInstances(ctx, ngs, StagePendingDeletion, ngs.ops.deleteParallelism, execBeforeDeleteHook)
	go handleRouteInstances(ctx, ngs, StageDeleting, ngs.ops.deleteParallelism, deleteInstance)
	go syncInstanceStatus(ctx, ngs)
	go removeDeletedInstances(ctx, ngs)

	return
}

func routeInstanceByStatus(ngs *NodeGroups, key Stage, status ...Stage) {
	for _, in := range ngs.FilterInstanceByStages(status...) {
		if v, ok := ngs.instanceQueue[key]; ok {
			if b := v.Add(in.ID, true); b {
				klog.V(9).Infof("add %s to instanceQueue(%s)", in.ID, key)
			} else {
				klog.V(9).Infof("add %s to instanceQueue(%s) but it existed", in.ID, key)
			}
		} else {
			klog.Errorf("instanceQueue(%s) not defined", key)
		}
	}
}

func handleRouteInstances(ctx context.Context, ngs *NodeGroups, key Stage, parallelism int, handle func(context.Context, *NodeGroups, *Instance)) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	if handle == nil {
		klog.Errorf("run %s instances status handle failde, handle is nil", key)
		return
	}

	klog.V(1).Infof("run %s instances status handle", key)

	tick := time.NewTicker(time.Second)
	for {
		select {
		case <-ctx.Done():
			klog.V(1).Infof("shutdown %s instances status handle", key)
			return
		case <-tick.C:
			// 判断当前处于InProcess的数量
			inProcessIns := ngs.FilterInstanceByStageAndStatus(key, StatusInProcess)
			// 已达到最大并发数量
			if len(inProcessIns) >= parallelism {
				continue
			}

			insIds := dumpInstancesFromQueue(ngs.instanceQueue[key], parallelism-len(inProcessIns))
			if len(insIds) == 0 {
				continue
			}

			for _, insId := range insIds {
				ins := GetNodeGroups().FindInstance(insId)
				if ins == nil || ins.Stage != key {
					continue
				}

				if ins.Status == "" || ins.Status == StatusInit {
					ins.Status = StatusInProcess
					ngs.UpdateInstancesStatus(ins)

					go handle(ctx, ngs, ins)
				}
			}

			removeInstancesFromQueue(ngs.instanceQueue[key], len(insIds))
		}
	}
}

// 从队列中获取size个元素(不会从队列中删除)
func dumpInstancesFromQueue[T string](q *queue.Queue, maxSize int) []T {
	if q == nil {
		return nil
	}

	queueLen := q.Length()
	if maxSize <= 0 || queueLen <= 0 {
		return nil
	}

	if queueLen < maxSize {
		maxSize = queueLen
	}

	ins := make([]T, 0, maxSize)
	for i := 0; i < maxSize; i++ {
		elem := q.Get(i)
		if v, ok := elem.(T); ok {
			ins = append(ins, v)
		}
	}

	return ins
}

// 从队列尾部删除count个元素
func removeInstancesFromQueue[T string](q *queue.Queue, count int) (ins []T) {
	if q == nil {
		return nil
	}

	ins = make([]T, 0, count)

	defer func() {
		recover()
	}()

	for i := 0; i < count; i++ {
		elem := q.Remove()
		if v, ok := elem.(T); ok {
			ins = append(ins, v)
		}
	}
	return ins
}

// 从云厂商更新instance状态
func syncInstanceStatus(ctx context.Context, ngs *NodeGroups) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	tick := time.NewTicker(time.Second * 5)
	for {
		select {
		case <-ctx.Done():
			klog.V(1).Infof("shutdown syncInstanceStatus")
			return
		case <-tick.C:
			insIds := removeInstancesFromQueue(ngs.instanceQueue[StageRunning], 20)
			if len(insIds) == 0 {
				continue
			}

			instances := make([]*Instance, 0, len(insIds))
			for _, insId := range insIds {
				ins := GetNodeGroups().FindInstance(insId)
				if ins == nil {
					continue
				}
				instances = append(instances, ins)
			}

			classifiedIns := pcommon.ClassifiedInstancesByProviderID(instances)
			for k, inss := range classifiedIns {
				inss = filterSyncInstanceStatus(ngs, inss)
				if len(inss) == 0 {
					continue
				}

				instancesId := make([]string, 0, len(inss))
				for _, ins := range inss {
					_, _, _, id, _ := pcommon.ExtractProviderID(ins.ProviderID)
					instancesId = append(instancesId, id)
				}

				cp, err := provider.NewCloudprovider(inss[0].ProviderID, GetNodeGroups().CloudProviderOption())
				if err != nil {
					klog.Errorf("NewCloudprovider failed, %s", err)
					continue
				}

				// 调用云厂商接口获取instance状态
				res, err := cp.InstancesStatus(ctx, instancesId...)
				if err != nil {
					klog.Errorf("sync instances status failed, %s", err)
				}

				for _, ins := range inss {
					_, _, _, id, _ := pcommon.ExtractProviderID(ins.ProviderID)
					if status, ok := res[id]; ok {
						switch status {
						case pcommon.InstanceStatusCreating:
							ins.Stage = StageCreating
							ins.Status = StatusSuccess
							ins.Error = ""
						case pcommon.InstanceStatusRunning:
							ins.Stage = StageRunning
							ins.Status = StatusSuccess
							ins.Error = ""
						case pcommon.InstanceStatusDeleted:
							ins.Stage = StageDeleted
							ins.Status = StatusSuccess
						case pcommon.InstanceStatusFailed:
							ins.Status = StatusFailed
						case pcommon.InstanceStatusUnknown:
							ins.Status = StatusUnknown
						}
					} else {
						ins.Status = StatusUnknown
						ins.Error = "unknown status"
					}

					// 查询需要一段时间防止在此时间内实例改变为其他状态
					if newIns := GetNodeGroups().FindInstance(ins.ID); newIns == nil || newIns.Stage != StageRunning {
						continue
					}

					klog.V(6).Infof("update instance(%s) staus is %s/%s", ins.ID, ins.Stage, ins.Status)
					ngs.UpdateInstancesStatus(ins)
				}
				klog.V(5).Infof("updated %d instances status by cloudprovider %s", len(inss), k)
			}
		}
	}
}

func filterSyncInstanceStatus(ngs *NodeGroups, inss []*Instance) []*Instance {
	filterIns := make([]*Instance, 0, len(inss))
	for _, ins := range inss {
		if v := ngs.FindInstance(ins.ID); v == nil {
			continue
		} else if v.Stage != StageRunning {
			continue
		}

		if ins.Status == StatusInit || ins.Status == StatusUnknown || ins.UpdateTime.Add(time.Minute*5).Before(time.Now()) {
			filterIns = append(filterIns, ins)
		}
	}
	return filterIns
}

// 查询最instance的最新状态以确认是否可以进行接下来的动作
func handleConfirm(ngs *NodeGroups, ins *Instance, stage Stage, status Status) (*Instance, error) {
	if v := ngs.FindInstance(ins.ID); v == nil {
		return ins, fmt.Errorf("instance(%s) not found", ins.ID)
	} else if v.Stage != stage {
		return ins, fmt.Errorf(" instance(%s) stage changed from %s to %s", ins.ID, stage, v.Stage)
	} else if v.Status != status {
		return ins, fmt.Errorf("instance(%s) status changed from %s to %s", ins.ID, status, v.Status)
	} else {
		return v, nil
	}
}

// 调用云厂商接口创建instance
func createInstance(ctx context.Context, ngs *NodeGroups, ins *Instance) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	var err error
	if ins, err = handleConfirm(ngs, ins, StagePending, StatusInProcess); err != nil {
		klog.Warningf("confirm createInstance not paas, %s", err)
		return
	}

	ng, err := GetNodeGroups().FindNodeGroupByInstanceID(ins.ID)
	if err != nil {
		err = fmt.Errorf("find nodegroup by instance ID failed, %s", err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	insParam := GetNodeGroups().InstanceParameter(ng.InstanceParameter)
	if insParam == nil {
		err = fmt.Errorf("nodegroup(%s).InstanceParameter[%s] not exist", ng.Id, ng.InstanceParameter)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	cp, err := provider.NewCloudprovider(insParam.ProviderIdTemplate, GetNodeGroups().CloudProviderOption())
	if err != nil {
		err = fmt.Errorf("NewCloudprovider failed, %s", err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	klog.V(1).Infof("creatting instance(%s)...", ins.ID)

	ctx1, cancel := context.WithTimeout(context.TODO(), time.Second*60)
	insId, err := cp.CreateInstance(ctx1, insParam.Parameter)
	cancel()
	if err != nil {
		err = fmt.Errorf("create instance(%s) failed, %s", ins.ID, err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
	} else {
		providerName, account, region, _, _ := pcommon.ExtractProviderID(insParam.ProviderIdTemplate)
		ins.ProviderID = pcommon.GenerateInstanceProviderID(providerName, account, region, insId)
		ins.Stage = StageCreating
		ins.Status = StatusInit
		ins.Error = ""
		klog.V(1).Infof("create instance(%s) success, ProviderID:%s", ins.ID, ins.ProviderID)
		ngs.UpdateInstances(ins)
	}
}

// 等待instance状态正常
func waitInstanceCreated(ctx context.Context, ngs *NodeGroups, ins *Instance) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	var err error
	if ins, err = handleConfirm(ngs, ins, StageCreating, StatusInProcess); err != nil {
		klog.Warningf("confirm waitInstanceCreated not paas, %s", err)
		return
	}

	cp, err := provider.NewCloudprovider(ins.ProviderID, GetNodeGroups().CloudProviderOption())
	if err != nil {
		err = fmt.Errorf("wait instance(%s) created failed, %s", ins.ID, err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	_, _, _, insId, _ := pcommon.ExtractProviderID(ins.ProviderID)
	status, err := cp.InstanceStatus(ctx, insId)
	if err != nil {
		err = fmt.Errorf("wait instance(%s) created failed, %s", ins.ID, err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	if status != pcommon.InstanceStatusRunning {
		ins.Status = StatusInit
		ins.Error = ""
		ngs.UpdateInstancesStatus(ins)
		return
	}

	ip, err := cp.InstanceIp(ctx, insId)
	if err != nil {
		err = fmt.Errorf("wait instance(%s) ip failed, %s", ins.ID, err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	if ip == "" {
		err = fmt.Errorf("wait instance(%s) ip failed, ip is null", ins.ID)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		klog.Error(err)
		return
	}

	ins.IP = ip
	ins.Stage = StageCreated
	ins.Status = ""
	ins.Error = ""
	if ins.Name == "" && ins.IP != "" {
		ins.Name = generateInstanceName(ins.IP)
	}
	ngs.UpdateInstances(ins)
	klog.V(1).Infof("wait instance(%s) created success", ins.ID)
}

func generateInstanceName(ip string) string {
	// node-y4znao.vm1-2-3-4
	return fmt.Sprintf("node-%s.vm%s", utils.RandStr(6), strings.ReplaceAll(ip, ".", "-"))
}

// 执行AfterCreated Hook
// - 修改host name
// - 调整内核参数
func execAfterCreatedHook(ctx context.Context, ngs *NodeGroups, ins *Instance) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	var err error
	if ins, err = handleConfirm(ngs, ins, StageCreated, StatusInProcess); err != nil {
		klog.Warningf("confirm execAfterCreatedHook not paas, %s", err)
		return
	}

	start := time.Now()
	klog.V(1).Infof("exec %s for intance(%s)...", AfterCreatedScript, ins.ID)
	err = execHookScript(ctx, ngs, ins, AfterCreatedScript, false)
	if err != nil {
		err = fmt.Errorf("exec intance(%s) execHookScript failed, cost:%v, %s", ins.ID, time.Since(start), err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		klog.Error(err)
	} else {
		ins.Stage = StageJoined
		ins.Status = StatusInit
		ins.Error = ""
		klog.V(1).Infof("exec %s for intance(%s) success, cost:%v", AfterCreatedScript, ins.ID, time.Since(start))
	}

	// 更新Instance信息
	ngs.UpdateInstancesStatus(ins)
}

// waitJoined等待在k8s集群就绪
func waitJoined(ctx context.Context, ngs *NodeGroups, ins *Instance) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	var err error
	if ins, err = handleConfirm(ngs, ins, StageJoined, StatusInProcess); err != nil {
		klog.Warningf("confirm waitJoined not paas, %s", err)
		return
	}

	_, err = NewKubeClient().CoreV1().Nodes().Get(ctx, ins.Name, metav1.GetOptions{})
	if err != nil {
		ins.Status = StatusInProcess
		ins.Error = err.Error()
		ngs.UpdateInstancesStatus(ins)
		return
	}

	start := time.Now()
	klog.V(1).Infof("patch lable for intance(%s)...", ins.ID)

	// 设置标签和污点, 需要等待
	err = patchNodeNodeGroupTemplate(ctx, ngs, ins)
	if err != nil {
		err = fmt.Errorf("patch lable for intance(%s) failed, cost:%v, %s", ins.ID, time.Since(start), err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		klog.Error(err)
	} else {
		ins.Stage = StageRunning
		ins.Status = StatusInit
		ins.Error = ""
		klog.V(1).Infof("patch lable for intance(%s) success, cost:%v", ins.ID, time.Since(start))
	}

	// 更新Instance信息
	ngs.UpdateInstancesStatus(ins)
}

// BeforeDelete Hook
func execBeforeDeleteHook(ctx context.Context, ngs *NodeGroups, ins *Instance) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	var err error
	if ins, err = handleConfirm(ngs, ins, StagePendingDeletion, StatusInProcess); err != nil {
		klog.Warningf("confirm execBeforeDeleteHook not paas, %s", err)
		return
	}

	start := time.Now()
	klog.V(1).Infof("exec %s for intance(%s)...", BeforeDeleteScript, ins.ID)
	err = execHookScript(ctx, ngs, ins, BeforeDeleteScript, false)
	if err != nil {
		err = fmt.Errorf("exec intance(%s) execHookScript failed, cost:%v, %s", ins.ID, time.Since(start), err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		klog.Error(err)
	} else {
		ins.Stage = StageDeleting
		ins.Status = StatusInit
		ins.Error = ""
		klog.V(1).Infof("exec %s for intance(%s) success, cost:%v", BeforeDeleteScript, ins.ID, time.Since(start))
	}
	ngs.UpdateInstancesStatus(ins)
}

// deleteInstance 调用云厂商接口删除instance
func deleteInstance(ctx context.Context, ngs *NodeGroups, ins *Instance) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	var err error
	if ins, err = handleConfirm(ngs, ins, StageDeleting, StatusInProcess); err != nil {
		klog.Warningf("confirm deleteInstance not paas, %s", err)
		return
	}

	klog.V(1).Infof("delete intance(%s)...", ins.ID)

	ctx1, cancel := context.WithTimeout(context.TODO(), time.Second*60)
	err = callDeleteInstance(ctx1, ngs, ins)
	defer cancel()
	if err != nil {
		err = fmt.Errorf("delete intance(%s) failed, %s", ins.ID, err)
		ins.Status = StatusFailed
		ins.Error = err.Error()
		klog.Error(err)
	} else {
		klog.V(1).Infof("delete intance(%s) success", ins.ID)

		err = DeleteNodeFromKubernetes(ctx1, ins.Name)
		if err != nil {
			err = fmt.Errorf("delete intance(%s) from kubernets failed, %s", ins.ID, err)
			ins.Status = StatusFailed
			ins.Error = err.Error()
			klog.Error(err)
		} else {
			// 将instance标记为已删除，等待7天或15天才把instance从记录里删除
			ins.Stage = StageDeleted
			ins.Status = StatusSuccess
			ins.Error = ""
			klog.V(1).Infof("delete intance(%s) from kubernets success", ins.ID)
		}
	}
	ngs.UpdateInstancesStatus(ins)
}

// removeDeletedInstances 移除之前已删除的instance
func removeDeletedInstances(ctx context.Context, ngs *NodeGroups) {
	common.ContextWaitGroupAdd(ctx, 1)
	defer common.ContextWaitGroupDone(ctx)

	tick := time.NewTicker(time.Minute)
	for {
		select {
		case <-ctx.Done():
			klog.V(1).Infof("shutdown removeDeletedInstances")
			return
		case <-tick.C:
			insIds := removeInstancesFromQueue(ngs.instanceQueue[StageDeleted], 10)
			if len(insIds) == 0 {
				continue
			}

			instances := make([]*Instance, 0, len(insIds))
			for _, insId := range insIds {
				ins := GetNodeGroups().FindInstance(insId)
				if ins == nil {
					continue
				}
				instances = append(instances, ins)
			}

			for _, ins := range instances {
				if time.Now().After(ins.UpdateTime.Add(time.Hour * 24 * 15)) {
					ngs.RemoveInstances(ins)
					klog.V(1).Infof("remove intance(%s) success", ins.ID)
				}
			}
		}
	}
}

// 调用云厂商接口删除instance
func callDeleteInstance(ctx context.Context, _ *NodeGroups, ins *Instance) error {
	cp, err := provider.NewCloudprovider(ins.ProviderID, GetNodeGroups().CloudProviderOption())
	if err != nil {
		return fmt.Errorf("call provider delete instance(%s) failed, %s", ins.ID, err)
	}

	klog.V(1).Infof("call provider delete instance(%s)...", ins.ID)

	_, _, _, insId, _ := pcommon.ExtractProviderID(ins.ProviderID)
	err = cp.DeleteInstance(ctx, insId, nil)
	if err != nil {
		return fmt.Errorf("call provider delete instance(%s) failed, %s", ins.ID, err)
	}

	return nil
}

func execHookScript(ctx context.Context, ngs *NodeGroups, ins *Instance, script string, force bool) error {
	local := path.Join(ngs.ops.hooksPath, script)
	remoteBaseDir := fmt.Sprintf("/home/%s/kube-node", os.Getenv(KubeNodeSshUser))
	remote := fmt.Sprintf("%s/hooks/%s", remoteBaseDir, script)
	envPath := fmt.Sprintf("%s/hooks/env", remoteBaseDir)
	logPath := strings.TrimSuffix(remote, ".sh") + ".log"

	var err error
	var executor *script_executor.ScriptExecutor
	if err = retry.Do(ctx, retry.WithMaxRetries(3, retry.NewExponential(time.Second*5)),
		func(_ context.Context) error {
			executor, err = script_executor.NewScriptExecutor(generateSshInfo(ins))
			if err != nil {
				return retry.RetryableError(fmt.Errorf("NewScriptExecutor for %s failed, %s", script, err))
			}
			return nil
		}); err != nil {
		klog.Error(err)
		return err
	}

	if force {
		if err = executor.RemoveAll(remoteBaseDir); err != nil {
			err = fmt.Errorf("remove %s failed, %s", remoteBaseDir, err)
			klog.Error(err)
			return err
		}
	}

	// 确保不会被重复执行
	exist, err := executor.FileExist(remote)
	if err != nil {
		err = fmt.Errorf("unable to determine if %s has been executed, %s", script, err)
		klog.Error(err)
		return err
	} else if exist {
		err = fmt.Errorf("hook %s has been executed, you can remove %s and retry", script, remoteBaseDir)
		klog.Error(err)
		return err
	}

	// 注入环境变量
	err = executor.WriteFile(strings.NewReader(generateEnv(ins)), envPath)
	if err != nil {
		err = fmt.Errorf("write AfterCreatedHook env to %s failed, %s", envPath, err)
		klog.Error(err)
		return err
	}

	// 将hook脚本拷贝至目标节点
	if err = executor.CopyFile(local, remote); err != nil {
		err = fmt.Errorf("copy %s from %s to %s failed, %s", script, local, remote, err)
		klog.Error(err)
		return err
	}

	// 脚本添加执行权限
	if _, err = executor.RunCommand(ctx, "chmod +x "+remote); err != nil {
		err = fmt.Errorf("make %s executable failed, %s", remote, err)
		klog.Error(err)
		return err
	}

	// 执行脚本
	ctx1, cancel := context.WithTimeout(ctx, time.Minute*15)
	defer cancel()
	output, err := executor.ExecuteScript(ctx1, fmt.Sprintf(". %s && %s", envPath, remote), logPath)
	if err != nil {
		err = fmt.Errorf("exec %s on %s failed, %s", remote, ins.IP, err)
		klog.Error(err)
		klog.Infof(output)
		return err
	}
	klog.V(3).Infof("script %s ouput:\n%s", script, output)

	return nil
}

func generateSshInfo(ins *Instance) (ip, port, user, passwd, key string) {
	return ins.IP, "22", os.Getenv(KubeNodeSshUser), os.Getenv(KubeNodeSshPasswd), os.Getenv(KubeNodeSshKeyPath)
}

func generateEnv(ins *Instance) string {
	var buffer bytes.Buffer
	buffer.WriteString(fmt.Sprintf("export %s=%s\n", KubeNodeIp, ins.IP))
	buffer.WriteString(fmt.Sprintf("export %s=%s\n", KubeNodeName, ins.Name))
	buffer.WriteString(fmt.Sprintf("export %s=%s\n", KubeProviderId, ins.ProviderID))
	return buffer.String()
}
