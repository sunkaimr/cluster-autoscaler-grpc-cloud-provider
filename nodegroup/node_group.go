package nodegroup

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/instance"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/script_executor"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/pkg/queue"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/pkg/utils"
	"github.com/sunkaimr/cluster-autoscaler-grpc-provider/provider"
	pcommon "github.com/sunkaimr/cluster-autoscaler-grpc-provider/provider/common"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	kubeerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
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

var (
	NotFoundErr        = errors.New("not found nodegroup")
	MatchedMultipleErr = errors.New("matched multiple nodegroup")

	nodeGroups = &NodeGroups{
		cache: make(nodeGroupCache, 0, 10),
		ops: &nodeGroupsOps{
			configFile:      "nodegroup-config.yaml",
			nameSpace:       "kube-system",
			statusConfigMap: "nodegroup-status",
		},
		instanceQueue: map[Status]*queue.Queue{
			StatusPending:         queue.New(),
			StatusCreating:        queue.New(),
			StatusCreated:         queue.New(),
			StatusRunning:         queue.New(),
			StatusPendingDeletion: queue.New(),
			StatusDeleting:        queue.New(),
			StatusDeleted:         queue.New(),
			StatusFailed:          queue.New(),
			StatusUnknown:         queue.New(),
		},
	}
)

type NodeGroupsConfig struct {
	CloudProviderOption provider.CloudProviderOption `json:"cloudProviderOption" yaml:"cloudProviderOption"`
	NodeGroups          []NodeGroup                  `json:"nodeGroups" yaml:"nodeGroups"`
}

type nodeGroupCache []*NodeGroup

type NodeGroups struct {
	sync.Mutex
	instanceQueue       map[Status]*queue.Queue
	ops                 *nodeGroupsOps
	cache               nodeGroupCache
	cloudProviderOption provider.CloudProviderOption
}

type nodeGroupsOps struct {
	configFile      string
	nameSpace       string
	statusConfigMap string // "nodegroup-status"
	hooksPath       string
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
	// 匹配节点组的模板
	NodeTemplate NodeTemplate `json:"nodeTemplate" yaml:"nodeTemplate"`
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
	return fmt.Sprintf("ID:%s MinSize:%d MaxSize:%d TargetSize:%d", ng.Id, ng.MinSize, ng.MaxSize, ng.TargetSize)
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
	runNodeController(ctx)

	go ngs.SyncInstances(ctx)
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

func (ngs *NodeGroups) InstanceParameter(key string) *provider.InstanceParameter {
	if v, ok := ngs.cloudProviderOption.InstanceParameter[key]; ok {
		return &v
	}
	return nil
}

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
	var matchNg []NodeGroup

	ngs.Lock()
	defer ngs.Unlock()

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

		genNodeTaintKeys := func(node *corev1.Node) []string {
			keys := make([]string, 0, len(node.Spec.Taints))
			for _, t := range node.Spec.Taints {
				keys = append(keys, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
			}
			return keys
		}

		genNodeGroupTaintKeys := func(ng *NodeGroup) []string {
			keys := make([]string, 0, len(ng.NodeTemplate.Taints))
			for _, t := range ng.NodeTemplate.Taints {
				keys = append(keys, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
			}
			return keys
		}

		if !utils.IsSubSlices(genNodeTaintKeys(node), genNodeGroupTaintKeys(ng)) {
			continue
		}
		matchNg = append(matchNg, *ng)
	}

	switch len(matchNg) {
	case 0:
		return *ngs.cache.find(DefaultNodeGroup), nil
	case 1:
		return matchNg[0], nil
	default:
		var ns []string
		for _, v := range matchNg {
			ns = append(ns, v.Id)
		}
		return matchNg[0], fmt.Errorf("node(%s) belongs to multiple nodegroups %v, %w", node.Name, ns, MatchedMultipleErr)
	}
}

func (ngs *NodeGroups) FindNodeGroupByNodeName(nodeName string) (NodeGroup, error) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, v := range ng.Instances {
			if v.Name == nodeName {
				return *ng, nil
			}
		}
	}

	return NodeGroup{}, NotFoundErr
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

	for i := 0; i < int(increaseSize); i++ {
		ng.Instances = append(ng.Instances, &Instance{
			ID:         utils.RandStr(8),
			Name:       "", // instance创建好才知道
			IP:         "", // instance创建好才知道
			ProviderID: "", // instance创建好才知道
			Status:     StatusPending,
			UpdateTime: time.Now(),
		})
	}

	return increaseSize, nil
}

