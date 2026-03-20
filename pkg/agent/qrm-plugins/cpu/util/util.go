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

	pkgerrors "github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	utilkubeconfig "github.com/kubewharf/katalyst-core/pkg/util/kubelet/config"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/qos"
)

const (
	defaultCPUBurstPercent = 100
)

var (
	ErrNoAvailableCPUHints             = pkgerrors.New("no available cpu hints")
	ErrNoAvailableMemoryBandwidthHints = pkgerrors.New("no available memory bandwidth hints")
)

// GetCoresReservedForSystem calculates the CPU cores reserved for the system based on the configuration.
// It prioritizes the kubelet configuration if enabled, otherwise falls back to the static configuration.
// It returns a CPUSet representing the reserved cores.
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

		// Parse the reserved cpu list from the kubelet configuration.
		reservedCPUList, _ := utilkubeconfig.GetReservedSystemCPUList(klConfig)
		if reservedCPUList != "" {
			conf.ReservedCPUList = reservedCPUList
			klog.Infof("get reservedCPUs: %s from kubelet config", reservedCPUList)

			reservedCPUSet := machine.MustParse(reservedCPUList)
			conf.ReservedCPUCores = reservedCPUSet.Size()
			return reservedCPUSet, nil
		}

		// If the reserved cpu list conf is not found, the reservation quantity is parsed from the kubelet configuration.
		reservedQuantity, found, err := utilkubeconfig.GetReservedQuantity(klConfig, string(v1.ResourceCPU))
		if err != nil {
			return machine.NewCPUSet(), fmt.Errorf("GetKubeletReservedQuantity failed with error: %v", err)
		} else {
			reservedQuantityFloat := float64(reservedQuantity.MilliValue()) / 1000
			reservedQuantityInt = int(math.Ceil(reservedQuantityFloat))

			general.Infof("get reservedQuantityInt: %d from kubelet config, found: %v", reservedQuantityInt, found)
		}
	} else {
		// Prioritize obtaining the reserved cpu list.
		reservedCPUList := conf.ReservedCPUList
		if reservedCPUList != "" {
			reservedCPUSet := machine.MustParse(reservedCPUList)
			klog.Infof("get reservedCPUs: %s from ReservedCPUList configuration %s", reservedCPUSet.String(), reservedCPUList)

			conf.ReservedCPUCores = reservedCPUSet.Size()
			return reservedCPUSet, nil
		}

		reservedQuantityInt = conf.ReservedCPUCores
		general.Infof("get reservedQuantityInt: %d from ReservedCPUCores configuration", reservedQuantityInt)
	}

	takeFn := calculator.TakeHTByNUMABalance
	if conf.EnableReserveCPUReversely {
		takeFn = calculator.TakeHTByNUMABalanceReversely
	}
	reservedCPUs, _, reserveErr := takeFn(machineInfo, allCPUs, reservedQuantityInt)
	if reserveErr != nil {
		return reservedCPUs, fmt.Errorf("takeByNUMABalance for reservedCPUsNum: %d failed with error: %v",
			reservedQuantityInt, reserveErr)
	}
	conf.ReservedCPUList = reservedCPUs.String()

	general.Infof("take reservedCPUs: %s by reservedCPUsNum: %d", reservedCPUs.String(), reservedQuantityInt)
	return reservedCPUs, nil
}

