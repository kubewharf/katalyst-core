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
	"encoding/json"
	"fmt"
	"path/filepath"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	rdmaDevicePrefix = "/dev/infiniband"
	rdmaCmPath       = "/dev/infiniband/rdma_cm"
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

type PCIDevice struct {
	Address string `json:"address"`
	RepName string `json:"repName"`
	VFName  string `json:"vfName"`
}

func PackAllocationResponse(conf qrm.SriovAllocationConfig,
	req *pluginapi.ResourceRequest,
	allocationInfo *state.AllocationInfo) (*pluginapi.ResourceAllocationResponse, error) {
	annotations := general.DeepCopyMap(conf.ExtraAnnotations)
	pciAnnotationValue, err := json.Marshal([]PCIDevice{
		{
			Address: allocationInfo.VFInfo.PCIAddr,
			RepName: allocationInfo.VFInfo.RepName,
			VFName:  allocationInfo.VFInfo.Name,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal pci device: %v", err)
	}
	annotations[conf.PCIAnnotation] = string(pciAnnotationValue)

	var devices []*pluginapi.DeviceSpec
	if ibDevName := allocationInfo.VFInfo.IBDevName; ibDevName != "" {
		rdmaPath := filepath.Join(rdmaDevicePrefix, allocationInfo.VFInfo.IBDevName)
		devices = []*pluginapi.DeviceSpec{
			{
				HostPath:      rdmaPath,
				ContainerPath: rdmaPath,
				Permissions:   "rwm",
			},
			{
				HostPath:      rdmaCmPath,
				ContainerPath: rdmaCmPath,
				Permissions:   "rw",
			},
		}
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
					Devices: devices,
				},
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}
