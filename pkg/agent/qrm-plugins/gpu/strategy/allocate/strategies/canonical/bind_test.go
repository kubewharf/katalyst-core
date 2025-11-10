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
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestCanonicalStrategy_Bind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		ctx            *allocate.AllocationContext
		sortedDevices  []string
		expectedResult *allocate.AllocationResult
		expectedErr    bool
	}{
		{
			name: "device topology is nil",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{
					PodNamespace:  "default",
					PodName:       "podName",
					ContainerName: "containerName",
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 2,
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2"},
			expectedErr:   true,
		},
		{
			name: "device request is greater than available devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{
					PodNamespace:  "default",
					PodName:       "podName",
					ContainerName: "containerName",
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest: 4,
				},
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {},
						"gpu-2": {},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2"},
			expectedErr:   true,
		},
		{
			name: "bind all the reusable devices first",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{
					PodNamespace:  "default",
					PodName:       "podName",
					ContainerName: "containerName",
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest:   2,
					ReusableDevices: []string{"gpu-1", "gpu-2"},
				},
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
						"gpu-4": {},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2"},
			},
		},
		{
			name: "bind all the reusable devices first and then the available devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &v1alpha1.ResourceRequest{
					PodNamespace:  "default",
					PodName:       "podName",
					ContainerName: "containerName",
				},
				DeviceReq: &v1alpha1.DeviceRequest{
					DeviceRequest:   4,
					ReusableDevices: []string{"gpu-1", "gpu-2"},
				},
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
						"gpu-4": {},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			canonicalStrategy := NewCanonicalStrategy()
			result, err := canonicalStrategy.Bind(tt.ctx, tt.sortedDevices)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.ElementsMatch(t, tt.expectedResult.AllocatedDevices, result.AllocatedDevices)
			}
		})
	}
}