func (ngs *NodeGroups) GetNodeGroupTargetSize(id string /*nodegroup id*/) (int, error) {
	ngs.Lock()
	defer ngs.Unlock()

	if ng := ngs.cache.find(id); ng == nil {
		return 0, NotFoundErr
	} else {
		return ng.TargetSize, nil
	}
}

// UpdateNodeInNodeGroup 需要考虑的几个事情：
// 1，首次启动时node相对应instance不存在 => 创建Instance
// 2，node标签或污点被修改导致instance所属NodeGroup发生改变
func (ngs *NodeGroups) UpdateNodeInNodeGroup(node *corev1.Node) error {
	// 更新NodeGroup.Nodes节点信息

	ng, err := ngs.FindNodeGroupByNodeName(node.Name)
	if err != nil && errors.Is(err, NotFoundErr) {
		// 首次启动时node相对应instance不存在
		// 创建Instance，设置Instance的Nam, ProviderID, Ip
		ng, err = ngs.MatchNodeGroup(node)
		if err != nil {
			// 找到了多个怎么办
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
		}
		return nil
	}

	// node标签或污点被修改导致instance所属NodeGroup发生改变
	matchedNg, err := ngs.MatchNodeGroup(node)
	if err != nil {
		err = fmt.Errorf("update node in nodegroup failed, %w", err)
		klog.Error(err)
	}
	// node所属nodegroup发生改变了, 删除ng中的instance，将node添加到matchedNg的instance中
	if ng.Id != matchedNg.Id {
		ngs.Lock()
		if ngs.cache.find(ng.Id) != nil {
			klog.Warningf("nodegroup(%s) not found", ng.Id)
		} else {
			ngs.cache.find(ng.Id).Instances.Delete(node.Name)
		}

		if ngs.cache.find(matchedNg.Id) != nil {
			klog.Warningf("nodegroup(%s) not found", matchedNg.Id)
		} else {
			ngs.cache.find(matchedNg.Id).Instances.Add(generateInstanceByNode(node))
		}
		ngs.Unlock()
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

			if ins.Status == StatusDeleted {
				continue
			}

			ins.Status = StatusPendingDeletion
		}
	}
}

