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

package util

import (
	"context"
	"fmt"
	"math"

	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilkubeconfig "github.com/kubewharf/katalyst-core/pkg/util/kubelet/config"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	defaultCFSPeriod int64 = 100000
)

func GetCoresReservedForSystem(conf *config.Configuration, metaServer *metaserver.MetaServer, machineInfo *machine.KatalystMachineInfo, allCPUs machine.CPUSet) (machine.CPUSet, error) {
	if conf == nil {
		return machine.NewCPUSet(), fmt.Errorf("nil conf")
	} else if metaServer == nil {
		return machine.NewCPUSet(), fmt.Errorf("nil metaServer")
	} else if machineInfo == nil {
		return machine.NewCPUSet(), fmt.Errorf("nil machineInfo")
	}

	var reservedQuantityInt int
	if conf.UseKubeletReservedConfig {
		klConfig, err := metaServer.GetKubeletConfig(context.TODO())
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("failed to get kubelet config: %v", err)
		}

		reservedQuantity, found, err := utilkubeconfig.GetReservedQuantity(klConfig, string(v1.ResourceCPU))
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("GetKubeletReservedQuantity failed with error: %v", err)
		} else {
			reservedQuantityFloat := float64(reservedQuantity.MilliValue()) / 1000
			reservedQuantityInt = int(math.Ceil(reservedQuantityFloat))

			general.Infof("get reservedQuantityInt: %d from kubelet config, found: %v", reservedQuantityInt, found)
		}
	} else {
		reservedQuantityInt = conf.ReservedCPUCores
		general.Infof("get reservedQuantityInt: %d from ReservedCPUCores configuration", reservedQuantityInt)
	}

	reservedCPUs, _, reserveErr := calculator.TakeHTByNUMABalance(machineInfo, allCPUs, reservedQuantityInt)
	if reserveErr != nil {
		return reservedCPUs, fmt.Errorf("takeByNUMABalance for reservedCPUsNum: %d failed with error: %v",
			reservedQuantityInt, reserveErr)
	}

	general.Infof("take reservedCPUs: %s by reservedCPUsNum: %d", reservedCPUs.String(), reservedQuantityInt)
	return reservedCPUs, nil
}

// RegenerateHints regenerates hints for container that'd already been allocated cpu,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func RegenerateHints(allocationInfo *state.AllocationInfo, reqInt int) map[string]*pluginapi.ListOfTopologyHints {
	if allocationInfo == nil {
		general.Errorf("RegenerateHints got nil allocationInfo")
		return nil
	}

	hints := map[string]*pluginapi.ListOfTopologyHints{}

	if allocationInfo.OriginalAllocationResult.Size() < reqInt {
		general.ErrorS(nil, "cpus already allocated with smaller quantity than requested",
			"podUID", allocationInfo.PodUid,
			"containerName", allocationInfo.ContainerName,
			"requestedResource", reqInt,
			"allocatedSize", allocationInfo.OriginalAllocationResult.Size())

		return nil
	}

	allocatedNumaNodes := allocationInfo.GetAllocationResultNUMASet().ToSliceUInt64()

	general.InfoS("regenerating machineInfo hints, cpus was already allocated to pod",
		"podNamespace", allocationInfo.PodNamespace,
		"podName", allocationInfo.PodName,
		"containerName", allocationInfo.ContainerName,
		"hint", allocatedNumaNodes)
	hints[string(v1.ResourceCPU)] = &pluginapi.ListOfTopologyHints{
		Hints: []*pluginapi.TopologyHint{
			{
				Nodes:     allocatedNumaNodes,
				Preferred: true,
			},
		},
	}
	return hints
}

