package nodegroup

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
			StatusCreated:  queue.New(),
			//StatusRegistering:     queue.New(),
			//StatusRegistered:      queue.New(),
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

	// 创建instance使用到的参数
	InstanceParameter string `json:"instanceParameter" yaml:"instanceParameter"`

	// 匹配节点租的模板
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

	go ngs.SyncInstances(ctx)

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

func (ngs *NodeGroups) CloudProviderOption() provider.CloudProviderOption {
	return ngs.cloudProviderOption
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
	if ins := ngs.cache.find(matchedNg.Id).Instances.Find(node.Name); ins != nil {
		newIns := generateInstanceByNode(node)
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

func generateInstanceByNode(node *corev1.Node) *Instance {
	var ins Instance

	ins.ID = utils.RandStr(8)
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
			if v.ID == ins.ID {
				v.Status = ins.Status
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
					cacheIns.Status = i.Status
					cacheIns.ErrorMsg = i.ErrorMsg
					cacheIns.UpdateTime = time.Now()
				}
			}
		}
	}
}

func (ngs *NodeGroups) SyncInstances(ctx context.Context) {
	// 创建instance流程(限制创建的并发个数)
	// - 调用云厂商SDK创建instance
	// - 将Instance状态设置为Creating
	// - 根据实例ID生成instance的Id
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusPending, StatusPending) }, time.Second*3, ctx.Done())

	// 等待instance的创建完成
	// - 调用云厂商的接口查询instance的状态
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusCreating, StatusCreating) }, time.Second*3, ctx.Done())

	// instance创建完成，执行AfterCreated Hook
	// - 执行AfterCreated Hook, 包含修改内核参数，修改hostname等。Hook需要支持幂等
	// - 执行kubeadm join流程
	// - 包含添加label，污点，注册CMDB等
	// - 当hook执行完成需要将instance状态更改为Running
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusCreated, StatusCreated) }, time.Second*1, ctx.Done())

	//// instance join k8s集群
	//// - 执行kubeadm join流程
	//go wait.Until(func() { routeInstanceByStatus(ngs, StatusRegistering, StatusRegistering) }, time.Second*1, ctx.Done())
	//
	//// 加入k8s成功
	//// - AfterJoin Hook, 包含添加label，污点，注册CMDB等。需要支持幂等
	//go wait.Until(func() { routeInstanceByStatus(ngs, StatusRegistering, StatusRegistering) }, time.Second*1, ctx.Done())

	// 更新Instance的状态
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusRunning, StatusRunning) }, time.Minute*5, ctx.Done())

	// 待删除的instance
	// - 执行BeforeDelete Hook, 需要支持幂等
	// - 当hook执行完成调用云厂商接口删除instance, 状态更改为Deleting
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusPendingDeletion, StatusPendingDeletion) }, time.Second*1, ctx.Done())

	// 等待instance删除完成
	// - 调用云厂商的接口等待instance删除完成，将instance修改为Deleted
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusDeleting, StatusDeleting) }, time.Second*3, ctx.Done())

	// instance删除完成，执行AfterDeleted  Hook
	// - 执行AfterDeleted  Hook, 包含修改解注册CMDB等。Hook需要支持幂等
	// - 删除nodegroup中的instance
	go wait.Until(func() { routeInstanceByStatus(ngs, StatusDeleted, StatusDeleted) }, time.Second*1, ctx.Done())

	// 下边为具体的处理逻辑
	go handleRouteInstances(ctx, ngs, StatusPending, createInstance)
	go handleRouteInstances(ctx, ngs, StatusCreating, waitInstanceCreated)
	go handleRouteInstances(ctx, ngs, StatusCreated, execAfterCreatedHook)
	//go handleRouteInstances(ctx, ngs, StatusRegistering, execKubeadmJoin)
	//go handleRouteInstances(ctx, ngs, StatusRegistered, execAfterJoinHook)
	go handleRouteInstances(ctx, ngs, StatusRunning, syncInstanceStatus)
	go handleRouteInstances(ctx, ngs, StatusPendingDeletion, execBeforeDeleteHook)
	go handleRouteInstances(ctx, ngs, StatusDeleting, waitInstanceDeleted)
	go handleRouteInstances(ctx, ngs, StatusDeleted, execAfterDeleteHook)
	go handleRouteInstances(ctx, ngs, StatusFailed, nil)
	go handleRouteInstances(ctx, ngs, StatusUnknown, nil)

	return
}

func routeInstanceByStatus(ngs *NodeGroups, key Status, status ...Status) {
	for _, in := range ngs.FilterInstanceByStatus(status...) {
		if v, ok := ngs.instanceQueue[key]; ok {
			v.Add(in, true)
		}
	}
}

