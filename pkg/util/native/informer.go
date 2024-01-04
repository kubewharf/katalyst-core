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

package native

import (
	"reflect"
	"sync"

	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/informers/internalinterfaces"
	v1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

var (
	informerNewFuncMtx sync.RWMutex
	informerNewFuncs   = make(map[reflect.Type]internalinterfaces.NewInformerFunc)
)

func SetInformerNewFunc(t reflect.Type, f internalinterfaces.NewInformerFunc) {
	informerNewFuncMtx.Lock()
	defer informerNewFuncMtx.Unlock()
	informerNewFuncs[t] = f
}

func GetInformerNewFunc(t reflect.Type) (internalinterfaces.NewInformerFunc, bool) {
	informerNewFuncMtx.RLock()
	defer informerNewFuncMtx.RUnlock()
	if f, ok := informerNewFuncs[t]; ok {
		return f, true
	}
	return nil, false
}

type podInformer struct {
	cache.SharedIndexInformer
	v1.PodLister
}

func NewPodInformer(informer cache.SharedIndexInformer) coreinformers.PodInformer {
	return &podInformer{SharedIndexInformer: informer}
}
func (i *podInformer) Informer() cache.SharedIndexInformer { return i.SharedIndexInformer }
func (i *podInformer) Lister() v1.PodLister {
	return v1.NewPodLister(i.SharedIndexInformer.GetIndexer())
}
