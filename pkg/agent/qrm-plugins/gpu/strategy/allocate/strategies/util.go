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

package strategies

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
)

// IsBindingContextValid checks if the context given for binding is valid
func IsBindingContextValid(ctx *allocate.AllocationContext, sortedDevices []string) (bool, string) {
	if ctx.DeviceTopology == nil {
		return false, "GPU topology is nil"
	}

	// Determine how many devices to allocate
	devicesToAllocate := int(ctx.DeviceReq.DeviceRequest)
	if devicesToAllocate > len(sortedDevices) {
		return false, fmt.Sprintf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices))
	}

	return true, ""
}
