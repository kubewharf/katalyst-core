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

package orm

import (
	"reflect"
	"sync"

	//nolint
	"github.com/golang/protobuf/proto"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/orm/checkpoint"
)

type ResourceAllocation map[string]*pluginapi.ResourceAllocationInfo // Keyed by resourceName.
type ContainerResources map[string]ResourceAllocation                // Keyed by containerName.
type PodResources map[string]ContainerResources                      // Keyed by podUID

type podResourcesChk struct {
	sync.RWMutex
	resources PodResources // Keyed by podUID.
}

var EmptyValue = reflect.Value{}

func newPodResourcesChk() *podResourcesChk {
	return &podResourcesChk{
		resources: make(PodResources),
	}
}

func (pr PodResources) DeepCopy() PodResources {
	copiedPodResources := make(PodResources)

	for podUID, containerResources := range pr {
		copiedPodResources[podUID] = containerResources.DeepCopy()
	}

	return copiedPodResources
}

func (cr ContainerResources) DeepCopy() ContainerResources {
	copiedContainerResources := make(ContainerResources)

	for containerName, resouceAllocation := range cr {
		copiedContainerResources[containerName] = resouceAllocation.DeepCopy()
	}

	return copiedContainerResources
}

func (ra ResourceAllocation) DeepCopy() ResourceAllocation {
	copiedResourceAllocation := make(ResourceAllocation)

	for resourceName, allocationInfo := range ra {
		copiedResourceAllocation[resourceName] = proto.Clone(allocationInfo).(*pluginapi.ResourceAllocationInfo)
	}

	return copiedResourceAllocation
}

func (pres *podResourcesChk) pods() sets.String {
	pres.RLock()
	defer pres.RUnlock()

	ret := sets.NewString()
	for k := range pres.resources {
		ret.Insert(k)
	}
	return ret
}

// "resourceName" here is different than "resourceName" in qrm allocation, one qrm plugin may
// only represent one resource in allocation, but can also return several other resourceNames
// to store in pod resources
func (pres *podResourcesChk) insert(podUID, contName, resourceName string, allocationInfo *pluginapi.ResourceAllocationInfo) {
	if allocationInfo == nil {
		return
	}

	pres.Lock()
	defer pres.Unlock()

	if _, podExists := pres.resources[podUID]; !podExists {
		pres.resources[podUID] = make(ContainerResources)
	}
	if _, contExists := pres.resources[podUID][contName]; !contExists {
		pres.resources[podUID][contName] = make(ResourceAllocation)
	}

	pres.resources[podUID][contName][resourceName] = proto.Clone(allocationInfo).(*pluginapi.ResourceAllocationInfo)
}

func (pres *podResourcesChk) deleteResourceAllocationInfo(podUID, contName, resourceName string) {
	pres.Lock()
	defer pres.Unlock()

	if pres.resources[podUID] != nil && pres.resources[podUID][contName] != nil {
		delete(pres.resources[podUID][contName], resourceName)
	}
}

func (pres *podResourcesChk) deletePod(podUID string) {
	pres.Lock()
	defer pres.Unlock()

	if pres.resources == nil {
		return
	}

	delete(pres.resources, podUID)
}

func (pres *podResourcesChk) delete(pods []string) {
	pres.Lock()
	defer pres.Unlock()

	if pres.resources == nil {
		return
	}

	for _, uid := range pods {
		delete(pres.resources, uid)
	}
}

func (pres *podResourcesChk) podResources(podUID string) ContainerResources {
	pres.RLock()
	defer pres.RUnlock()

	if _, podExists := pres.resources[podUID]; !podExists {
		return nil
	}

	return pres.resources[podUID]
}

// Returns all resources information allocated to the given container.
// Returns nil if we don't have cached state for the given <podUID, contName>.
func (pres *podResourcesChk) containerAllResources(podUID, contName string) ResourceAllocation {
	pres.RLock()
	defer pres.RUnlock()

	if _, podExists := pres.resources[podUID]; !podExists {
		return nil
	}
	if _, contExists := pres.resources[podUID][contName]; !contExists {
		return nil
	}

	return pres.resources[podUID][contName].DeepCopy()
}

// Turns podResourcesChk to checkpointData.
func (pres *podResourcesChk) toCheckpointData() []checkpoint.PodResourcesEntry {
	pres.RLock()
	defer pres.RUnlock()

	var data []checkpoint.PodResourcesEntry
	for podUID, containerResources := range pres.resources {
		for conName, resourcesAllocation := range containerResources {
			for resourceName, allocationInfo := range resourcesAllocation {
				allocRespBytes, err := allocationInfo.Marshal()
				if err != nil {
					klog.Errorf("Can't marshal allocationInfo for %v %v %v: %v", podUID, conName, resourceName, err)
					continue
				}
				data = append(data, checkpoint.PodResourcesEntry{
					PodUID:         podUID,
					ContainerName:  conName,
					ResourceName:   resourceName,
					AllocationInfo: string(allocRespBytes)})
			}
		}
	}
	return data
}

// Populates podResourcesChk from the passed in checkpointData.
func (pres *podResourcesChk) fromCheckpointData(data []checkpoint.PodResourcesEntry) {
	for _, entry := range data {
		klog.V(2).Infof("Get checkpoint entry: %s %s %s %s\n",
			entry.PodUID, entry.ContainerName, entry.ResourceName, entry.AllocationInfo)
		allocationInfo := &pluginapi.ResourceAllocationInfo{}
		err := allocationInfo.Unmarshal([]byte(entry.AllocationInfo))
		if err != nil {
			klog.Errorf("Can't unmarshal allocationInfo for %s %s %s %s: %v",
				entry.PodUID, entry.ContainerName, entry.ResourceName, entry.AllocationInfo, err)
			continue
		}
		pres.insert(entry.PodUID, entry.ContainerName, entry.ResourceName, allocationInfo)
	}
}

func (pres *podResourcesChk) allAllocatedResourceNames() sets.String {
	pres.RLock()
	defer pres.RUnlock()

	res := sets.NewString()

	for _, containerResources := range pres.resources {
		for _, resourcesAllocation := range containerResources {
			for resourceName := range resourcesAllocation {
				res.Insert(resourceName)
			}
		}
	}

	return res
}
