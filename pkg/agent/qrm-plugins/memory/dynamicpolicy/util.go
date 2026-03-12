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

package dynamicpolicy

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilkubeconfig "github.com/kubewharf/katalyst-core/pkg/util/kubelet/config"
)

func GetFullyDropCacheBytes(container *v1.Container) int64 {
	if container == nil {
		return 0
	}

	memoryLimit := container.Resources.Limits[v1.ResourceMemory]
	memoryReq := container.Resources.Requests[v1.ResourceMemory]
	fullyDropCacheBytes := memoryLimit.Value()

	if fullyDropCacheBytes == 0 {
		fullyDropCacheBytes = memoryReq.Value()
	}

	return fullyDropCacheBytes
}

// getResourcesReservedMemory is used to spread total reserved memories for all memory resources into per-numa level.
// this reserve resource calculation logic should be kept in qrm, if advisor wants
// to get this info, it should depend on the returned checkpoint (through cpu-server)
func getResourcesReservedMemory(conf *config.Configuration, metaServer *metaserver.MetaServer, machineInfo *info.MachineInfo,
	resourceNames []string,
) (map[v1.ResourceName]map[int]uint64, error) {
	if conf == nil {
		return nil, fmt.Errorf("nil conf")
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	} else if machineInfo == nil {
		return nil, fmt.Errorf("nil machineInfo")
	}

	resourceNames = append(resourceNames, string(v1.ResourceMemory))

	resourcesReservedMemory := make(map[v1.ResourceName]map[int]uint64)
	for _, resourceName := range resourceNames {
		// ignore duplicated resource name
		if _, ok := resourcesReservedMemory[v1.ResourceName(resourceName)]; ok {
			continue
		}

		var reservedMemory map[int]uint64
		var err error
		switch {
		case v1.ResourceName(resourceName) == v1.ResourceMemory:
			reservedMemory, err = getReservedMemory(conf, metaServer, machineInfo)
			if err != nil {
				return nil, err
			}
		case strings.HasPrefix(resourceName, v1.ResourceHugePagesPrefix):
			reservedMemory, err = getReservedHugePagesMemory(conf, metaServer, machineInfo, v1.ResourceName(resourceName))
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unknown memory resource name: %s", resourceName)
		}

		resourcesReservedMemory[v1.ResourceName(resourceName)] = reservedMemory
	}

	return resourcesReservedMemory, nil
}

// getReservedMemory gets reserved v1.ResourceMemory per numa level.
func getReservedMemory(conf *config.Configuration, metaServer *metaserver.MetaServer, machineInfo *info.MachineInfo) (map[int]uint64, error) {
	numasCount := len(machineInfo.Topology)

	var reservedMemoryGB float64
	if conf.UseKubeletReservedConfig {
		klConfig, err := metaServer.GetKubeletConfig(context.TODO())
		if err != nil {
			return nil, fmt.Errorf("failed to get kubelet config: %v", err)
		}

		reservedQuantity, found, err := utilkubeconfig.GetReservedQuantity(klConfig, string(v1.ResourceMemory))
		if err != nil {
			return nil, fmt.Errorf("GetKubeletReservedQuantity failed with error: %v", err)
		} else {
			unitGB := resource.MustParse("1Gi")
			reservedMemoryGB = float64(reservedQuantity.Value()) / float64(unitGB.Value())
			general.Infof("get reservedMemoryGB: %.2f from kubelet config, found: %v", reservedMemoryGB, found)
		}
	} else {
		reservedMemoryGB = float64(conf.ReservedMemoryGB)
		general.Infof("get reservedMemoryGB: %.2f from ReservedMemoryGB configuration", reservedMemoryGB)
	}

	perNumaReservedGB := uint64(math.Ceil(reservedMemoryGB / float64(numasCount)))
	perNumaReservedQuantity := resource.MustParse(fmt.Sprintf("%dGi", perNumaReservedGB))
	ceilReservedMemoryGB := perNumaReservedGB * uint64(numasCount)

	general.Infof("reservedMemoryGB: %.2f, ceilReservedMemoryGB: %d, perNumaReservedGB: %d, numasCount: %d",
		reservedMemoryGB, ceilReservedMemoryGB, perNumaReservedGB, numasCount)

	reservedMemory := make(map[int]uint64)
	for _, node := range machineInfo.Topology {
		reservedMemory[node.Id] = uint64(perNumaReservedQuantity.Value())
	}
	return reservedMemory, nil
}