// PackAllocationResponse fills pluginapi.ResourceAllocationResponse with information from AllocationInfo and pluginapi.ResourceRequest
func PackAllocationResponse(allocationInfo *state.AllocationInfo, resourceName, ociPropertyName string,
	isNodeResource, isScalarResource bool, req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceAllocationResponse, error) {
	if allocationInfo == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil allocationInfo")
	} else if req == nil {
		return nil, fmt.Errorf("packAllocationResponse got nil request")
	}

	return &pluginapi.ResourceAllocationResponse{
		PodUid:         req.PodUid,
		PodNamespace:   req.PodNamespace,
		PodName:        req.PodName,
		ContainerName:  req.ContainerName,
		ContainerType:  req.ContainerType,
		ContainerIndex: req.ContainerIndex,
		PodRole:        req.PodRole,
		PodType:        req.PodType,
		ResourceName:   resourceName,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				resourceName: {
					OciPropertyName:   ociPropertyName,
					IsNodeResource:    isNodeResource,
					IsScalarResource:  isScalarResource,
					AllocatedQuantity: float64(allocationInfo.AllocationResult.Size()),
					AllocationResult:  allocationInfo.AllocationResult.String(),
					ResourceHints: &pluginapi.ListOfTopologyHints{
						Hints: []*pluginapi.TopologyHint{
							req.Hint,
						},
					},
				},
			},
		},
		Labels:         general.DeepCopyMap(req.Labels),
		Annotations:    general.DeepCopyMap(req.Annotations),
		NativeQosClass: req.NativeQosClass,
	}, nil
}

func AdvisorDegradation(advisorHealth, enableReclaim bool) bool {
	advisorDegradation := !advisorHealth && !enableReclaim

	general.Infof("advisorDegradation: %v", advisorDegradation)

	return advisorDegradation
}

func CPUIsSufficient(request, available float64) bool {
	// the minimal CPU core is 0.001 (1core = 1000m)
	return request < available+0.0001
}

// GetCPUBurstPolicy returns the specified cpu burst policy
func GetCPUBurstPolicy(pod *v1.Pod, qosConfig *generic.QoSConfiguration, conf *dynamic.DynamicAgentConfiguration) string {
	ok, policy := qos.GetPodCPUBurstPolicy(qosConfig, pod)
	if ok {
		return policy
	}

	return conf.GetDynamicConfiguration().CPUBurstPolicy
}

// GetCPUBurstPercent returns the upper limit of the allowed burst percent
func GetCPUBurstPercent(pod *v1.Pod, qosConfig *generic.QoSConfiguration, conf *dynamic.DynamicAgentConfiguration) (int64, error) {
	ok, percent, err := qos.GetPodCPUBurstPercent(qosConfig, pod)
	if err != nil {
		return 0, err
	}
	if ok {
		return percent, nil
	}

	return conf.GetDynamicConfiguration().CPUBurstPercent, nil
}

// CalcCPUBurstVal caculates the cpu burst value in cgroup file
func CalcCPUBurstVal(container *v1.Container, cpuBurstPercent int64) int64 {
	containerCPUMilliLimit := native.GetContainerMilliCPULimit(container)
	if containerCPUMilliLimit <= 0 {
		return 0
	}

	cpuBurstInCores := (float64(containerCPUMilliLimit) / 1000) * (float64(cpuBurstPercent) / 100)
	cpuBurstVal := int64(cpuBurstInCores * float64(defaultCFSPeriod))

	return cpuBurstVal
}

// ApplyCPUBurstConfigForContainer applies the cpu burst config to cgroup file for a container
func ApplyCPUBurstConfigForContainer(podUID, containerName, containerID string, cpuData *common.CPUData) {
	if err := cgroupcmutils.ApplyCPUForContainer(podUID, containerID, cpuData); err != nil {
		general.Errorf("apply cpu burst failed, pod: %s, container: %s(%s), cpuData: %+v, err: %v",
			podUID, containerName, containerID, *cpuData, err)
		return
	}

	klog.V(2).Infof("apply cpu burst for container successfully, pod: %s, container: %s(%s), cpuData: %+v",
		podUID, containerName, containerID, *cpuData)
}
