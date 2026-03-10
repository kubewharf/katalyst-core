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

package resourcepackage

import (
	"context"
	"fmt"
	"sync"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	hintoptimizerutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	qosutil "github.com/kubewharf/katalyst-core/pkg/util/qos"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const HintOptimizerNameResourcePackage = "resource_package"

const (
	syncResourcePackageUpdatePeriod = 30 * time.Second
)

// resourcePackageHintOptimizer implements the HintOptimizer interface based on resource package information.
type resourcePackageHintOptimizer struct {
	conf       *config.Configuration
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
	state      state.State

	mux                sync.RWMutex
	resourcePackageMap map[int][]resourcepackage.ResourcePackageItem
}

// NewResourcePackageHintOptimizer creates a new resourcePackageHintOptimizer.
func NewResourcePackageHintOptimizer(
	options policy.HintOptimizerFactoryOptions,
) (hintoptimizer.HintOptimizer, error) {
	o := &resourcePackageHintOptimizer{
		conf:       options.Conf,
		metaServer: options.MetaServer,
		emitter:    options.Emitter,
		state:      options.State,
	}
	o.updateResourcePackageMap()
	return o, nil
}

// OptimizeHints optimizes the topology hints based on resource package information.
func (o *resourcePackageHintOptimizer) OptimizeHints(
	request hintoptimizer.Request,
	hints *pluginapi.ListOfTopologyHints,
) error {
	err := hintoptimizerutil.GenericOptimizeHintsCheck(request, hints)
	if err != nil {
		general.Errorf("GenericOptimizeHintsCheck failed with error: %v", err)
		return err
	}

	if qosutil.AnnotationsIndicateNUMAExclusive(request.Annotations) {
		general.Infof("skip resourcePackageHintOptimizer for exclusive numa pod: %s/%s, container: %s",
			request.PodNamespace, request.PodName, request.ContainerName)
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	resourcePackage := resourcepackage.GetResourcePackageName(request.ResourceRequest.Annotations)
	if resourcePackage == "" {
		general.Errorf("skip resourcePackageHintOptimizer for pod resource package not found in annotation")
		return hintoptimizerutil.ErrHintOptimizerSkip
	}

	resourcePackageAllocatableMap, err := o.getResourcePackageAllocatable(resourcePackage)
	if err != nil {
		general.Errorf("getResourcePackageAllocatable failed with error: %v", err)
		return err
	}

	resourcePackageAllocatedMap, err := o.getResourcePackageAllocated(resourcePackage)
	if err != nil {
		general.Errorf("getResourcePackageAllocated failed with error: %v", err)
		return err
	}

	general.Infof("optimize hints for pod %s/%s, container %s, resource package %s, cpu request %.3f, resourcePackageAllocatableMap %+v, resourcePackageAllocatedMap %+v, hints %+v",
		request.PodNamespace, request.PodName, request.ContainerName, resourcePackage, request.CPURequest, resourcePackageAllocatableMap, resourcePackageAllocatedMap, hints)

	// Optimize hints based on resource package information
	err = o.populateHintsByResourcePackage(hints, request.CPURequest, resourcePackageAllocatableMap, resourcePackageAllocatedMap)
	if err != nil {
		general.Errorf("populateHintsByResourcePackage failed with error: %v", err)
		return err
	}

	if len(hints.Hints) == 0 {
		return fmt.Errorf("no hints found for resource package %s", resourcePackage)
	}

	general.Infof("optimized hints for pod %s/%s, container %s, resource package %s, cpu request %.3f, resourcePackageAllocatableMap %+v, resourcePackageAllocatedMap %+v, hints %+v",
		request.PodNamespace, request.PodName, request.ContainerName, resourcePackage, request.CPURequest, resourcePackageAllocatableMap, resourcePackageAllocatedMap, hints)

	return nil
}

// Run starts the resource package hint optimizer.
func (o *resourcePackageHintOptimizer) Run(stopCh <-chan struct{}) error {
	go wait.Until(o.updateResourcePackageMap, syncResourcePackageUpdatePeriod, stopCh)
	return nil
}

// populateHintsByResourcePackage optimizes hints based on resource package information.
func (o *resourcePackageHintOptimizer) populateHintsByResourcePackage(
	hints *pluginapi.ListOfTopologyHints,
	cpuRequest float64,
	resourcePackageAllocatableMap map[int]float64,
	resourcePackageAllocatedMap map[int]float64,
) error {
	canAllocateNodes := sets.NewInt()
	for nodeID, allocatable := range resourcePackageAllocatableMap {
		if allocatable-resourcePackageAllocatedMap[nodeID] >= cpuRequest {
			canAllocateNodes.Insert(nodeID)
		}
	}

	optimizedHints := make([]*pluginapi.TopologyHint, 0, len(hints.Hints))
	for _, hint := range hints.Hints {
		if len(hint.Nodes) != 1 {
			continue
		}

		if canAllocateNodes.Has(int(hint.Nodes[0])) {
			optimizedHints = append(optimizedHints, hint)
		}
	}

	hints.Hints = optimizedHints
	return nil
}

func (o *resourcePackageHintOptimizer) getResourcePackageAllocatable(resourcePackage string) (map[int]float64, error) {
	resourcePackageMap := o.getResourcePackageMap()
	if resourcePackageMap == nil {
		return nil, fmt.Errorf("resourcePackageMap is nil")
	}

	allocatable := make(map[int]float64)
	for nodeID, packages := range resourcePackageMap {
		for _, pkg := range packages {
			if pkg.PackageName == resourcePackage {
				if pkg.Allocatable == nil {
					continue
				}

				// Use the native package to get CPU quantity safely
				cpuQuantity := native.CPUQuantityGetter()(*pkg.Allocatable)
				if cpuQuantity.IsZero() {
					continue
				}

				allocatable[nodeID] = float64(cpuQuantity.MilliValue()) / 1000
			}
		}
	}
	return allocatable, nil
}

func (o *resourcePackageHintOptimizer) getResourcePackageAllocated(resourcePackage string) (map[int]float64, error) {
	machineState := o.state.GetMachineState()
	allocated := make(map[int]float64)
	for nodeID, nodeState := range machineState {
		if nodeState == nil {
			continue
		}

		for _, entries := range nodeState.PodEntries {
			for _, entry := range entries {
				if entry == nil || resourcepackage.GetResourcePackageName(entry.Annotations) != resourcePackage {
					continue
				}

				allocated[nodeID] += entry.RequestQuantity
			}
		}
	}
	return allocated, nil
}

func (o *resourcePackageHintOptimizer) getResourcePackageMap() map[int][]resourcepackage.ResourcePackageItem {
	o.mux.RLock()
	defer o.mux.RUnlock()
	return o.resourcePackageMap
}

func (o *resourcePackageHintOptimizer) updateResourcePackageMap() {
	// Get resource package information from meta server
	resourcePackageMap, err := o.metaServer.NodeResourcePackages(context.Background())
	if err != nil {
		general.Errorf("NodeResourcePackages failed with error: %v", err)
		return
	}

	o.mux.Lock()
	defer o.mux.Unlock()
	if apiequality.Semantic.DeepEqual(resourcePackageMap, o.resourcePackageMap) {
		return
	}
	general.Infof("update resource package map from %+v to %+v", o.resourcePackageMap, resourcePackageMap)
	o.resourcePackageMap = resourcePackageMap
}