// DeleteNodesInNodeGroup 收到CA的删除NodeGroup中node请求，将对应的Instance标记为Deleting状态
func (ngs *NodeGroups) DeleteNodesInNodeGroup(id string, nodeNames ...string) error {
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

		//if ins.Status == StatusDeleted {
		//	klog.Warningf("nodegroup(%s) node(%s) status is Deleted",ID, name)
		//	continue
		//}

		ins.Status = StatusPendingDeletion
		ins.ErrorMsg = ""
		klog.V(0).Infof("nodegroup(%s) node(%s) is %s and marked PendingDeletion", ng.Id, ins.Name, ins.Status)
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

func generateInstanceByNode(node *corev1.Node) *Instance {
	var ins Instance

	ins.ID = utils.RandStr(8)
	ins.Name = node.Name
	ins.ProviderID = node.Spec.ProviderID
	ins.Status = StatusUnknown
	for _, addr := range node.Status.Addresses {
		if addr.Type == "InternalIP" {
			ins.IP = addr.Address
		}
	}
	return &ins
}

func BuildNodeFromTemplate(ng *NodeGroup) *corev1.Node {
	ngName := ng.Id
	ngt := &ng.NodeTemplate
	node := corev1.Node{}
	nodeName := fmt.Sprintf("%s-%d", ngName, rand.Int63())

	node.ObjectMeta = metav1.ObjectMeta{
		Name:     nodeName,
		SelfLink: fmt.Sprintf("/api/v1/nodes/%s", nodeName),
		Labels:   map[string]string{},
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

func (ngs *NodeGroups) toYaml() (string, error) {
	ngs.Lock()
	defer ngs.Unlock()

	var ngc NodeGroupsConfig
	for _, ng := range ngs.cache {
		ngc.NodeGroups = append(ngc.NodeGroups, *ng)
	}
	ngc.CloudProviderOption = ngs.cloudProviderOption

	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	defer encoder.Close()

	encoder.SetIndent(2)
	if err := encoder.Encode(ngc); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func WriteNodeGroupStatusToConfigMap(ctx context.Context) {
	ctx.Value("wg").(*sync.WaitGroup).Add(1)
	defer ctx.Value("wg").(*sync.WaitGroup).Done()

	namespace := GetNodeGroups().ops.nameSpace
	configmap := GetNodeGroups().ops.statusConfigMap

	data, err := GetNodeGroups().toYaml()
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
		klog.V(5).Infof("configmap(%s/%s) md5 not changed", namespace, configmap)
		return
	} else {
		klog.V(3).Infof("configmap(%s/%s) md5 changed", namespace, configmap)
	}

	if configMap.ObjectMeta.Annotations == nil {
		configMap.ObjectMeta.Annotations = make(map[string]string)
	}
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

func SyncNodeGroupStatusFromConfigMap(configMap *corev1.ConfigMap) {
	namespace := configMap.Namespace
	configmap := configMap.Name

	// NodeGroupStatus已经发生变动，上锁禁止其他地方再修改
	equel, err := compareConfigMapMd5(configMap)
	if err != nil {
		klog.Errorf("compare configmap(%s/%s) md5 failed, %s", namespace, configmap, err)
		return
	}

	if equel {
		klog.V(5).Infof("configmap(%s/%s) md5 not changed", namespace, configmap)
		return
	} else {
		klog.V(1).Infof("configmap(%s/%s) md5 changed, need sync to NodeGroupStatus", namespace, configmap)
	}

	ngs := GetNodeGroups()
	ngs.Lock()
	defer ngs.Unlock()

	data := configMap.Data["NodeGroupsConfig"]
	if len(data) == 0 {
		klog.Errorf("configmap(%s/%s) data.NodeGroupsConfig is null", namespace, configmap)
		return
	}

	var ngc NodeGroupsConfig
	err = yaml.Unmarshal([]byte(data), &ngc)
	if err != nil {
		return
	}

	// TODO 是否再这里做校验
	ngs.cloudProviderOption = ngc.CloudProviderOption
	ngs.cache = make(nodeGroupCache, 0, 10)
	for _, v := range ngc.NodeGroups {
		ng := v
		ngs.cache.add(&ng)
	}

	klog.V(1).Infof("sync to NodeGroupStatus form configmap(%s/%s) success", namespace, configmap)
	return
}

// TODO 是否在此处限制configmap哪些字段可以修改，哪些字段不能修改
func compareConfigMapMd5(configMap *corev1.ConfigMap) (bool, error) {
	namespace := configMap.Namespace
	configmap := configMap.Name

	ngs1, err := GetNodeGroups().toYaml()
	if err != nil {
		return false, fmt.Errorf("marshal NodeGroup to yaml failed, %s", err)
	}

	data := configMap.Data["NodeGroupsConfig"]
	if len(data) == 0 {
		return false, fmt.Errorf("configmap(%s/%s).NodeGroupsConfig is null", namespace, configmap)
	}

	var ngs1Copy NodeGroupsConfig
	err = yaml.Unmarshal([]byte(ngs1), &ngs1Copy)
	if err != nil {
		return false, fmt.Errorf("unmarshal ngs1 to NodeGroupsConfig failed, %s", err)
	}

	var ngs2Copy NodeGroupsConfig
	err = yaml.Unmarshal([]byte(data), &ngs2Copy)
	if err != nil {
		return false, fmt.Errorf("unmarshal configmap(%s/%s).NodeGroupsConfig failed, %s", namespace, configmap, err)
	}

	for i := range ngs1Copy.NodeGroups {
		for j := range ngs1Copy.NodeGroups[i].Instances {
			ngs1Copy.NodeGroups[i].Instances[j].UpdateTime = time.Time{}
		}
	}
	for i := range ngs2Copy.NodeGroups {
		for j := range ngs2Copy.NodeGroups[i].Instances {
			ngs2Copy.NodeGroups[i].Instances[j].UpdateTime = time.Time{}
		}
	}

	cleanedNgs1, err := yaml.Marshal(ngs1Copy)
	if err != nil {
		return false, fmt.Errorf("marshal cleaned ngs1 to yaml failed, %s", err)
	}
	cleanedNgs2, err := yaml.Marshal(ngs2Copy)
	if err != nil {
		return false, fmt.Errorf("marshal cleaned ngs2 to yaml failed, %s", err)
	}
	return md5.Sum(cleanedNgs1) == md5.Sum(cleanedNgs2), nil
}

// 从config map中获取最nodegroup的状态配置
// 1, 若cm不存在则以配置文件为主
// 2, cm存在但内容为空以配置文件为主
// 3, 解析失败报错，程序无法启动
func (ngs *NodeGroups) readStatusFromConfigMap() (*NodeGroupsConfig, error) {
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

	path := filepath.Dir(filePath)
	filename := filepath.Base(filePath)

	viper.AddConfigPath(path)

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
	ngc, err := ngs.readStatusFromConfigMap()
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
			NodeTemplate: NodeTemplate{
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

func (ng *NodeGroup) FilterInstanceByStatus(status ...Status) []*Instance {
	filterInstance := make([]*Instance, 0, 10)
	for _, ins := range ng.Instances {
		for _, s := range status {
			if ins.Status == s {
				incCopy := *ins
				filterInstance = append(filterInstance, &incCopy)
			}
		}
	}
	return filterInstance
}

func (ngs *NodeGroups) FilterInstanceByStatus(status ...Status) []*Instance {
	filterInstance := make([]*Instance, 0, 10)
	for _, ng := range ngs.List() {
		ngs.Lock()
		filterInstance = append(filterInstance, ng.FilterInstanceByStatus(status...)...)
		ngs.Unlock()
	}
	// 未去重
	return filterInstance
}

func (ngs *NodeGroups) UpdateInstanceStatus(ins *Instance) error {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, v := range ng.Instances {
			if v.ID == ins.ID {
				v.Status = ins.Status
				v.ErrorMsg = ins.ErrorMsg
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
					cacheIns.Status = i.Status
					cacheIns.ErrorMsg = i.ErrorMsg
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
					cacheIns.Status = i.Status
					cacheIns.ErrorMsg = i.ErrorMsg
					cacheIns.UpdateTime = time.Now()
				}
			}
		}
	}
}

func (ngs *NodeGroups) SyncInstances(ctx context.Context) {
	ctx.Value("wg").(*sync.WaitGroup).Add(1)
	defer ctx.Value("wg").(*sync.WaitGroup).Done()

	// 创建instance流程(限制创建的并发个数)
	// - 调用云厂商SDK创建instance
	// - 将Instance状态设置为Creating
	// - 根据实例ID生成instance的Id
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusPending, StatusPending) }, time.Second*10, ctx.Done())

	// 已经调用了云厂商接口创建了instance, 等待运行起来
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusCreating, StatusCreating) }, time.Second*5, ctx.Done())

	// instance状态已经运行起来，等待执行AfterCratedHook
	// - 执行AfterCreated Hook, 包含修改内核参数，修改hostname等。Hook需要支持幂等
	// - 执行kubeadm join流程
	// - 包含添加label，污点，注册CMDB等
	// - 当hook执行完成需要将instance状态更改为Running
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusCreated, StatusCreated) }, time.Second*5, ctx.Done())

	// 更新Instance的状态
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusRunning, StatusRunning, StatusUnknown, "") }, time.Minute*5, ctx.Done())

	// 删除instance前等待执行BeforeDeleteHook
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusPendingDeletion, StatusPendingDeletion) }, time.Second*5, ctx.Done())

	// BeforeDeleteHook执行成功等待调用云厂商接口删除instance
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusDeleting, StatusDeleting) }, time.Second*5, ctx.Done())

	// 云厂商instance成功，instance记录保留一段时间后删除记录
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusDeleted, StatusDeleted) }, time.Minute, ctx.Done())

	// 定时报告状态是Failed的instance
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusFailed, StatusFailed) }, time.Minute, ctx.Done())

	go handleRouteInstances(ctx, ngs, StatusPending, 1, 3, createInstance)
	go handleRouteInstances(ctx, ngs, StatusCreating, 1, 10, waitInstanceCreated)
	go handleRouteInstances(ctx, ngs, StatusCreated, 1, 3, execAfterCreatedHook)
	go handleRouteInstances(ctx, ngs, StatusRunning, 1, 20, syncInstanceStatus)
	go handleRouteInstances(ctx, ngs, StatusPendingDeletion, 1, 1, execBeforeDeleteHook)
	go handleRouteInstances(ctx, ngs, StatusDeleting, 1, 3, deleteInstance)
	go handleRouteInstances(ctx, ngs, StatusDeleted, 1, 10, removeDeletedInstances)
	go handleRouteInstances(ctx, ngs, StatusFailed, 1, 100, printFailedStatusInstances)
	//go handleRouteInstances(ctx, ngs, StatusUnknown, nil)

	return
}

