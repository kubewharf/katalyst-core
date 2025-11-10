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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCanonicalStrategy_Filter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		ctx                     *allocate.AllocationContext
		availableDevices        []string
		expectedFilteredDevices []string
	}{
		{
			name: "empty hint nodes does not filter",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							NumaNodes: []int{0, 1},
						},
						"gpu-2": {
							NumaNodes: []int{2, 3},
						},
					},
				},
			},
			availableDevices:        []string{"gpu-1", "gpu-2"},
			expectedFilteredDevices: []string{"gpu-1", "gpu-2"},
		},
		{
			name: "filtered devices by hint nodes",
			ctx: &allocate.AllocationContext{
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							NumaNodes: []int{0, 1},
						},
						"gpu-2": {
							NumaNodes: []int{2, 3},
						},
					},
				},
				HintNodes: machine.NewCPUSet(0, 1, 2),
			},
			availableDevices:        []string{"gpu-1", "gpu-2"},
			expectedFilteredDevices: []string{"gpu-1"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			canonicalStrategy := NewCanonicalStrategy()
			actualFilteredDevices, err := canonicalStrategy.Filter(tt.ctx, tt.availableDevices)
			assert.NoError(t, err)
			assert.ElementsMatch(t, tt.expectedFilteredDevices, actualFilteredDevices, "filtered devices are not equal")
		})
	}
}
