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

package target

import (
	"context"
	"fmt"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	configapis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configinformers "github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions/config/v1alpha1"
	kcclient "github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type KatalystCustomConfigTargetHandler struct {
	mu sync.RWMutex

	ctx       context.Context
	client    *kcclient.GenericClientSet
	kccConfig *controller.KCCConfig

	syncedFunc []cache.InformerSynced

	// map from gvr to referencing KCCs keys. Actually, it's invalid for multiple KCCs to reference
	// the same gvr, and this map is mainly used to detect this anomaly
	gvrKatalystCustomConfigMap map[metav1.GroupVersionResource]sets.String
	// map from kcc key to gvr. Since the gvr in kcc may be unexpectedly changed,
	// we store the mapping in cache to make sure we can still find the original mapping
	katalystCustomConfigGVRMap map[string]metav1.GroupVersionResource
	// map from gvr to kcc target accessor
	targetAccessorMap map[metav1.GroupVersionResource]KatalystCustomConfigTargetAccessor
	// targetHandlerFuncMap stores the handler functions for all controllers
	// that are interested in kcc-target changes
	targetHandlerFuncMap map[string]KatalystCustomConfigTargetHandlerFunc
}

func NewKatalystCustomConfigTargetHandler(ctx context.Context, client *kcclient.GenericClientSet, kccConfig *controller.KCCConfig,
	katalystCustomConfigInformer configinformers.KatalystCustomConfigInformer,
) *KatalystCustomConfigTargetHandler {
	k := &KatalystCustomConfigTargetHandler{
		ctx:       ctx,
		client:    client,
		kccConfig: kccConfig,
		syncedFunc: []cache.InformerSynced{
			katalystCustomConfigInformer.Informer().HasSynced,
		},
		gvrKatalystCustomConfigMap: make(map[metav1.GroupVersionResource]sets.String),
		katalystCustomConfigGVRMap: make(map[string]metav1.GroupVersionResource),
		targetHandlerFuncMap:       make(map[string]KatalystCustomConfigTargetHandlerFunc),
		targetAccessorMap:          make(map[metav1.GroupVersionResource]KatalystCustomConfigTargetAccessor),
	}

	katalystCustomConfigInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    k.addKatalystCustomConfigEventHandle,
		UpdateFunc: k.updateKatalystCustomConfigEventHandle,
		DeleteFunc: k.deleteKatalystCustomConfigEventHandle,
	})
	return k
}

// HasSynced whether all cache has synced
func (k *KatalystCustomConfigTargetHandler) HasSynced() bool {
	for _, hasSynced := range k.syncedFunc {
		if !hasSynced() {
			return false
		}
	}
	return true
}

func (k *KatalystCustomConfigTargetHandler) Run() {
	defer k.shutDown()
	<-k.ctx.Done()
}

// RegisterTargetHandler is used to register handler functions for the given gvr
func (k *KatalystCustomConfigTargetHandler) RegisterTargetHandler(name string, handlerFunc KatalystCustomConfigTargetHandlerFunc) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.targetHandlerFuncMap[name] = handlerFunc
}

// GetKCCKeyListByGVR get kcc keyList by gvr.
func (k *KatalystCustomConfigTargetHandler) GetKCCKeyListByGVR(gvr metav1.GroupVersionResource) []string {
	k.mu.RLock()
	defer k.mu.RUnlock()

	kccKeys, ok := k.gvrKatalystCustomConfigMap[gvr]
	if ok {
		return kccKeys.List()
	}
	return nil
}

func (k *KatalystCustomConfigTargetHandler) GetTargetAccessorByGVR(gvr metav1.GroupVersionResource) (KatalystCustomConfigTargetAccessor, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	accessor, ok := k.targetAccessorMap[gvr]
	if ok {
		return accessor, true
	}
	return nil, false
}

// RangeGVRTargetAccessor is used to walk through all accessors and perform the given function
func (k *KatalystCustomConfigTargetHandler) RangeGVRTargetAccessor(f func(gvr metav1.GroupVersionResource, accessor KatalystCustomConfigTargetAccessor) bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()

	for gvr, a := range k.targetAccessorMap {
		ret := f(gvr, a)
		if !ret {
			return
		}
	}
}

func (k *KatalystCustomConfigTargetHandler) addKatalystCustomConfigEventHandle(obj interface{}) {
	kcc, ok := obj.(*configapis.KatalystCustomConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *KatalystCustomConfig: %v", obj)
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(kcc)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", kcc, err))
		return
	}

	_, err = k.addOrUpdateGVRAndKCC(kcc.Spec.TargetType, key)
	if err != nil {
		klog.Errorf("cannot convert add or update gvr %s kcc %s: %v", kcc.Spec.TargetType, key, err)
		return
	}
}

func (k *KatalystCustomConfigTargetHandler) updateKatalystCustomConfigEventHandle(old interface{}, new interface{}) {
	oldKCC, ok := old.(*configapis.KatalystCustomConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *KatalystCustomConfig: %v", new)
		return
	}

	newKCC, ok := new.(*configapis.KatalystCustomConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *KatalystCustomConfig: %v", new)
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(newKCC)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", newKCC, err))
		return
	}

	_, err = k.addOrUpdateGVRAndKCC(newKCC.Spec.TargetType, key)
	if err != nil {
		klog.Errorf("cannot convert add or update gvr %s kcc %s: %v", newKCC.Spec.TargetType, key, err)
		return
	}

	// if kcc has updated, it needs trigger all related kcc target to reconcile
	if newKCC.GetGeneration() == newKCC.Status.ObservedGeneration &&
		oldKCC.Status.ObservedGeneration != newKCC.Status.ObservedGeneration {
		accessor, ok := k.GetTargetAccessorByGVR(newKCC.Spec.TargetType)
		if ok {
			kccTargets, err := accessor.List(labels.Everything())
			if err != nil {
				klog.Errorf("list gvr %s kcc target failed: %v", newKCC.Spec.TargetType, err)
				return
			}

			for _, target := range kccTargets {
				accessor.Enqueue("", target)
			}
		}
	}
}

