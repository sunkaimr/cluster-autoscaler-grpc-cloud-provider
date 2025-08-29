package nodegroup

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"github.com/spf13/viper"
	. "github.com/sunkaimr/cluster-autoscaler-grpc-provider/nodegroup/instance"
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
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultNodeGroup        = "default"
	ConfigMapLastUpdatedKey = "cluster-autoscaler.grpc-provider/last-updated"
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
			StatusPending:  queue.New(),
			StatusCreating: queue.New(),
			StatusRunning:  queue.New(),
			StatusDeleting: queue.New(),
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
	cache               nodeGroupCache
	updateTime          time.Time
	instanceQueue       map[Status]*queue.Queue
	ops                 *nodeGroupsOps
	cloudProviderOption provider.CloudProviderOption
}

type nodeGroupsOps struct {
	configFile      string
	nameSpace       string
	statusConfigMap string // "nodegroup-status"
}

type NodeGroup struct {
	// 节点池ID
	Id string `json:"id" yaml:"id"`

	// 节点池内最小节点数量
	// 如果节点池的TargetSize小于MinSize应该发起扩容
	MinSize int `json:"minSize" yaml:"minSize"`

	// 节点池内最大节点数量
	MaxSize int `json:"maxSize" yaml:"maxSize"`

	// 节点池目标节点数量
	TargetSize int `json:"targetSize" yaml:"targetSize"`

	// 节点列表
	Instances InstanceList `json:"instances" yaml:"instances"`

	NodeTemplate NodeTemplate `json:"nodeTemplate" yaml:"nodeTemplate"`
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
	return fmt.Sprintf("%s MinSize:%d MaxSize:%d", ng.Id, ng.MinSize, ng.MaxSize)
}

func GetNodeGroups() *NodeGroups {
	return nodeGroups
}

// Run
// watch Node 定时更新NodeGroup中Instance信息
// 定时将当前NodeGroup status持久化到config map中
// 启动Instance Controller调用云厂商接口实现对Instance控制
func (ngs *NodeGroups) Run(ctx context.Context, ops ...func()) error {
	for _, op := range ops {
		op()
	}

	ngs.loadNodeGroups()
	runNodeController(ctx)

	go wait.Until(
		func() {
			if err := ngs.WriteStatusConfigMap(); err != nil {
				klog.Error(err)
			}
		},
		time.Second*5,
		ctx.Done(),
	)

	go ngs.SyncAllInstances(ctx)

	return nil
}

func (ngs *NodeGroups) WithOpsConfigFile(f string) func() {
	return func() {
		ngs.ops.configFile = f
	}
}
func (ngs *NodeGroups) WithOpsNamespace(ns string) func() {
	return func() {
		ngs.ops.nameSpace = ns
	}
}
func (ngs *NodeGroups) WithOpsStatusConfigMap(cm string) func() {
	return func() {
		ngs.ops.statusConfigMap = cm
	}
}

func (ngs *NodeGroups) Length() int {
	return len(ngs.cache)
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
	}

	ng.TargetSize += increaseSize

	for i := 0; i < int(increaseSize); i++ {
		ng.Instances = append(ng.Instances, &Instance{
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
			ngs.cache.find(ng.Id).Instances.Add(generateInstance(node))
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
			ngs.cache.find(matchedNg.Id).Instances.Add(generateInstance(node))
		}
		ngs.Unlock()
	}

	// instance的IP,ProviderID发生变化
	ngs.Lock()
	if ins := ngs.cache.find(matchedNg.Id).Instances.Find(node.Name); ins != nil {
		newIns := generateInstance(node)
		if ins.ProviderID != newIns.ProviderID || ins.IP != newIns.IP {
			ins.ProviderID = newIns.ProviderID
			ins.IP = newIns.IP
		}
	}
	ngs.Unlock()

	return nil
}

// DeleteNode 当watch到k8s中节点不存在后标记nodegroup中对应的Instance为Deleting状态
func (ngs *NodeGroups) DeleteNode(nodeName string) {
	ngs.Lock()

	for _, ng := range ngs.cache {
		for i, v := range ng.Instances {
			if nodeName == v.Name {
				ng.Instances[i].Status = StatusDeleting
			}
		}
	}
	ngs.Unlock()
}