func routeInstanceByStatus(ngs *NodeGroups, key Status, status ...Status) {
	for _, in := range ngs.FilterInstanceByStatus(status...) {
		if v, ok := ngs.instanceQueue[key]; ok {
			v.Add(in, true)
		}
	}
}

func handleRouteInstances(ctx context.Context, ngs *NodeGroups, key Status, period int, maxSize int, handle func(context.Context, *NodeGroups, []*Instance)) {
	ctx.Value("wg").(*sync.WaitGroup).Add(1)
	defer ctx.Value("wg").(*sync.WaitGroup).Done()

	if handle == nil {
		klog.Errorf("run %s instances status handle failde, handle is nil", key)
		return
	}

	klog.V(1).Infof("run %s instances status handle", key)

	tick := time.NewTicker(time.Second * time.Duration(period))
	for {
		select {
		case <-ctx.Done():
			klog.V(1).Infof("shutdown %s instances status handle", key)
			return
		case <-tick.C:
			instances := dumpElementsFromQueue(ngs.instanceQueue[key], maxSize, time.Second)
			if len(instances) == 0 {
				continue
			}
			klog.V(5).Infof("there are %d %s instances need to handle", len(instances), key)

			handle(ctx, ngs, instances)

			removeElementsFromQueue(ngs.instanceQueue[key], len(instances))
		}
	}
}