func handleRouteInstances(ctx context.Context, ngs *NodeGroups, key Status, handle func(context.Context, *NodeGroups, []*Instance)) {
	if handle == nil {
		klog.Errorf("run %s instances status handle failde， handle is nil", key)
		return
	}

	klog.V(1).Infof("run %s instances status handle", key)

	tick := time.NewTicker(time.Second * 1)
	for {
		select {
		case <-ctx.Done():
			klog.V(1).Info("shutdown %s instances status handle", key)
			return
		case <-tick.C:
			instances := dumpElementsFromQueue(ngs.instanceQueue[key], 50, time.Second)
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
			klog.Errorf("find nodegroup by instance ID failed, %s", err)
			continue
		}

		insParam, ok := GetNodeGroups().CloudProviderOption().InstanceParameter[ng.InstanceParameter]
		if !ok {
			klog.Errorf("nodegroup(%s).InstanceParameter[%s] not exist", ng.Id, ng.InstanceParameter)
			continue
		}

		cp, err := provider.NewCloudprovider(insParam.ProviderIdTemplate, GetNodeGroups().CloudProviderOption())
		if err != nil {
			klog.Errorf("NewCloudprovider failed, %s", err)
			continue
		}

		klog.V(1).Infof("creatting instance(%s)...", ins.ID)

		ins, err = cp.CreateInstance(ctx, ins, insParam.Parameter)
		if err != nil {
			klog.Errorf("create instance(%s) failed, %s", ins.ID, err)
		} else {
			provider, account, region, _, _ := pcommon.ExtractProviderID(insParam.ProviderIdTemplate)

			ins.ProviderID = pcommon.GenerateInstanceProviderID(provider, account, region, ins.ProviderID)
			ins.Status = StatusCreating

			klog.V(1).Infof("create instance(%s) success, ProviderID:%s", ins.ID, ins.ProviderID)
		}

		// 更新Instance的状态
		ngs.UpdateInstances(ins)
	}
}

// 等待instance状态正常
func waitInstanceCreated(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for i, ins := range instances {
		cp, err := provider.NewCloudprovider(ins.ProviderID, GetNodeGroups().CloudProviderOption())
		if err != nil {
			klog.Errorf("wait instance created failed, %s", err)
			continue
		}

		lastStatus := ins.Status
		ins, err = cp.InstanceStatus(ctx, ins)
		if err != nil {
			klog.Errorf("wait instance(%s) created failed, %s", instances[i].ID, err)
		}

		if lastStatus == StatusCreating && ins.Status == StatusRunning {
			ins.Status = StatusCreated
			if ins.Name == "" && ins.IP != "" {
				ins.Name = generateInstanceName(ins.IP)
			}
			klog.V(1).Infof("wait instance(%s) created success", instances[i].ID)
		}
		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

func generateInstanceName(ip string) string {
	// node-y4znao.vm10-2-13-34
	return fmt.Sprintf("node-%s.vm%s", utils.RandStr(6), strings.ReplaceAll(ip, ".", "-"))
}

// 执行AfterCreated Hook
// - 修改host name
// - 调整内核参数
func execAfterCreatedHook(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	for _, ins := range instances {

		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

// 执行kubeadm join加入kubernetes集群
func execKubeadmJoin(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for _, ins := range instances {

		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

// AfterJoin Hook：
// - 添加Label和污点
// - 注册CMDB
func execAfterJoinHook(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for _, ins := range instances {

		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

// BeforeDelete Hook
// - 调用云厂商接口删除instance
func execBeforeDeleteHook(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for _, ins := range instances {

		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}

// 等待instance状态正常
func waitInstanceDeleted(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	classifiedIns := pcommon.ClassifiedInstancesByProviderID(instances)
	// 调用云厂商接口获取instance状态
	for _, inss := range classifiedIns {
		if len(inss) == 0 {
			continue
		}

		cp, err := provider.NewCloudprovider(inss[0].ProviderID, GetNodeGroups().CloudProviderOption())
		if err != nil {
			klog.Errorf("NewCloudprovider failed, %s", err)
			continue
		}

		for _, ins := range inss {
			ins, err = cp.InstanceStatus(ctx, ins)
			if err != nil {
				klog.Errorf("create instances failed, %s", err)
			}

			// 更新Instance信息
			ngs.UpdateInstances(ins)
		}
	}
}

// AfterDeleted Hook：
// - 解注册CMDB
func execAfterDeleteHook(ctx context.Context, ngs *NodeGroups, instances []*Instance) {
	// 调用云厂商接口获取instance状态
	for _, ins := range instances {

		// 更新Instance信息
		ngs.UpdateInstances(ins)
	}
}
