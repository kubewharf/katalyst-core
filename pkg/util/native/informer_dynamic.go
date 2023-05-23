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
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/dynamicmapper"
)

// DynamicInformer keeps the informer-related contents for each workload
type DynamicInformer struct {
	GVK      schema.GroupVersionKind
	GVR      schema.GroupVersionResource
	Informer informers.GenericInformer
}

type DynamicResourcesManager struct {
	// dynamicGVRs and dynamicInformers initialize only once
	dynamicGVRs      map[string]schema.GroupVersionResource
	dynamicInformers map[string]DynamicInformer

	mapper                 *dynamicmapper.RegeneratingDiscoveryRESTMapper
	dynamicInformerFactory dynamicinformer.DynamicSharedInformerFactory
}

// NewDynamicResourcesManager initializes a dynamic resources manger to manage dynamic informers
func NewDynamicResourcesManager(
	dynamicResources []string,
	mapper *dynamicmapper.RegeneratingDiscoveryRESTMapper,
	dynamicInformerFactory dynamicinformer.DynamicSharedInformerFactory,
) (*DynamicResourcesManager, error) {
	m := &DynamicResourcesManager{
		dynamicInformers:       make(map[string]DynamicInformer),
		mapper:                 mapper,
		dynamicInformerFactory: dynamicInformerFactory,
	}

	dynamicGVRs, err := getDynamicResourcesGVRMap(dynamicResources)
	if err != nil {
		return nil, fmt.Errorf("new dynamic resource manager failed: %v", err)
	}
	m.dynamicGVRs = dynamicGVRs

	m.initDynamicInformers()
	return m, nil
}

// Run start mapper to refresh  starts a goroutine to check if it has new gvr support available,
// and if so, panics to restart to make sure all caches are correct
func (m *DynamicResourcesManager) Run(ctx context.Context) {
	// run RegeneratingDiscoveryRESTMapper to refresh gvr to gvk map
	m.mapper.RunUntil(ctx.Done())

	// try to check whether new resources need to support, and if so,
	// panics to restart
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		for resource, gvr := range m.dynamicGVRs {
			gvk, err := m.mapper.KindFor(gvr)
			if err != nil {
				klog.Errorf("find for %v failed, err %v", gvr.String(), err)
				continue
			}

			// check whether current gvk is equal to old one
			if informer, ok := m.dynamicInformers[resource]; ok && gvk == informer.GVK {
				continue
			}

			panic(fmt.Sprintf("gvk %s for gvr %s found, try to restart", gvk.String(), gvr.String()))
		}
	}, 1*time.Minute)
}

// GetDynamicInformers gets current dynamic informers
func (m *DynamicResourcesManager) GetDynamicInformers() map[string]DynamicInformer {
	return m.dynamicInformers
}

// initDynamicInformers initializes dynamic informers map
func (m *DynamicResourcesManager) initDynamicInformers() {
	_ = wait.ExponentialBackoff(retry.DefaultRetry, func() (bool, error) {
		err := m.syncDynamicInformers()
		if err == nil {
			return true, nil
		}

		klog.Errorf("sync DynamicInformers failed: %v, try to refresh mapper", err)

		err = m.mapper.RegenerateMappings()
		if err != nil {
			klog.Errorf("regenerate DiscoveryRESTMapper failed: %v", err)
		}
		return false, nil
	})
}

// syncDynamicInformers sync dynamic informers by current dynamic resources
func (m *DynamicResourcesManager) syncDynamicInformers() error {
	var errList []error
	for resource, gvr := range m.dynamicGVRs {
		if _, ok := m.dynamicInformers[resource]; ok {
			continue
		}

		gvk, err := m.mapper.KindFor(gvr)
		if err != nil {
			errList = append(errList, fmt.Errorf("find for %v failed, err %v", gvr.String(), err))
			continue
		}

		m.dynamicInformers[resource] = DynamicInformer{
			GVK:      gvk,
			GVR:      gvr,
			Informer: m.dynamicInformerFactory.ForResource(gvr),
		}
	}
	if len(errList) > 0 {
		return errors.NewAggregate(errList)
	}

	return nil
}

func getDynamicResourcesGVRMap(dynamicResources []string) (map[string]schema.GroupVersionResource, error) {
	dynamicGVRs := make(map[string]schema.GroupVersionResource, len(dynamicResources))
	var errList []error
	for _, resource := range dynamicResources {
		if _, ok := dynamicGVRs[resource]; ok {
			continue
		}

		gvr, _ := schema.ParseResourceArg(resource)
		if gvr == nil {
			return nil, fmt.Errorf("ParseResourceArg resource %v failed", resource)
		}

		dynamicGVRs[resource] = *gvr
	}
	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return dynamicGVRs, nil
}