// DeleteNodesInNodeGroup 收到CA的删除NodeGroup中node请求，将对应的Instance标记为Deleting状态
func (ngs *NodeGroups) DeleteNodesInNodeGroup(id string, nodeNames ...string) error {
	ngs.Lock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return NotFoundErr

	}

	// 判断删除后的size是否小于MinSize
	maxDeletedSize := len(nodeNames)
	if ng.TargetSize-len(nodeNames) < ng.MinSize {
		maxDeletedSize = ng.TargetSize - ng.MinSize
	}

	deleted := 0
	for i, ins := range ng.Instances {
		if deleted >= maxDeletedSize {
			break
		}

		for _, name := range nodeNames {
			if ins.Name == name && ins.Status != StatusPendingDeletion {
				ng.Instances[i].Status = StatusPendingDeletion
				klog.V(0).Infof("mark nodegroup(%s) node(%s) is PendingDeletion", ng.Id, ins.Name)
				deleted++
				break
			}
		}
	}

	ng.TargetSize -= maxDeletedSize

	ngs.Unlock()
	return nil
}

// AddInstancesInNodeGroup 收到CA的调添加Node请求，向nodegroup中添加N个instance
func (ngs *NodeGroups) AddInstancesInNodeGroup(id string, size int) error {
	ngs.Lock()
	defer ngs.Unlock()

	ng := ngs.cache.find(id)
	if ng == nil {
		return NotFoundErr
	}

	for i := 0; i < size; i++ {
		ng.Instances.Add(&Instance{
			Status: StatusPending,
		})
	}

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

	actuality := ng.Instances.DecreasePending(size)
	ng.TargetSize -= actuality
	return actuality, nil
}

func generateInstance(node *corev1.Node) *Instance {
	var ins Instance

	ins.Name = node.Name
	ins.ProviderID = node.Spec.ProviderID
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
	result["beta.kubernetes.io/arch"] = cloudprovider.DefaultArch
	result["beta.kubernetes.io/os"] = cloudprovider.DefaultOS
	result["kubernetes.io/os"] = cloudprovider.DefaultOS
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
	var ngc NodeGroupsConfig
	for _, ng := range ngs.cache {
		ngc.NodeGroups = append(ngc.NodeGroups, *ng)
	}
	ngc.CloudProviderOption = ngs.cloudProviderOption
	ngs.Unlock()

	out, err := yaml.Marshal(ngc)
	if err != nil {
		return "", err
	}

	return string(out), nil
}

func (ngs *NodeGroups) WriteStatusConfigMap() error {
	cmNamespace, cmName := ngs.ops.nameSpace, ngs.ops.statusConfigMap
	data, err := ngs.toYaml()
	if err != nil {
		err = fmt.Errorf("marshal NodeGroup to yaml failed, %s", err)
		klog.Error(err)
		return err
	}

	maps := kubeClient.CoreV1().ConfigMaps(cmNamespace)
	configMap, err := maps.Get(context.TODO(), cmName, metav1.GetOptions{})
	if err != nil && !kubeerrors.IsNotFound(err) {
		err = fmt.Errorf("get configmap(%s/%s) from apiserver failed, %s", cmNamespace, cmName, err)
		klog.Error(err)
		return err
	}

	// config map不存在则创建
	if err != nil && kubeerrors.IsNotFound(err) {
		configMap = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cmNamespace,
				Name:      cmName,
				Annotations: map[string]string{
					ConfigMapLastUpdatedKey: time.Now().Format(time.RFC3339),
				},
			},
			Data: map[string]string{"NodeGroupsConfig": data},
		}
		_, err = maps.Create(context.TODO(), configMap, metav1.CreateOptions{})
		if err != nil {
			err = fmt.Errorf("create configmap(%s/%s) failed, %s", cmNamespace, cmName, err)
			klog.Error(err)
			return err
		}
		klog.V(1).Infof("create configmap(%s/%s) success", cmNamespace, cmName)
		return nil
	}

	// 根据md5判断内容是否一致, 若一致则无需更新
	if md5.Sum([]byte(data)) == md5.Sum([]byte(configMap.Data["NodeGroupsConfig"])) {
		klog.V(5).Infof("configmap(%s/%s) md5 not changed", cmNamespace, cmName)
		return nil
	} else {
		klog.V(3).Infof("configmap(%s/%s) md5 changed", cmNamespace, cmName)
	}

	if configMap.ObjectMeta.Annotations == nil {
		configMap.ObjectMeta.Annotations = make(map[string]string)
	}
	configMap.Data["NodeGroupsConfig"] = data
	configMap.ObjectMeta.Annotations[ConfigMapLastUpdatedKey] = time.Now().Format(time.RFC3339)
	_, err = maps.Update(context.TODO(), configMap, metav1.UpdateOptions{})
	if err != nil {
		err = fmt.Errorf("update configmap(%s/%s) failed, %s", cmNamespace, cmName, err)
		klog.Error(err)
		return err
	}
	klog.V(3).Infof("update configmap(%s/%s) success", cmNamespace, cmName)
	return nil
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
			if v.ProviderID == ins.ProviderID {
				v.Status = ins.Status
				v.UpdateTime = time.Now()
				return nil
			}
		}
	}
	return fmt.Errorf("not found instance(Name:%s, providerID:%s)", ins.Name, ins.ProviderID)
}

