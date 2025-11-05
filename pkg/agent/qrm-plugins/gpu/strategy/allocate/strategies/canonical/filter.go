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

package canonical

import (
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	gpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
)

// Filter filters the available devices based on whether they are already occupied.
// The assumption is that each device can only be allocated to one container at most.
// It only returns devices that are not occupied yet.
func (s *CanonicalStrategy) Filter(
	ctx *allocate.AllocationContext, allAvailableDevices []string,
) ([]string, error) {
	filteredDevices := sets.NewString()
	for _, device := range allAvailableDevices {
		if !ctx.HintNodes.IsEmpty() && !gpuutil.IsNUMAAffinityDevice(device, ctx.DeviceTopology, ctx.HintNodes) {
			continue
		}

		filteredDevices.Insert(device)
	}

	return filteredDevices.UnsortedList(), nil
}
