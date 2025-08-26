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

package staticpolicy

import (
	"fmt"
	"math"
	"math/rand"
	"time"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/state"
	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type (
	ReservationPolicy  string
	NICSelectionPoligy string
)

const (
	FirstNIC         ReservationPolicy = "first"
	EvenDistribution ReservationPolicy = "even"

	RandomOne NICSelectionPoligy = "random"
	FirstOne  NICSelectionPoligy = "first"
	LastOne   NICSelectionPoligy = "last"

	LowPriorityGroupNameSuffix = "low_priority"
)

type NICFilter func(nics []machine.InterfaceInfo, req *pluginapi.ResourceRequest, agentCtx *agent.GenericContext) []machine.InterfaceInfo

// isReqAffinityRestricted returns true if allocated network interface must have affinity with allocated numa
func isReqAffinityRestricted(reqAnnotations map[string]string) bool {
	return reqAnnotations[consts.PodAnnotationNetworkEnhancementAffinityRestricted] ==
		consts.PodAnnotationNetworkEnhancementAffinityRestrictedTrue
}

// checkNICPreferenceOfReq returns true if allocate network interface matches up with the
// preference of requests, and it will return error if it breaks hard restrictions.
func checkNICPreferenceOfReq(nic machine.InterfaceInfo, reqAnnotations map[string]string) (bool, error) {
	switch reqAnnotations[consts.PodAnnotationNetworkEnhancementNamespaceType] {
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeHost:
		if nic.NSName == machine.DefaultNICNamespace {
			return true, nil
		} else {
			return false, fmt.Errorf("checkNICPreferenceOfReq got invalid nic: %s with %s: %s, NSName: %s",
				nic.Name, consts.PodAnnotationNetworkEnhancementNamespaceType,
				consts.PodAnnotationNetworkEnhancementNamespaceTypeHost, nic.NSName)
		}
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeHostPrefer:
		if nic.NSName == machine.DefaultNICNamespace {
			return true, nil
		} else {
			return false, nil
		}
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHost:
		if nic.NSName != machine.DefaultNICNamespace {
			return true, nil
		} else {
			return false, fmt.Errorf("checkNICPreferenceOfReq got invalid nic: %s with %s: %s, NSName: %s",
				nic.Name, consts.PodAnnotationNetworkEnhancementNamespaceType,
				consts.PodAnnotationNetworkEnhancementNamespaceTypeHost, nic.NSName)
		}
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHostPrefer:
		if nic.NSName != machine.DefaultNICNamespace {
			return true, nil
		} else {
			return false, nil
		}
	default:
		// there is no preference,
		// so any type will be preferred.
		return true, nil
	}
}

// filterAvailableNICsByReq walks through nicFilters to select the targeted network interfaces
func filterAvailableNICsByReq(nics []machine.InterfaceInfo, req *pluginapi.ResourceRequest, agentCtx *agent.GenericContext, nicFilters []NICFilter) ([]machine.InterfaceInfo, error) {
	if req == nil {
		return nil, fmt.Errorf("filterAvailableNICsByReq got nil req")
	} else if agentCtx == nil {
		return nil, fmt.Errorf("filterAvailableNICsByReq got nil agentCtx")
	}

	filteredNICs := nics
	for _, nicFilter := range nicFilters {
		filteredNICs = nicFilter(filteredNICs, req, agentCtx)
	}
	return filteredNICs, nil
}

func filterNICsByNamespaceType(nics []machine.InterfaceInfo, req *pluginapi.ResourceRequest, _ *agent.GenericContext) []machine.InterfaceInfo {
	filteredNICs := make([]machine.InterfaceInfo, 0, len(nics))

	for _, nic := range nics {
		filterOut := true
		switch req.Annotations[consts.PodAnnotationNetworkEnhancementNamespaceType] {
		case consts.PodAnnotationNetworkEnhancementNamespaceTypeHost:
			if nic.NSName == machine.DefaultNICNamespace {
				filteredNICs = append(filteredNICs, nic)
				filterOut = false
			}
		case consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHost:
			if nic.NSName != machine.DefaultNICNamespace {
				filteredNICs = append(filteredNICs, nic)
				filterOut = false
			}
		default:
			filteredNICs = append(filteredNICs, nic)
			filterOut = false
		}

		if filterOut {
			general.Infof("filter out nic: %s mismatching with enhancement %s: %s",
				nic.Name, consts.PodAnnotationNetworkEnhancementNamespaceType, consts.PodAnnotationNetworkEnhancementNamespaceTypeHost)
		}
	}

	if len(filteredNICs) == 0 {
		general.InfoS("nic list returned by filterNICsByNamespaceType is empty",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
	}

	return filteredNICs
}

func filterNICsByHint(nics []machine.InterfaceInfo, req *pluginapi.ResourceRequest, agentCtx *agent.GenericContext) []machine.InterfaceInfo {
	// means not to filter by hint (in topology hint calculation period)
	if req.Hint == nil {
		general.InfoS("req hint is nil, skip filterNICsByHint",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return nics
	}

	var exactlyMatchNIC *machine.InterfaceInfo
	hintMatchedNICs := make([]machine.InterfaceInfo, 0, len(nics))

	hintNUMASet, err := machine.NewCPUSetUint64(req.Hint.Nodes...)
	if err != nil {
		general.Errorf("NewCPUSetUint64 failed with error: %v, filter out all nics", err)
		return nil
	}

	for i, nic := range nics {
		allocateNUMAs, err := machine.GetNICAllocateNUMAs(nic, agentCtx.KatalystMachineInfo)
		if err != nil {
			general.Errorf("get allocateNUMAs for nic: %s failed with error: %v, filter out it", nic.Name, err)
			continue
		}

		if allocateNUMAs.Equals(hintNUMASet) {
			if exactlyMatchNIC == nil {
				general.InfoS("add hint exactly matched nic",
					"podNamespace", req.PodNamespace,
					"podName", req.PodName,
					"containerName", req.ContainerName,
					"nic", nic.Name,
					"allocateNUMAs", allocateNUMAs.String(),
					"hintNUMASet", hintNUMASet.String())
				exactlyMatchNIC = &nics[i]
			}
		} else if allocateNUMAs.IsSubsetOf(hintNUMASet) { // for pod affinity_restricted != true
			general.InfoS("add hint matched nic",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"nic", nic.Name,
				"allocateNUMAs", allocateNUMAs.String(),
				"hintNUMASet", hintNUMASet.String())
			hintMatchedNICs = append(hintMatchedNICs, nic)
		}
	}

	if exactlyMatchNIC != nil {
		return []machine.InterfaceInfo{*exactlyMatchNIC}
	} else {
		return hintMatchedNICs
	}
}

func getRandomNICs(nics []machine.InterfaceInfo) machine.InterfaceInfo {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return nics[r.Intn(len(nics))]
}

func selectOneNIC(nics []machine.InterfaceInfo, policy NICSelectionPoligy) machine.InterfaceInfo {
	if len(nics) == 0 {
		general.Errorf("no NIC to select")
		return machine.InterfaceInfo{}
	}

	switch policy {
	case RandomOne:
		return getRandomNICs(nics)
	case FirstOne:
		// since we only pass filtered nics, always picking the first or the last one actually indicates a kind of binpacking
		return nics[0]
	case LastOne:
		return nics[len(nics)-1]
	}

	// use LastOne as default
	return nics[len(nics)-1]
}

// packAllocationResponse fills pluginapi.ResourceAllocationResponse with information from AllocationInfo and pluginapi.ResourceRequest
func packAllocationResponse(req *pluginapi.ResourceRequest, allocationInfo *state.AllocationInfo, resourceAllocationAnnotations map[string]string) (*pluginapi.ResourceAllocationResponse, error) {
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
		ResourceName:   req.ResourceName,
		AllocationResult: &pluginapi.ResourceAllocation{
			ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
				string(consts.ResourceNetBandwidth): {
					IsNodeResource:    true,
					IsScalarResource:  true, // to avoid re-allocating
					AllocatedQuantity: float64(allocationInfo.Egress),
					AllocationResult:  allocationInfo.NumaNodes.String(),
					Annotations:       resourceAllocationAnnotations,
					ResourceHints: &pluginapi.ListOfTopologyHints{
						Hints: []*pluginapi.TopologyHint{
							req.Hint,
						},
					},
				},
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}

// getReservedBandwidth is used to spread total reserved bandwidth into per-nic level.
func getReservedBandwidth(nics []machine.InterfaceInfo, reservation uint32, policy ReservationPolicy) (map[string]uint32, error) {
	nicCount := len(nics)
	reservedBandwidth := make(map[string]uint32)

	if nicCount == 0 {
		return reservedBandwidth, nil
	}

	general.Infof("reservedBanwidth: %d, nicCount: %d, policy: %s, ",
		reservation, nicCount, policy)

	switch policy {
	case FirstNIC:
		reservedBandwidth[nics[0].Name] = reservation
	case EvenDistribution:
		for _, iface := range nics {
			reservedBandwidth[iface.Name] = reservation / uint32(nicCount)
		}
	default:
		return nil, fmt.Errorf("unsupported network bandwidth reservation policy: %s", policy)
	}

	return reservedBandwidth, nil
}

func getResourceIdentifier(ifaceNS, ifaceName string) string {
	if len(ifaceNS) > 0 {
		return fmt.Sprintf("%s-%s", ifaceNS, ifaceName)
	}

	return ifaceName
}

func applyImplicitReq(req *pluginapi.ResourceRequest, allocationInfo *state.AllocationInfo) error {
	if req == nil || allocationInfo == nil {
		return fmt.Errorf("nil req or allocationInfo")
	}

	if !isImplicitReq(req.Annotations) {
		return nil
	}

	allocationInfo.Annotations[state.NetBandwidthImplicitAnnotationKey] = fmt.Sprintf("%d",
		general.Max(int(math.Ceil(req.ResourceRequests[string(apiconsts.ResourceNetBandwidth)])), 0))
	return nil
}

func isImplicitReq(annotations map[string]string) bool {
	return annotations[qrmutil.PodAnnotationQuantityFromQRMDeclarationKey] == qrmutil.PodAnnotationQuantityFromQRMDeclarationTrue
}

func getGroupName(nicName, groupSuffix string) string {
	return fmt.Sprintf("%s_%s", nicName, groupSuffix)
}
