package nodegroup

import (
	"context"
	"fmt"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	listers "k8s.io/client-go/listers/core/v1"
	_ "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"time"
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

	// 如果在k8s集群外部使用需要通过k8s的config文件来创建client
	cfg, err := clientcmd.BuildConfigFromFlags("", "kube_config")
	if err != nil {
		klog.Exitf("Error building kubeconfig: %s", err.Error())
	}

	// 在集群群内部使用，如运行在k8s的pod中通过以下方式创建client
	//cfg, err := rest.InClusterConfig()
	//if err != nil {
	//	klog.Exitf("Error building kubeconfig: %s", err.Error())
	//}

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
			if newNode.ResourceVersion == oldNode.ResourceVersion {
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
	defer c.workQueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	klog.V(0).Info("Starting Node controller")

	// Wait for the caches to be synced before starting workers
	klog.V(0).Info("Waiting for informer caches to sync")
	if ok := cache.WaitForCacheSync(stopCh, c.nodeSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	klog.V(0).Info("cache synced")

	klog.V(0).Info("Starting workers")
	// Launch two workers to process Foo resources
	for i := 0; i < workers; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}
	klog.V(0).Infof("started %d node controller workers", workers)
	return nil
}

func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

func (c *Controller) processNextWorkItem() bool {
	obj, shutdown := c.workQueue.Get()

	if shutdown {
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