func (ngs *NodeGroups) UpdateInstancesStatus(newIns ...*Instance) {
	ngs.Lock()
	defer ngs.Unlock()

	for _, ng := range ngs.cache {
		for _, cacheIns := range ng.Instances {
			for _, ins := range newIns {
				if cacheIns.ProviderID == ins.ProviderID {
					cacheIns.Status = ins.Status
					cacheIns.UpdateTime = time.Now()
				}
			}

		}
	}
}

func (ngs *NodeGroups) SyncAllInstances(ctx context.Context) {
	// 1, 定时同步instance状态
	// Running状态的5min同步一次
	// Creating, Deleting状态的10s同步一次
	go wait.Until(
		func() {
			klog.V(5).Infof("syncing running status instances")
			for _, in := range ngs.FilterInstanceByStatus("", StatusRunning) {
				ngs.instanceQueue[StatusRunning].Add(in, true)
			}
		},
		time.Minute*5,
		//time.Minute*1,
		ctx.Done(),
	)

	// 2, 创建instance流程
	go wait.Until(
		func() {
			klog.V(5).Infof("syncing pending status instances")
			for _, in := range ngs.FilterInstanceByStatus(StatusPending) {
				ngs.instanceQueue[StatusPending].Add(in, true)
			}
		},
		time.Second*10,
		ctx.Done(),
	)

	// 3, 注册K8S流程
	go wait.Until(
		func() {
			klog.V(5).Infof("syncing creating status instances")
			for _, in := range ngs.FilterInstanceByStatus(StatusCreating) {
				ngs.instanceQueue[StatusCreating].Add(in, true)
			}
		},
		time.Second*10,
		ctx.Done(),
	)

	// 4, 删除instance流程
	go wait.Until(
		func() {
			klog.V(5).Infof("syncing deleting status instances")
			for _, in := range ngs.FilterInstanceByStatus(StatusDeleting) {
				ngs.instanceQueue[StatusDeleting].Add(in, true)
			}
		},
		time.Second*10,
		ctx.Done(),
	)

	go ngs.RunSyncRunningInstancesStatus(ctx)

	return
}

func (ngs *NodeGroups) RunSyncRunningInstancesStatus(ctx context.Context) {
	klog.V(1).Info("run sync running instances status")

	tick := time.NewTicker(time.Second * 1)
	for {
		select {
		case <-ctx.Done():
			klog.V(1).Info("shutdown RunSyncRunningInstancesStatus")
			return
		case <-tick.C:
			allIns := outQueue(ngs.instanceQueue[StatusRunning], 50, time.Second)
			if len(allIns) == 0 {
				continue
			}
			klog.V(5).Infof("there are %d instances need to sync status", len(allIns))

			classifiedIns := pcommon.ClassifiedInstancesByProviderID(allIns)

			// 调用云厂商接口获取instance状态
			for k, ins := range classifiedIns {
				if len(ins) == 0 {
					continue
				}

				cp, err := provider.NewCloudprovider(ins[0].ProviderID, GetNodeGroups().cloudProviderOption)
				if err != nil {
					klog.Errorf("NewCloudprovider failed, %s", err)
					continue
				}

				newIns, err := cp.InstancesStatus(ins...)
				if err != nil {
					klog.Errorf("InstancesStatus failed, %s", err)
				}

				// 更新Instance的状态
				ngs.UpdateInstancesStatus(newIns...)
				klog.V(5).Infof("updated %d instances status by cloudprovider %s", len(newIns), k)
			}
		}
	}
}

// 在t时间内从队列中获取size个元素
func outQueue(q *queue.Queue, maxSize int, t time.Duration) (ins []*Instance) {
	exitTime := time.Now().Add(t)
	for {
		func() {
			defer func() {
				recover()
			}()

			elem := q.Remove()
			if v, ok := elem.(*Instance); ok {
				ins = append(ins, v)
			}

		}()
		if len(ins) >= maxSize {
			return ins
		}

		if time.Now().After(exitTime) {
			return ins
		}
		time.Sleep(t / time.Duration(maxSize) / 10)
	}
}
