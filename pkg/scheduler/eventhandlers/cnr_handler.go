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

package eventhandlers

import (
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	schedulercache "github.com/kubewharf/katalyst-core/pkg/scheduler/cache"
)

const (
	CommonCNRHandler = "CommonCNRHandler"
)

func RegisterCommonCNRHandler() {
	RegisterEventHandler(CommonCNRHandler, AddCNREventHandler)
}

// AddCNREventHandler adds CNR event handlers for the scheduler.
func AddCNREventHandler(_ informers.SharedInformerFactory, internalInformerFactory externalversions.SharedInformerFactory) {
	cnrInformer := internalInformerFactory.Node().V1alpha1().CustomNodeResources()
	cnrInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    addCNRToCache,
			UpdateFunc: updateNodeInCache,
			DeleteFunc: deleteCNRFromCache,
		})
}

func addCNRToCache(obj interface{}) {
	cnr, ok := obj.(*apis.CustomNodeResource)
	if !ok {
		klog.Errorf("cannot convert obj to *apis.CNR: %v", obj)
		return
	}
	klog.V(3).InfoS("Add event for CNR", "CNR", klog.KObj(cnr))

	schedulercache.GetCache().AddOrUpdateCNR(cnr)
}

func updateNodeInCache(_, newObj interface{}) {
	newCNR, ok := newObj.(*apis.CustomNodeResource)
	if !ok {
		klog.ErrorS(nil, "Cannot convert newObj to *apis.CNR", "newObj", newObj)
		return
	}
	klog.V(3).InfoS("Update event for CNR", "CNR", klog.KObj(newCNR))

	schedulercache.GetCache().AddOrUpdateCNR(newCNR)
}

func deleteCNRFromCache(obj interface{}) {
	var cnr *apis.CustomNodeResource
	switch t := obj.(type) {
	case *apis.CustomNodeResource:
		cnr = t
	case cache.DeletedFinalStateUnknown:
		var ok bool
		cnr, ok = t.Obj.(*apis.CustomNodeResource)
		if !ok {
			klog.ErrorS(nil, "Cannot convert to *apis.CNR", "obj", t.Obj)
			return
		}
	default:
		klog.ErrorS(nil, "Cannot convert to *apis.CNR", "obj", t)
		return
	}
	klog.V(3).InfoS("Delete event for CNR", "CNR", klog.KObj(cnr))

	schedulercache.GetCache().RemoveCNR(cnr)
}
