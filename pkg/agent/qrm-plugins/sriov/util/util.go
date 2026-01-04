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
	"encoding/json"
	"fmt"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	rdmaDevicePrefix = "/dev/infiniband"
	rdmaCmPath       = "/dev/infiniband/rdma_cm"
)

func ValidateRequestQuantity(req *pluginapi.ResourceRequest) error {
	if req == nil {
		return fmt.Errorf("got nil req")
	}

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
	Address   string `json:"address"`
	RepName   string `json:"repName"`
	VFName    string `json:"vfName"`
	Container string `json:"container"`
}

func PackResourceAllocationInfo(conf qrm.SriovAllocationConfig,
	allocationInfo *state.AllocationInfo) (*pluginapi.ResourceAllocationInfo, error) {
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
	if ibDevices := allocationInfo.VFInfo.IBDevices; len(ibDevices) > 0 {
		for _, device := range ibDevices {
			rdmaPath := filepath.Join(rdmaDevicePrefix, device)
			devices = append(devices, &pluginapi.DeviceSpec{
				HostPath:      rdmaPath,
				ContainerPath: rdmaPath,
				Permissions:   "rwm",
			})
		}
		devices = append(devices, &pluginapi.DeviceSpec{
			HostPath:      rdmaCmPath,
			ContainerPath: rdmaCmPath,
			Permissions:   "rw",
		})
	}

	return &pluginapi.ResourceAllocationInfo{
		IsNodeResource:    true,
		IsScalarResource:  true, // to avoid re-allocating
		AllocatedQuantity: 1,
		Annotations:       annotations,
		Devices:           devices,
	}, nil
}

func PackAllocationResponse(conf qrm.SriovAllocationConfig,
	req *pluginapi.ResourceRequest, allocationInfo *state.AllocationInfo) (*pluginapi.ResourceAllocationResponse, error) {
	resourceAllocationInfo, err := PackResourceAllocationInfo(conf, allocationInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to pack resource allocation info: %v", err)
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
				string(consts.ResourceSriovNic): resourceAllocationInfo,
			},
		},
		Labels:      general.DeepCopyMap(req.Labels),
		Annotations: general.DeepCopyMap(req.Annotations),
	}, nil
}

func UpdateSriovVFResultAnnotation(client kubernetes.Interface, allocationInfo *state.AllocationInfo) error {
	annotationPatch := fmt.Sprintf(`[{"op": "add", "path": "/metadata/annotations/%s", "value": "%s"}]`,
		apiconsts.PodAnnotationSriovVFResultKey, allocationInfo.VFInfo.PCIAddr)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := client.CoreV1().Pods(allocationInfo.PodNamespace).Patch(context.Background(), allocationInfo.PodName,
			types.JSONPatchType, []byte(annotationPatch), metav1.PatchOptions{})
		return err
	})

	return err
}