// getReservedHugePagesMemory gets the reserved memory for the hugepages resource name per numa level.
func getReservedHugePagesMemory(conf *config.Configuration, metaServer *metaserver.MetaServer, machineInfo *info.MachineInfo,
	resourceName v1.ResourceName,
) (map[int]uint64, error) {
	numaNodes := []int{}
	for _, node := range machineInfo.Topology {
		numaNodes = append(numaNodes, node.Id)
	}

	if len(numaNodes) == 0 {
		return nil, fmt.Errorf("[memory_plugin] machine topology numa node number is zero")
	}

	nodeNumber := int64(len(numaNodes))
	sort.Ints(numaNodes)

	reservedMemory := make(map[int]uint64)
	var reservedBytes int64
	if conf.UseKubeletReservedConfig {
		klConfig, err := metaServer.GetKubeletConfig(context.TODO())
		if err != nil {
			return nil, fmt.Errorf("failed to get kubelet config: %v", err)
		}

		reservedMemoryInfo, err := utilkubeconfig.GetReservedMemoryInfo(klConfig)
		if err == nil && len(reservedMemoryInfo) != 0 {
			for _, numaNode := range numaNodes {
				if reservedMem, ok := reservedMemoryInfo[int32(numaNode)]; ok {
					quantity := reservedMem[resourceName]
					reservedMemory[numaNode] = uint64(quantity.Value())
				}
			}
			general.Infof("get numa reserved %v:%v bytes from kubelet config", resourceName, reservedMemory)
			return reservedMemory, nil
		}

		reservedQuantity, found, err := utilkubeconfig.GetReservedQuantity(klConfig, string(resourceName))
		if err != nil {
			return nil, fmt.Errorf("GetReservedQuantity failed with error: %v", err)
		}
		reservedBytes = reservedQuantity.Value()
		general.Infof("get reserved %v:%d bytes from kubelet config, found: %v", resourceName, reservedBytes, found)
	} else {
		if len(conf.ReservedNumaMemory) > 0 {
			for _, numaNode := range numaNodes {
				if reservedMem, ok := conf.ReservedNumaMemory[int32(numaNode)]; ok {
					quantity := reservedMem[resourceName]
					reservedMemory[numaNode] = uint64(quantity.Value())
				}
			}
			general.Infof("get numa reserved %v:%v bytes from ReservedNumaMemory configuration", resourceName, reservedMemory)
			return reservedMemory, nil
		}

		reservedQuantity := resource.MustParse(fmt.Sprintf("%dGi", conf.ReservedMemoryGB))
		reservedBytes = reservedQuantity.Value()
		general.Infof("get reserved %v:%d bytes from ReservedMemoryGB configuration", resourceName, reservedBytes)
	}

	integerPart := uint64(reservedBytes / nodeNumber)
	remainder := uint64(reservedBytes % nodeNumber)
	for _, node := range numaNodes {
		reservedMemory[node] = integerPart
	}

	reservedMemory[numaNodes[0]] = reservedMemory[numaNodes[0]] + remainder
	return reservedMemory, nil
}

func applySidecarAllocationInfoFromMainContainer(sidecarAllocationInfo, mainAllocationInfo *state.AllocationInfo) bool {
	changed := false
	if !sidecarAllocationInfo.NumaAllocationResult.Equals(mainAllocationInfo.NumaAllocationResult) {
		sidecarAllocationInfo.NumaAllocationResult = mainAllocationInfo.NumaAllocationResult.Clone()
		changed = true
	}

	// Copy missing annotations from main container
	for key, value := range mainAllocationInfo.Annotations {
		if _, ok := sidecarAllocationInfo.Annotations[key]; !ok {
			sidecarAllocationInfo.Annotations[key] = value
			changed = true
		}
	}

	return changed
}
