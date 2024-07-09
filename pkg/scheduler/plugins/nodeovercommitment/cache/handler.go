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

package cache

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	clientgocache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	v1alpha12 "github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	schedulercache "github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/eventhandlers"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	OvercommitPodHandler = "OvercommitPodHandler"
	OvercommitCNRHandler = "OvercommitCNRHandler"

	OvercommitNOCHandler = "OvercommitNOCHandler"

	NOCIndexerKey = "overcommitIndexerKey"
)

// RegisterPodHandler register handler to scheduler event handlers
func RegisterPodHandler() {
	eventhandlers.RegisterEventHandler(OvercommitPodHandler, func(informerFactory informers.SharedInformerFactory, _ externalversions.SharedInformerFactory) {
		podInformer := informerFactory.Core().V1().Pods()
		podInformer.Informer().AddEventHandler(
			clientgocache.FilteringResourceEventHandler{
				FilterFunc: func(obj interface{}) bool {
					switch t := obj.(type) {
					case *v1.Pod:
						return native.IsAssignedPod(t)
					case clientgocache.DeletedFinalStateUnknown:
						if _, ok := t.Obj.(*v1.Pod); ok {
							// The carried object may be stale, so we don't use it to check if
							// it's assigned or not. Attempting to cleanup anyways.
							return true
						}
						utilruntime.HandleError(fmt.Errorf("unable to convert object %T to *v1.Pod", obj))
						return false
					default:
						utilruntime.HandleError(fmt.Errorf("unable to handle object: %T", obj))
						return false
					}
				},
				Handler: clientgocache.ResourceEventHandlerFuncs{
					AddFunc:    addPod,
					UpdateFunc: updatePod,
					DeleteFunc: deletePod,
				},
			},
		)
	})
}

// RegisterCNRHandler register handler to scheduler event handlers
func RegisterCNRHandler() {
	eventhandlers.RegisterEventHandler(OvercommitCNRHandler, func(_ informers.SharedInformerFactory, internalInformerFactory externalversions.SharedInformerFactory) {
		cnrInformer := internalInformerFactory.Node().V1alpha1().CustomNodeResources()
		cnrInformer.Informer().AddEventHandler(
			clientgocache.ResourceEventHandlerFuncs{
				AddFunc:    addCNR,
				UpdateFunc: updateCNR,
				DeleteFunc: deleteCNR,
			})
	})
}

func RegisterNOCHandler() {
	eventhandlers.RegisterEventHandler(OvercommitNOCHandler, func(_ informers.SharedInformerFactory, internalInformerFactory externalversions.SharedInformerFactory) {
		nocInformer := internalInformerFactory.Overcommit().V1alpha1().NodeOvercommitConfigs()

		err := nocInformer.Informer().AddIndexers(clientgocache.Indexers{
			NOCIndexerKey: func(obj interface{}) ([]string, error) {
				noc, ok := obj.(*v1alpha12.NodeOvercommitConfig)
				if !ok {
					klog.Warningf("transfer obj to noc fail")
					return []string{}, nil
				}

				if noc.Spec.NodeOvercommitSelectorVal == "" {
					return []string{}, nil
				}

				return []string{noc.Spec.NodeOvercommitSelectorVal}, nil
			},
		})
		if err != nil {
			klog.Fatalf("RegisterNOCHandler fail: %v", err)
		}

		cache.nocIndexer = nocInformer.Informer().GetIndexer()
	})
}

func addPod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}
	klog.V(6).InfoS("Add event for scheduled pod", "pod", klog.KObj(pod))

	if err := GetCache().AddPod(pod); err != nil {
		klog.Errorf("%v cache AddPod failed, pod: %v, err: %v", OvercommitPodHandler, klog.KObj(pod), err)
	}
}

func updatePod(_, newObj interface{}) {
	newPod, ok := newObj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", newObj)
		return
	}
	klog.V(6).InfoS("Add event for scheduled pod", "pod", klog.KObj(newPod))

	if err := GetCache().AddPod(newPod); err != nil {
		klog.Errorf("%v cache AddPod failed, pod: %v, err: %v", OvercommitPodHandler, klog.KObj(newPod), err)
	}
}

func deletePod(obj interface{}) {
	var pod *v1.Pod
	switch t := obj.(type) {
	case *v1.Pod:
		pod = t
	case clientgocache.DeletedFinalStateUnknown:
		var ok bool
		pod, ok = t.Obj.(*v1.Pod)
		if !ok {
			klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", t.Obj)
			return
		}
	default:
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", t)
		return
	}
	klog.V(6).InfoS("Delete event for scheduled pod", "pod", klog.KObj(pod))

	if err := GetCache().RemovePod(pod); err != nil {
		klog.ErrorS(err, "Scheduler cache RemovePod failed", "pod", klog.KObj(pod))
	}
}

func addCNR(obj interface{}) {
	cnr, ok := obj.(*v1alpha1.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to CNR: %v", obj)
		return
	}

	GetCache().AddOrUpdateCNR(cnr)
}

func updateCNR(_, newObj interface{}) {
	newCNR, ok := newObj.(*v1alpha1.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to CNR: %v", newObj)
		return
	}

	GetCache().AddOrUpdateCNR(newCNR)
}

func deleteCNR(obj interface{}) {
	var cnr *v1alpha1.CustomNodeResource
	switch t := obj.(type) {
	case *v1alpha1.CustomNodeResource:
		cnr = t
	case clientgocache.DeletedFinalStateUnknown:
		var ok bool
		cnr, ok = t.Obj.(*v1alpha1.CustomNodeResource)
		if !ok {
			klog.ErrorS(nil, "Cannot convert to *apis.CNR", "obj", t.Obj)
			return
		}
	default:
		klog.ErrorS(nil, "Cannot convert to *apis.CNR", "obj", t)
		return
	}

	schedulercache.GetCache().RemoveCNR(cnr)
}
