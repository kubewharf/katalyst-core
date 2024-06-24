package loadaware

import (
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	toolcache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	v1pod "k8s.io/kubernetes/pkg/api/v1/pod"

	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/eventhandlers"
)

const (
	LoadAwarePodHandler = "LoadAwarePodHandler"
	LoadAwareNPDHandler = "LoadAwareNPDHandler"
)

func RegisterPodHandler() {
	eventhandlers.RegisterEventHandler(
		LoadAwarePodHandler,
		func(informerFactory informers.SharedInformerFactory, _ externalversions.SharedInformerFactory) {
			podInformer := informerFactory.Core().V1().Pods()
			podInformer.Informer().AddEventHandler(
				toolcache.FilteringResourceEventHandler{
					FilterFunc: func(obj interface{}) bool {
						return true
					},
					Handler: toolcache.ResourceEventHandlerFuncs{
						AddFunc:    OnAdd,
						UpdateFunc: OnUpdate,
						DeleteFunc: OnDelete,
					},
				},
			)
		})
}

func (p *Plugin) registerNodeMonitorHandler() {
	eventhandlers.RegisterEventHandler(
		LoadAwareNPDHandler,
		func(_ informers.SharedInformerFactory, internalInformerFactory externalversions.SharedInformerFactory) {
			p.npdLister = internalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister()
		},
	)
}

func OnAdd(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.Warningf("transfer obj to pod fail")
		return
	}
	nodeName := pod.Spec.NodeName
	if nodeName == "" || v1pod.IsPodTerminal(pod) {
		return
	}
	startTime := time.Now()
	if pod.Status.StartTime != nil {
		startTime = pod.Status.StartTime.Time
	}

	cache.addPod(nodeName, pod, startTime)
}

func OnUpdate(oldObj, newObj interface{}) {
	pod, ok := newObj.(*v1.Pod)
	if !ok {
		return
	}
	if v1pod.IsPodTerminal(pod) {
		cache.removePod(pod.Spec.NodeName, pod)
	} else {
		//pod delete and pod may merge a update event
		assignTime := time.Now()
		if pod.Status.StartTime != nil {
			assignTime = pod.Status.StartTime.Time
		}
		cache.addPod(pod.Spec.NodeName, pod, assignTime)
	}
}

func OnDelete(obj interface{}) {
	var pod *v1.Pod
	switch t := obj.(type) {
	case *v1.Pod:
		pod = t
	case toolcache.DeletedFinalStateUnknown:
		var ok bool
		pod, ok = t.Obj.(*v1.Pod)
		if !ok {
			return
		}
	default:
		return
	}
	cache.removePod(pod.Spec.NodeName, pod)
}