func (k *KatalystCustomConfigTargetHandler) deleteKatalystCustomConfigEventHandle(obj interface{}) {
	kcc, ok := obj.(*configapis.KatalystCustomConfig)
	if !ok {
		klog.Errorf("cannot convert obj to *KatalystCustomConfig: %v", obj)
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(kcc)
	if err != nil {
		utilruntime.HandleError(fmt.Errorf("couldn't get key for object %#v: %v", kcc, err))
		return
	}

	k.deleteGVRAndKCCKey(kcc.Spec.TargetType, key)

	// when some kcc of a gvr has been deleted, we need reconcile its kcc target immediately
	accessor, ok := k.GetTargetAccessorByGVR(kcc.Spec.TargetType)
	if ok {
		kccTargets, err := accessor.List(labels.Everything())
		if err != nil {
			klog.Errorf("list gvr %s kcc target failed: %v", kcc.Spec.TargetType, err)
			return
		}

		for _, target := range kccTargets {
			accessor.Enqueue("", target)
		}
	}
}

// addOrUpdateGVRAndKCC add gvr and kcc key to cache and return current kcc keys which use this gvr.
func (k *KatalystCustomConfigTargetHandler) addOrUpdateGVRAndKCC(gvr metav1.GroupVersionResource, key string) (KatalystCustomConfigTargetAccessor, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	old, ok := k.katalystCustomConfigGVRMap[key]
	if ok && old != gvr {
		k.deleteGVRAndKCCKeyWithoutLock(old, key)
	}

	return k.addGVRAndKCCKeyWithoutLock(gvr, key)
}

// deleteGVRAndKCCKey delete gvr and kcc key, return whether it is empty after delete that
func (k *KatalystCustomConfigTargetHandler) deleteGVRAndKCCKey(gvr metav1.GroupVersionResource, key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.deleteGVRAndKCCKeyWithoutLock(gvr, key)
}

func (k *KatalystCustomConfigTargetHandler) deleteGVRAndKCCKeyWithoutLock(gvr metav1.GroupVersionResource, key string) {
	kccKeys, ok := k.gvrKatalystCustomConfigMap[gvr]
	if ok {
		delete(kccKeys, key)
		delete(k.katalystCustomConfigGVRMap, key)
	}

	if len(kccKeys) == 0 {
		if accessor, ok := k.targetAccessorMap[gvr]; ok {
			accessor.Stop()
		}
		delete(k.targetAccessorMap, gvr)
		delete(k.gvrKatalystCustomConfigMap, gvr)
	}
}

func (k *KatalystCustomConfigTargetHandler) addGVRAndKCCKeyWithoutLock(gvr metav1.GroupVersionResource, key string) (KatalystCustomConfigTargetAccessor, error) {
	if err := k.checkGVRValid(gvr); err != nil {
		return nil, err
	}

	_, ok := k.gvrKatalystCustomConfigMap[gvr]
	if !ok {
		_, ok := k.targetAccessorMap[gvr]
		if !ok {
			accessor, err := NewRealKatalystCustomConfigTargetAccessor(gvr,
				k.client.DynamicClient, k.targetHandlerFuncMap)
			if err != nil {
				return nil, err
			}
			accessor.Start()
			k.targetAccessorMap[gvr] = accessor
		} else {
			klog.Fatalf("targetAccessor exists for unseen gvr %s", gvr.String())
		}
		k.gvrKatalystCustomConfigMap[gvr] = sets.NewString()
	}
	k.gvrKatalystCustomConfigMap[gvr].Insert(key)
	k.katalystCustomConfigGVRMap[key] = gvr
	return k.targetAccessorMap[gvr], nil
}

// checkGVRValid is used to check whether the given gvr is valid, skip to create corresponding
// target accessor otherwise
func (k *KatalystCustomConfigTargetHandler) checkGVRValid(gvr metav1.GroupVersionResource) error {
	if !k.kccConfig.ValidAPIGroupSet.Has(gvr.Group) {
		return fmt.Errorf("gvr %s is not in valid api group set", gvr.String())
	}

	schemaGVR := native.ToSchemaGVR(gvr.Group, gvr.Version, gvr.Resource)
	resourceList, err := k.client.DiscoveryClient.ServerResourcesForGroupVersion(schemaGVR.GroupVersion().String())
	if err != nil {
		return err
	}

	for _, resource := range resourceList.APIResources {
		if resource.Name == gvr.Resource {
			return nil
		}
	}

	return apierrors.NewNotFound(schemaGVR.GroupResource(), schemaGVR.Resource)
}

func (k *KatalystCustomConfigTargetHandler) shutDown() {
	k.mu.Lock()
	defer k.mu.Unlock()

	for _, accessor := range k.targetAccessorMap {
		accessor.Stop()
	}
}
