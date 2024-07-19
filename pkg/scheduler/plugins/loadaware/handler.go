/*
Copyright 2022 The Katalyst Authors.

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
	LoadAwareSPDHandler = "LoadAwareSPDHandler"
)

func (p *Plugin) registerPodHandler() {
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
						AddFunc:    p.OnAdd,
						UpdateFunc: p.OnUpdate,
						DeleteFunc: p.OnDelete,
					},
				},
			)
		})
}

func (p *Plugin) registerNPDHandler() {
	eventhandlers.RegisterEventHandler(
		LoadAwareNPDHandler,
		func(_ informers.SharedInformerFactory, internalInformerFactory externalversions.SharedInformerFactory) {
			p.npdLister = internalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister()
		},
	)
}

func (p *Plugin) registerSPDHandler() {
	eventhandlers.RegisterEventHandler(
		LoadAwareSPDHandler,
		func(_ informers.SharedInformerFactory, internalInformerFactory externalversions.SharedInformerFactory) {
			p.spdLister = internalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister()
			p.spdHasSynced = internalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer().HasSynced
		},
	)
}

func (p *Plugin) OnAdd(obj interface{}) {
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

	p.cache.addPod(nodeName, pod, startTime)
}

func (p *Plugin) OnUpdate(oldObj, newObj interface{}) {
	pod, ok := newObj.(*v1.Pod)
	if !ok {
		return
	}
	if v1pod.IsPodTerminal(pod) {
		p.cache.removePod(pod.Spec.NodeName, pod)
	} else {
		// pod delete and pod may merge a update event
		assignTime := time.Now()
		if pod.Status.StartTime != nil {
			assignTime = pod.Status.StartTime.Time
		}
		p.cache.addPod(pod.Spec.NodeName, pod, assignTime)
	}
}

func (p *Plugin) OnDelete(obj interface{}) {
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
	p.cache.removePod(pod.Spec.NodeName, pod)
}
