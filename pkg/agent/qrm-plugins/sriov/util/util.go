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
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func ValidateRequestQuantity(req *pluginapi.ResourceRequest) error {
	reqInt, _, err := util.GetQuantityFromResourceReq(req)

	if err != nil {
		return fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	if reqInt != 1 {
		return fmt.Errorf("only support request 1 sriov nic")
	}

	return nil
}

func PackAllocationInfo(req *pluginapi.ResourceRequest, vf state.VfInfo, qosLevel string) *state.AllocationInfo {
	return &state.AllocationInfo{
		AllocationMeta: commonstate.GenerateGenericContainerAllocationMeta(req,
			commonstate.EmptyOwnerPoolName, qosLevel),
		PCIDevice: state.PCIDevice{
			Address: vf.PciAddress,
			RepName: vf.RepName,
			VfName:  vf.Name,
		},
		MountDevices: vf.RdmaDevices,
	}
}

func PackAllocationResponse(req *pluginapi.ResourceRequest, allocationInfo *state.AllocationInfo) *pluginapi.ResourceAllocationResponse {
	// todo: add pci device as annotations
	annotations := map[string]string{}

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
				string(consts.ResourceSriovNic): {
					IsNodeResource:    true,
					IsScalarResource:  true, // to avoid re-allocating
					AllocatedQuantity: 1,
					// todo: add RDMA devices
					Annotations: annotations,
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
	}
}
