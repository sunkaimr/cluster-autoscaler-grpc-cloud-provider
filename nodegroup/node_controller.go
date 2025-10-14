package nodegroup

import (
	"context"
	"fmt"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"math"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/sunkaimr/cluster-autoscaler-grpc-cloud-provider/nodegroup/instance"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	listers "k8s.io/client-go/listers/core/v1"
	_ "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

type Controller struct {
	kubeClientSet kubernetes.Interface

	nodeLister listers.NodeLister
	nodeSynced cache.InformerSynced

	workQueue workqueue.RateLimitingInterface
}

var kubeClient *kubernetes.Clientset

func NewKubeClient() *kubernetes.Clientset {
	if kubeClient != nil {
		return kubeClient
	}

	kubeconfig := GetNodeGroups().ops.kubeConfig

	var err error
	var cfg *rest.Config
	if kubeconfig != "" {
		// 如果在k8s集群外部使用需要通过k8s的config文件来创建client
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			klog.Exitf("Error building kubeconfig: %s", err.Error())
		}
	} else {
		// 在集群群内部使用，如运行在k8s的pod中通过以下方式创建client
		cfg, err = rest.InClusterConfig()
		if err != nil {
			klog.Exitf("Error building kubeconfig: %s", err.Error())
		}
	}

	kubeClient, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		klog.Exitf("Error building kubernetes clientset: %s", err.Error())
	}
	return kubeClient
}

func runNodeController(ctx context.Context) {
	if kubeClient == nil {
		kubeClient = NewKubeClient()
	}
	informerFactory := informers.NewSharedInformerFactory(kubeClient, 0)

	nodeCtl := newController(kubeClient, informerFactory.Core().V1().Nodes())

	informerFactory.Start(ctx.Done())

	if err := nodeCtl.run(3, ctx.Done()); err != nil {
		klog.Exitf("Error running controller: %s", err.Error())
	}
}

func newController(
	kcs kubernetes.Interface,
	nodeInformer corev1informers.NodeInformer) *Controller {

	controller := &Controller{
		kubeClientSet: kcs,
		nodeLister:    nodeInformer.Lister(),
		nodeSynced:    nodeInformer.Informer().HasSynced,
		workQueue:     workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "nodes"),
	}

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: controller.enqueue,
		UpdateFunc: func(old, new interface{}) {
			newNode := new.(*corev1.Node)
			oldNode := old.(*corev1.Node)
			if reflect.DeepEqual(newNode.Annotations, oldNode.Annotations) &&
				reflect.DeepEqual(newNode.Labels, oldNode.Labels) &&
				reflect.DeepEqual(newNode.Spec.Taints, oldNode.Spec.Taints) {
				return
			}
			controller.enqueue(newNode)
		},
		DeleteFunc: controller.enqueue,
	})

	return controller
}

func (c *Controller) enqueue(obj interface{}) {
	var key string
	var err error
	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		klog.Error(err)
		return
	}
	c.workQueue.Add(key)
}

func (c *Controller) run(workers int, stopCh <-chan struct{}) error {
	defer utilruntime.HandleCrash()
	//defer c.workQueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.V(0).Info("Starting Node controller")

	// Wait for the caches to be synced before starting workers
	klog.V(0).Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.nodeSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	klog.V(0).Info("informer caches synced")

	klog.V(0).Info("Starting workers")
	for i := 0; i < workers; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}
	klog.V(0).Infof("started %d node controller workers", workers)

	for {
		time.Sleep(time.Second)
		if c.workQueue.Len() == 0 {
			klog.V(0).Info("workQueue handle finished")
			break
		}
	}

	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workQueue.Get()

	if shutdown {
		klog.Info("work queue shutting down")
		return false
	}

	// We wrap this block in a func so we can defer c.workQueue.Done.
	err := func(obj interface{}) error {
		defer c.workQueue.Done(obj)
		var key string
		var ok bool

		if key, ok = obj.(string); !ok {
			c.workQueue.Forget(obj)
			klog.V(1).Infof("expected string in workQueue but got %#v", obj)
			return nil
		}

		if err := c.syncHandler(key); err != nil {
			// Put the item back on the workQueue to handle any transient errors.
			c.workQueue.AddRateLimited(key)
			return fmt.Errorf("error syncing '%s': %s, requeuing", key, err.Error())
		}

		c.workQueue.Forget(obj)
		klog.V(5).Infof("Successfully synced '%s'", key)
		return nil
	}(obj)

	if err != nil {
		klog.Error(err)
		return true
	}

	return true
}