// 在t时间内从队列中获取size个元素(不会从队列中删除)
func dumpElementsFromQueue[T *Instance](q *queue.Queue, maxSize int, t time.Duration) []T {
	if q == nil {
		return nil
	}

	t1 := time.Tick(t)
	t2 := time.Tick(t / time.Duration(maxSize) / 10)
exit:
	for {
		select {
		// 超时退出
		case <-t1:
			if q.Length() < maxSize {
				maxSize = q.Length()
			}
			break exit
			// 满足maxSize退出
		case <-t2:
			if q.Length() >= maxSize {
				break exit
			}
		}
	}

	ins := make([]T, 0, maxSize)
	queueLen := q.Length()

	for i := queueLen - 1; i >= queueLen-maxSize; i-- {
		elem := q.Get(i)
		if v, ok := elem.(T); ok {
			ins = append(ins, v)
		}
	}
	return ins
}

// 从队列尾部删除count个元素
func removeElementsFromQueue[T *Instance](q *queue.Queue, count int) (ins []T) {
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
func syncInstanceStatus(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	classifiedIns := pcommon.ClassifiedInstancesByProviderID(instances)

	// 调用云厂商接口获取instance状态
	for k, ins := range classifiedIns {
		if len(ins) == 0 {
			continue
		}

		cp, err := provider.NewCloudprovider(ins[0].ProviderID, GetNodeGroups().CloudProviderOption())
		if err != nil {
			klog.Errorf("NewCloudprovider failed, %s", err)
			continue
		}

		ins, err = cp.InstancesStatus(ctx, ins...)
		if err != nil {
			klog.Errorf("sync instances status failed, %s", err)
		}

		// 更新Instance的状态
		ngs.UpdateInstancesStatus(ins...)
		klog.V(5).Infof("updated %d instances status by cloudprovider %s", len(ins), k)
	}
}

// 调用云厂商接口创建instance
func createInstance(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for _, ins := range instances {
		ng, err := GetNodeGroups().FindNodeGroupByInstanceID(ins.ID)
		if err != nil {
			err = fmt.Errorf("find nodegroup by instance ID failed, %s", err)
			ins.ErrorMsg = err.Error()
			klog.Error(err)
			ngs.UpdateInstances(ins)
			continue
		}

		insParam := GetNodeGroups().InstanceParameter(ng.InstanceParameter)
		if insParam == nil {
			err = fmt.Errorf("nodegroup(%s).InstanceParameter[%s] not exist", ng.Id, ng.InstanceParameter)
			ins.ErrorMsg = err.Error()
			klog.Error(err)
			ngs.UpdateInstances(ins)
			continue
		}

		cp, err := provider.NewCloudprovider(insParam.ProviderIdTemplate, GetNodeGroups().CloudProviderOption())
		if err != nil {
			err = fmt.Errorf("NewCloudprovider failed, %s", err)
			ins.ErrorMsg = err.Error()
			klog.Error(err)
			ngs.UpdateInstances(ins)
			continue
		}

		klog.V(1).Infof("creatting instance(%s)...", ins.ID)

		_, err = cp.CreateInstance(ctx, ins, insParam.Parameter)
		if err != nil {
			klog.Errorf("create instance(%s) failed, %s", ins.ID, err)
		} else {
			providerName, account, region, _, _ := pcommon.ExtractProviderID(insParam.ProviderIdTemplate)

			ins.ProviderID = pcommon.GenerateInstanceProviderID(providerName, account, region, ins.ProviderID)
			ins.Status = StatusCreating
			ins.ErrorMsg = ""

			klog.V(1).Infof("create instance(%s) success, ProviderID:%s", ins.ID, ins.ProviderID)
		}
		// 更新Instance的状态
		ngs.UpdateInstances(ins)
	}
}

// 等待instance状态正常
func waitInstanceCreated(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for _, ins := range instances {
		cp, err := provider.NewCloudprovider(ins.ProviderID, GetNodeGroups().CloudProviderOption())
		if err != nil {
			klog.Errorf("wait instance(%s) created failed, %s", ins.ID, err)
			continue
		}

		lastStatus := ins.Status
		ins, err = cp.InstanceStatus(ctx, ins)
		if err != nil {
			klog.Errorf("wait instance(%s) created failed, %s", ins.ID, err)
		}

		if lastStatus == StatusCreating && ins.Status == StatusRunning {
			ins.Status = StatusCreated
			if ins.Name == "" && ins.IP != "" {
				ins.Name = generateInstanceName(ins.IP)
			}
			klog.V(1).Infof("wait instance(%s) created success", ins.ID)
		}
		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

func generateInstanceName(ip string) string {
	// node-y4znao.vm1-2-3-4
	return fmt.Sprintf("node-%s.vm%s", utils.RandStr(6), strings.ReplaceAll(ip, ".", "-"))
}

// 执行AfterCreated Hook
// - 修改host name
// - 调整内核参数
func execAfterCreatedHook(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	for _, ins := range instances {
		start := time.Now()
		klog.V(1).Infof("exec %s for intance(%s)...", AfterCreatedScript, ins.ID)
		err := execHookScript(ctx, ngs, ins, AfterCreatedScript, false)
		if err != nil {
			ins.Status = StatusFailed
			ins.ErrorMsg = err.Error()
			klog.Errorf("exec intance(%s) execHookScript failed, cost:%v, %s", ins.ID, time.Since(start), err)
		} else {
			// 设置标签和污点
			err = patchNodeNodeGroupTemplate(ctx, ngs, ins)
			if err != nil {
				ins.Status = StatusFailed
				ins.ErrorMsg = err.Error()
				klog.Errorf("patch intance(%s) NodeNodeGroupTemplate failed, cost:%v, %s", ins.ID, time.Since(start), err)
			} else {
				ins.Status = StatusRunning
				ins.ErrorMsg = ""
				klog.V(1).Infof("exec %s for intance(%s) success, cost:%v", AfterCreatedScript, ins.ID, time.Since(start))
			}
		}

		// 更新Instance信息
		ngs.UpdateInstancesStatus(ins)

	}
}

// BeforeDelete Hook
// TODO 调用k8s接口删除node
func execBeforeDeleteHook(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	for _, ins := range instances {
		start := time.Now()
		klog.V(1).Infof("exec %s for intance(%s)...", BeforeDeleteScript, ins.ID)
		err := execHookScript(ctx, ngs, ins, BeforeDeleteScript, false)
		if err != nil {
			ins.Status = StatusFailed
			ins.ErrorMsg = err.Error()
			klog.Errorf("exec intance(%s) execHookScript failed, cost:%v, %s", ins.ID, time.Since(start), err)
		} else {
			ins.Status = StatusDeleting
			ins.ErrorMsg = ""
			klog.V(1).Infof("exec %s for intance(%s) success, cost:%v", BeforeDeleteScript, ins.ID, time.Since(start))
		}
		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

// deleteInstance 调用云厂商接口删除instance
func deleteInstance(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	for _, ins := range instances {
		klog.V(1).Infof("delete intance(%s)...", ins.ID)
		err := callDeleteInstance(ctx, ngs, ins)
		if err != nil {
			ins.Status = StatusFailed
			ins.ErrorMsg = err.Error()
			klog.Errorf("delete intance(%s) failed, %s", ins.ID, err)
		} else {
			// 将instance标记为已删除，等待7天或15天才把instance从记录里删除
			ins.Status = StatusDeleted
			ins.ErrorMsg = ""
			klog.V(1).Infof("delete intance(%s) success", ins.ID)
		}

		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

// removeDeletedInstances 移除之前已删除的instance
func removeDeletedInstances(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	for _, ins := range instances {
		if time.Now().After(ins.UpdateTime.Add(time.Hour * 24 * 7)) {
			ngs.RemoveInstances(ins)
			klog.V(1).Infof("remove intance(%s) success", ins.ID)
		}
	}
}

// printFailedStatusInstances 报错错误状态的instance
func printFailedStatusInstances(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	for _, ins := range instances {
		klog.Errorf("intance(%s) status is: %s, ErrorMsg: %s", ins.ID, ins.Status, ins.ErrorMsg)
	}
}

// 调用云厂商接口删除instance
func callDeleteInstance(ctx context.Context, ngs *NodeGroups, ins *Instance) error {
	cp, err := provider.NewCloudprovider(ins.ProviderID, GetNodeGroups().CloudProviderOption())
	if err != nil {
		return fmt.Errorf("call provider delete instance(%s) failed, %s", ins.ID, err)
	}

	klog.V(1).Infof("call provider delete instance(%s)...", ins.ID)

	ins, err = cp.DeleteInstance(ctx, ins, nil)
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

	executor, err := script_executor.NewScriptExecutor(generateSshInfo(ins))
	if err != nil {
		err = fmt.Errorf("NewScriptExecutor for %s failed, %s", script, err)
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
		return nil
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
