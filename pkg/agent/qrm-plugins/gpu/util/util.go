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
	"math"

	pkgerrors "github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	qrmutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var ErrNoAvailableGPUMemoryHints = pkgerrors.New("no available gpu memory hints")

func GetNUMANodesCountToFitGPUReq(
	gpuReq float64, cpuTopology *machine.CPUTopology, gpuTopology *machine.DeviceTopology,
) (int, int, error) {
	if gpuTopology == nil {
		return 0, 0, fmt.Errorf("GetNUMANodesCountToFitGPUReq got nil gpuTopology")
	}

	numaCount := cpuTopology.NumNUMANodes
	if numaCount == 0 {
		return 0, 0, fmt.Errorf("there is no NUMA in cpuTopology")
	}

	if len(gpuTopology.Devices)%numaCount != 0 {
		general.Warningf("GPUs count %d cannot be evenly divisible by NUMA count %d", len(gpuTopology.Devices), numaCount)
	}

	gpusPerNUMA := (len(gpuTopology.Devices) + numaCount - 1) / numaCount
	numaCountNeeded := int(math.Ceil(gpuReq / float64(gpusPerNUMA)))
	if numaCountNeeded == 0 {
		numaCountNeeded = 1
	}
	if numaCountNeeded > numaCount {
		return 0, 0, fmt.Errorf("invalid gpu req: %.3f in topology with NUMAs count: %d and GPUs count: %d", gpuReq, numaCount, len(gpuTopology.Devices))
	}

	gpusCountNeededPerNUMA := int(math.Ceil(gpuReq / float64(numaCountNeeded)))
	return numaCountNeeded, gpusCountNeededPerNUMA, nil
}

func IsNUMAAffinityDevice(
	device string, deviceTopology *machine.DeviceTopology, hintNodes machine.CPUSet,
) bool {
	info, ok := deviceTopology.Devices[device]
	if !ok {
		general.Errorf("failed to find device info for device %s", device)
		return false
	}

	return machine.NewCPUSet(info.GetNUMANode()...).IsSubsetOf(hintNodes)
}

// GetGPUCount extracts GPU count from resource request
func GetGPUCount(req *pluginapi.ResourceRequest, deviceNames []string) (float64, sets.String, error) {
	gpuCount := float64(0)
	gpuNames := sets.NewString()

	for _, resourceName := range deviceNames {
		_, request, err := qrmutil.GetQuantityFromResourceRequests(req.ResourceRequests, resourceName, false)
		if err != nil && !errors.IsNotFound(err) {
			return 0, nil, err
		}
		gpuCount += request
		gpuNames.Insert(resourceName)
	}

	if gpuCount == 0 {
		return 0, gpuNames, fmt.Errorf("no available GPU count")
	}

	return gpuCount, gpuNames, nil
}