func (c *Controller) syncHandler(key string) error {
	_, nodeName, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		klog.V(1).Infof("invalid resource key: %s", key)
		return nil
	}

	// 判断pod是否还存在
	node, err := c.nodeLister.Get(nodeName)
	if err != nil {
		if errors.IsNotFound(err) {
			// node被删除了
			klog.V(1).Infof("%s was deleted", key)
			GetNodeGroups().DeleteNode(nodeName)
			GetNodeGroups().RefreshTargetSize()
			return nil
		}

		return err
	}

	// Node创建或被更新
	klog.V(5).Infof("%s was updated", node.Name)

	err = GetNodeGroups().UpdateNodeInNodeGroup(node)
	if err != nil {
		klog.Errorf("UpdateNodeInNodeGroup node(%s) failed, %s", node.Name, err)
		return nil
	}
	return nil
}

func patchNodeNodeGroupTemplate(ctx context.Context, ngs *NodeGroups, ins *instance.Instance) error {
	node, err := NewKubeClient().CoreV1().Nodes().Get(ctx, ins.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node(%s) from apiserver failed, %s", ins.Name, err)
	}

	ng, err := ngs.FindNodeGroupByInstanceID(ins.ID)
	if err != nil {
		return fmt.Errorf("find nodegroup by instance.ID(%s) failed, %s", ins.ID, err)
	}

	for k, v := range ng.NodeTemplate.Annotations {
		node.Annotations[k] = v
	}

	for k, v := range ng.NodeTemplate.Labels {
		node.Labels[k] = v
	}

	for _, t := range ng.NodeTemplate.Taints {
		node.Spec.Taints = append(node.Spec.Taints, t)
	}

	_, err = NewKubeClient().CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("patch node(%s) NodeGroupTemplate failed, %s", ins.Name, err)
	}
	return nil
}

func DeleteNodeFromKubernetes(ctx context.Context, nodeName string) error {
	err := NewKubeClient().CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("delete node(%s) form kubernetes failed, %s", nodeName, err)
	}
	return nil
}

func WatchConfigMap(ctx context.Context, namespace, configmap string, handle func(cm *corev1.ConfigMap)) {
	retryDelay := time.Second * 5
	maxRetryDelay := time.Minute * 5

	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := watchConfigMapWithRetry(ctx, namespace, configmap, handle)
			if err != nil {
				klog.Errorf("watch configmap %s/%s failed: %v, retrying in %v", namespace, configmap, err, retryDelay)

				time.Sleep(retryDelay)
				retryDelay = time.Duration(math.Min(float64(retryDelay*2), float64(maxRetryDelay)))
				continue
			}

			retryDelay = time.Second * 5
		}
	}
}

func watchConfigMapWithRetry(ctx context.Context, namespace, configmap string, handle func(cm *corev1.ConfigMap)) error {
	opts := metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", configmap).String(),
	}

	w, err := NewKubeClient().CoreV1().ConfigMaps(namespace).Watch(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to create watch for %s/%s, %v", namespace, configmap, err)
	}
	defer w.Stop()

	klog.Infof("started watching configmap %s/%s", namespace, configmap)

	for {
		select {
		case event, ok := <-w.ResultChan():
			if !ok {
				return fmt.Errorf("watch configmap %s/%s channel closed", namespace, configmap)
			}

			switch event.Type {
			case watch.Modified:
				if cm, ok := event.Object.(*corev1.ConfigMap); ok {
					klog.V(3).Infof("ConfigMap %s/%s modified", cm.Namespace, cm.Name)
					// 处理 configmap 修改
					handle(cm)
				}
			case watch.Added:
			case watch.Deleted:
			case watch.Error:
				if status, ok := event.Object.(*metav1.Status); ok {
					return fmt.Errorf("watch error for %s/%s, %s", namespace, configmap, status.Message)
				}
				return fmt.Errorf("unknown watch error for %s/%s, %v", namespace, configmap, event.Object)
			}
		case <-ctx.Done():
			klog.Infof("stopping watch due to context cancellation")
			return nil
		}
	}
}
