package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/workqueue"
)

const (
	NAMESPACE = "client-go-tutorials"
)

// 自定义controller数据结构，嵌入了真实的控制器
type Controller struct {
	// 本地缓存，关注的对象都会同步到这里
	indexer cache.Indexer
	// 消息队列，用来触发对真实对象的处理事件
	queue workqueue.RateLimitingInterface
	// 实际运行运行的控制器
	informer cache.Controller
}

// NewController 简单封装了数据结构的实例化
func NewController(queue workqueue.RateLimitingInterface, indexer cache.Indexer, informer cache.Controller) *Controller {
	return &Controller{
		informer: informer,
		indexer:  indexer,
		queue:    queue,
	}
}

// processNextItem 不间断从队列中取得数据并处理
func (c *Controller) processNextItem() bool {
	// 注意，队列里面不是对象，而是key，这是个阻塞队列，会一直等待
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	// Tell the queue that we are done with processing this key. This unblocks the key for other workers
	// This allows safe parallel processing because two pods with the same key are never processed in
	// parallel.
	defer c.queue.Done(key)

	// 注意，这里的syncToStdout应该是业务代码，处理对象变化的事件
	err := c.syncToStdout(key.(string))

	// 如果前面的业务逻辑遇到了错误，就在此处理
	c.handleErr(err, key)

	// 外面的调用逻辑是：返回true就继续调用processNextItem方法
	return true
}

// syncToStdout 这是业务逻辑代码，被调用意味着key对应的对象有变化(新增或者修改)
func (c *Controller) syncToStdout(key string) error {
	// 从本地缓存中取出完整的对象
	obj, exists, err := c.indexer.GetByKey(key)
	if err != nil {
		klog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	// 如果不存在，就表示这是个删除事件
	if !exists {
		fmt.Printf("Pod %s does not exist anymore\n", key)
	} else {
		objType, err := meta.TypeAccessor(obj)
		if err != nil {
			klog.Errorf("get type accessor error, [%s], failed with %v", key, err)
			return err
		}

		klog.Infof("kind [%s], apiversion [%s]", objType.GetKind(), objType.GetAPIVersion())

		// 这里无视了obj具体是什么类型的对象(deployment、pod这些都有可能)，
		// 用meta.Accessor转换出metav1.Object对象后就能获取该对象的所有meta信息
		objMeta, err := meta.Accessor(obj)

		if err != nil {
			klog.Errorf("get meta accessor error, [%s], failed with %v", key, err)
			return err
		}

		// 打印对象的meta信息，验证meta.Accessor返回的对象是否符合预期
		klog.Infof("name [%s], namespace [%s], lable app [%s]",
			objMeta.GetName(),
			objMeta.GetNamespace(),
			objMeta.GetLabels()["app"])

		fmt.Printf("Sync/Add/Update for Pod %s\n", obj.(*v1.Pod).GetName())
	}
	return nil
}

// handleErr 如果前面的业务逻辑执行出现错误，就在此集中处理错误，本例中主要是重试次数的控制
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.queue.Forget(key)
		return
	}

	// 如果重试次数未超过5次，就继续重试
	if c.queue.NumRequeues(key) < 5 {
		klog.Infof("Error syncing pod %v: %v", key, err)

		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// queue and the re-enqueue history, the key will be processed later again.
		c.queue.AddRateLimited(key)
		return
	}

	// 代码走到这里，意味着有错误并且重试超过了5次，应该立即丢弃
	c.queue.Forget(key)
	// 这种连续五次重试还未成功的错误，交给全局处理逻辑
	runtime.HandleError(err)
	klog.Infof("Dropping pod %q out of the queue: %v", key, err)
}

// Run 开始常规的控制器模式（持续响应资源变化事件）
func (c *Controller) Run(threadiness int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	// Let the workers stop when we are done
	defer c.queue.ShutDown()
	klog.Info("Starting Pod controller")

	go c.informer.Run(stopCh)

	// Wait for all involved caches to be synced, before processing items from the queue is started
	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		runtime.HandleError(fmt.Errorf("timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	klog.Info("Stopping Pod controller")
}

// runWorker 这是个无限循环，不断地从队列取出数据处理
func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}

func StartController(c Getter, resource string, namespace string) {

}

func main() {
	var kubeconfig *string
	var master string

	// 试图取到当前账号的家目录
	if home := homedir.HomeDir(); home != "" {
		// 如果能取到，就把家目录下的.kube/config作为默认配置文件
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
		master = ""
	} else {
		// 如果取不到，就没有默认配置文件，必须通过kubeconfig参数来指定
		flag.StringVar(kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
		flag.StringVar(&master, "master", "", "master url")
		flag.Parse()
	}

	config, err := clientcmd.BuildConfigFromFlags(master, *kubeconfig)
	if err != nil {
		klog.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatal(err)
	}

	podListWatcher := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "pods", NAMESPACE, fields.Everything())

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	indexer, informer := cache.NewIndexerInformer(podListWatcher, &v1.Pod{}, 0, cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(old interface{}, new interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(new)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			if err == nil {
				queue.Add(key)
			}
		},
	}, cache.Indexers{})

	controller := NewController(queue, indexer, informer)

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	// Wait forever
	select {}
}