// RegenerateHints regenerates hints for container that'd already been allocated cpu,
// and regenerateHints will assemble hints based on already-existed AllocationInfo,
// without any calculation logics at all
func RegenerateHints(allocationInfo *state.AllocationInfo, regenerate bool) map[string]*pluginapi.ListOfTopologyHints {
	if allocationInfo == nil {
		general.Errorf("RegenerateHints got nil allocationInfo")
		return nil
	}

	hints := map[string]*pluginapi.ListOfTopologyHints{}

	if regenerate {
		general.ErrorS(nil, "need to regenerate hints",
			"podNamespace", allocationInfo.PodNamespace,
			"podName", allocationInfo.PodName,
			"podUID", allocationInfo.PodUid,
			"containerName", allocationInfo.ContainerName)

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

// AdvisorDegradation checks if the advisor is in a degraded state.
// It returns true if the advisor is unhealthy and reclaim is disabled.
func AdvisorDegradation(advisorHealth, enableReclaim bool) bool {
	advisorDegradation := !advisorHealth && !enableReclaim

	general.Infof("advisorDegradation: %v", advisorDegradation)

	return advisorDegradation
}

// CPUIsSufficient checks if the requested CPU is within the available limits,
// considering a small tolerance for floating point comparison.
func CPUIsSufficient(request, available float64) bool {
	// the minimal CPU core is 0.001 (1core = 1000m)
	return request < available+0.0001
}

// GetContainerRequestedCores parses and returns request cores for the given container
func GetContainerRequestedCores(metaServer *metaserver.MetaServer, allocationInfo *state.AllocationInfo) float64 {
	if allocationInfo == nil {
		general.Errorf("got nil allocationInfo")
		return 0
	}

	if metaServer == nil {
		general.Errorf("got nil metaServer")
		return allocationInfo.RequestQuantity
	}

	container, err := metaServer.GetContainerSpec(allocationInfo.PodUid, allocationInfo.ContainerName)
	if err != nil || container == nil {
		general.Errorf("get container failed with error: %v", err)
		return allocationInfo.RequestQuantity
	}

	cpuQuantity := native.CPUQuantityGetter()(container.Resources.Requests)
	metaValue := general.MaxFloat64(float64(cpuQuantity.MilliValue())/1000.0, 0)

	// optimize this logic someday:
	//	only for refresh cpu request for old pod with cpu ceil and old inplace update resized pods.
	if allocationInfo.CheckShared() {
		// if there is these two annotations in memory state, it is a new pod,
		// we don't need to check the pod request from podWatcher
		if allocationInfo.Annotations[consts.PodAnnotationAggregatedRequestsKey] != "" ||
			allocationInfo.Annotations[consts.PodAnnotationInplaceUpdateResizingKey] != "" {
			return allocationInfo.RequestQuantity
		}
		if allocationInfo.CheckNUMABinding() {
			if metaValue < allocationInfo.RequestQuantity {
				general.Infof("[snb] get cpu request quantity: (%.3f->%.3f) for pod: %s/%s container: %s from podWatcher",
					allocationInfo.RequestQuantity, metaValue, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				return metaValue
			}
		} else {
			if metaValue != allocationInfo.RequestQuantity {
				general.Infof("[share] get cpu request quantity: (%.3f->%.3f) for pod: %s/%s container: %s from podWatcher",
					allocationInfo.RequestQuantity, metaValue, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				return metaValue
			}
		}
	} else if allocationInfo.RequestQuantity == 0 {
		general.Infof("[other] get cpu request quantity: (%.3f->%.3f) for pod: %s/%s container: %s from podWatcher",
			allocationInfo.RequestQuantity, metaValue, allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
		return metaValue
	}

	return allocationInfo.RequestQuantity
}

// PopulateHintsByAvailableNUMANodes appends topology hints for the given NUMA nodes to the hints list.
func PopulateHintsByAvailableNUMANodes(
	numaNodes []int,
	hints *pluginapi.ListOfTopologyHints,
	preferred bool,
) {
	if hints == nil {
		general.Errorf("got nil hints")
		return
	}

	for _, nodeID := range numaNodes {
		hints.Hints = append(hints.Hints, &pluginapi.TopologyHint{
			Nodes:     []uint64{uint64(nodeID)},
			Preferred: preferred,
		})
	}
}

// GetPodCPUBurstPolicy gets the cpu burst policy of a given pod.
func GetPodCPUBurstPolicy(conf *config.Configuration, pod *v1.Pod, dynamicConfig *dynamic.DynamicAgentConfiguration,
	isSoleSharedCoresPod bool,
) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("got nil pod")
	}

	qosConf := conf.QoSConfiguration
	if qosConf == nil {
		return "", fmt.Errorf("got nil QoSConfiguration")
	}

	cpuBurstPolicy := qos.GetPodCPUBurstPolicyFromCPUEnhancement(qosConf, pod)

	// We may override cpu burst policy of dedicated cores pods with default values in dynamic config.
	qosLevel, _ := qosConf.GetQoSLevel(pod, map[string]string{})
	if qosLevel == consts.PodAnnotationQoSLevelDedicatedCores {
		cpuBurstPolicy = getOverriddenDedicatedCoresPodBurstPolicy(conf, dynamicConfig, cpuBurstPolicy)
	} else if qosLevel == consts.PodAnnotationQoSLevelSharedCores {
		cpuBurstPolicy = getOverriddenSharedCoresPodBurstPolicy(conf, dynamicConfig, cpuBurstPolicy, isSoleSharedCoresPod)
	}

	return cpuBurstPolicy, nil
}

// getOverriddenDedicatedCoresPodBurstPolicy returns the cpu burst policy for a given dedicated cores pod by checking with
// core config and dynamic config. Only pods with cpu burst policy default from their annotations will be overridden.
func getOverriddenDedicatedCoresPodBurstPolicy(conf *config.Configuration, dynamicConfig *dynamic.DynamicAgentConfiguration,
	originalBurstPolicy string,
) string {
	// return original burst policy if it is not default
	if originalBurstPolicy != consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault {
		return originalBurstPolicy
	}

	if conf.EnableDefaultDedicatedCoresCPUBurst {
		return consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic
	}

	if dynamicConfig != nil && dynamicConfig.GetDynamicConfiguration().EnableDedicatedCoresDefaultCPUBurst != nil {
		if *dynamicConfig.GetDynamicConfiguration().EnableDedicatedCoresDefaultCPUBurst {
			return consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic
		} else {
			// If EnableDedicatedCoresDefaultCPUBurst is explicitly false, we will close the cpu burst policy.
			return consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed
		}
	}

	return consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault
}

// getOverriddenSharedCoresPodBurstPolicy returns the cpu burst policy for a given shared cores pod by checking with
// core config and dynamic config. Pods will have overridden annotations if
// 1. It is the only shared cores pod in the node.
// 2. It has cpu burst policy default from their annotations.
func getOverriddenSharedCoresPodBurstPolicy(conf *config.Configuration, dynamicConfig *dynamic.DynamicAgentConfiguration,
	originalBurstPolicy string, isSoleSharedCoresPod bool,
) string {
	// return original burst policy if it is not default
	if originalBurstPolicy != consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault {
		return originalBurstPolicy
	}

	if conf.EnableDefaultSharedCoresCPUBurst {
		if isSoleSharedCoresPod {
			return consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic
		} else {
			// Close the cpu burst policy if EnableDefaultSharedCoresCPUBurst is true, but it is not the sole shared cores pod.
			// This is to ensure that pods with non-zero cpu burst values will now have zero cpu burst values.
			return consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed
		}
	}

	if dynamicConfig != nil && dynamicConfig.GetDynamicConfiguration().EnableSharedCoresDefaultCPUBurst != nil {
		if *dynamicConfig.GetDynamicConfiguration().EnableSharedCoresDefaultCPUBurst && isSoleSharedCoresPod {
			return consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic
		} else {
			// If EnableSharedCoresDefaultCPUBurst is explicitly false, we will close the cpu burst policy.
			return consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed
		}
	}

	return consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault
}

// GetPodCPUBurstPercent gets the cpu burst percent of a given pod.
func GetPodCPUBurstPercent(qosConf *generic.QoSConfiguration, pod *v1.Pod, dynamicConfig *dynamic.DynamicAgentConfiguration) (float64, error) {
	if pod == nil {
		return 0, fmt.Errorf("got nil pod")
	}

	if qosConf == nil {
		return 0, fmt.Errorf("got nil QoSConfiguration")
	}
	percent, found, err := qos.GetPodCPUBurstPercentFromCPUEnhancement(qosConf, pod)
	if err != nil {
		return 0.0, fmt.Errorf("failed to get pod CPUBurstPercent from cpu enhancement: %v", err)
	}

	// if not found, return default percent
	if !found {
		return getDefaultCPUBurstPercent(dynamicConfig), nil
	}

	return percent, nil
}

func getDefaultCPUBurstPercent(dynamicConfig *dynamic.DynamicAgentConfiguration) float64 {
	if dynamicConfig != nil {
		return float64(dynamicConfig.GetDynamicConfiguration().DefaultCPUBurstPercent)
	}

	return defaultCPUBurstPercent
}

// CalculateCPUBurstFromPercent calculates the CPU burst value based on the given percentage and CPU quota.
func CalculateCPUBurstFromPercent(percent float64, cpuQuota int64) uint64 {
	return uint64(float64(cpuQuota) * percent / 100)
}

// IsSoleSharedCoresPod returns true if there is only one shared cores pod in the node.
// Keeps the original dynamic-config gating, and also calculates when core config
// EnableDefaultSharedCoresCPUBurst is true.
func IsSoleSharedCoresPod(conf *config.Configuration, podList []*v1.Pod, dynamicConfig *dynamic.DynamicAgentConfiguration) bool {
	qosConfig := conf.QoSConfiguration

	shouldCalculate := false
	if dynamicConfig != nil && dynamicConfig.GetDynamicConfiguration().EnableSharedCoresDefaultCPUBurst != nil {
		if *dynamicConfig.GetDynamicConfiguration().EnableSharedCoresDefaultCPUBurst {
			shouldCalculate = true
		}
	}
	if conf.EnableDefaultSharedCoresCPUBurst {
		shouldCalculate = true
	}

	if shouldCalculate {
		numSharedCoresPods := 0
		for _, pod := range podList {
			qosLevel, _ := qosConfig.GetQoSLevel(pod, map[string]string{})
			if qosLevel == consts.PodAnnotationQoSLevelSharedCores {
				numSharedCoresPods++
			}
		}
		return numSharedCoresPods == 1
	}

	return false
}

// GetAggResourcePackagePinnedCPUSet aggregates pinned CPUSets from resource packages that match the given attribute selector.
// It iterates through each NUMA node's resource packages, filters them using the attribute selector,
// and unions the pinned CPUSets from the corresponding machine state.
func GetAggResourcePackagePinnedCPUSet(attributeSelector labels.Selector, machineState state.NUMANodeMap) machine.CPUSet {
	res := machine.NewCPUSet()
	numaStates := machineState.GetNUMAResourcePackageStates()
	for _, pkgStates := range numaStates {
		for _, rpState := range pkgStates {
			if rpState == nil {
				continue
			}
			if !attributeSelector.Matches(labels.Set(rpState.Attributes)) {
				continue
			}
			res = res.Union(rpState.PinnedCPUSet)
		}
	}
	return res
}
