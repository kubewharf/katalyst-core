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
	"fmt"
	"math"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func getProportionalSize(oldPoolSize, oldTotalSize, newTotalSize int, ceil bool) int {
	if ceil {
		return int(math.Ceil(float64(newTotalSize) * (float64(oldPoolSize) / float64(oldTotalSize))))
	} else {
		return int(float64(newTotalSize) * (float64(oldPoolSize) / float64(oldTotalSize)))
	}
}

func generateMachineStateFromPodEntries(topology *machine.CPUTopology, podEntries state.PodEntries, originMachineState state.NUMANodeMap) (state.NUMANodeMap, error) {
	return state.GenerateMachineStateFromPodEntries(topology, podEntries, originMachineState)
}

// updateAllocationInfoByReq updates allocationInfo by latest req when admitting active pod,
// because qos level and annotations will change after we support customized updater of enhancements and qos level
func updateAllocationInfoByReq(req *pluginapi.ResourceRequest, allocationInfo *state.AllocationInfo) error {
	if req == nil {
		return fmt.Errorf("updateAllocationInfoByReq got nil req")
	} else if allocationInfo == nil {
		return nil
	}

	if req.Annotations[apiconsts.PodAnnotationQoSLevelKey] != "" &&
		req.Annotations[apiconsts.PodAnnotationQoSLevelKey] != allocationInfo.QoSLevel {
		general.Infof("update allocationInfo QoS level from %s to %s",
			allocationInfo.QoSLevel, req.Annotations[apiconsts.PodAnnotationQoSLevelKey])
		allocationInfo.QoSLevel = req.Annotations[apiconsts.PodAnnotationQoSLevelKey]
	}

	allocationInfo.Annotations = general.DeepCopyMap(req.Annotations)
	return nil
}
