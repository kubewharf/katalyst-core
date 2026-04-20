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

package deviceaffinity

import (
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func dimName(name, value string) map[string]string {
	return map[string]string{name: value}
}

func dimLevel(priorityLevel int, group string) map[string]string {
	// Bind tests use numeric priority dimensions (e.g. "0", "1", "2").
	return dimName(strconv.Itoa(priorityLevel), group)
}

func dims(parts ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, p := range parts {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

func TestBind_NumberOfDevicesAllocated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		ctx                *allocate.AllocationContext
		sortedDevices      []string
		expectedResult     *allocate.AllocationResult
		expectedErr        bool
		isRandom           bool
		expectedResultSize int
	}{
		{
			name: "able to allocate 1 device in affinity group of size 2",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   1,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-5": {
							Dimensions: dimLevel(0, "g56"),
						},
						"gpu-6": {
							Dimensions: dimLevel(0, "g56"),
						},
						"gpu-7": {
							Dimensions: dimLevel(0, "g78"),
						},
						"gpu-8": {
							Dimensions: dimLevel(0, "g78"),
						},
					},
				},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			isRandom:           true,
			expectedResultSize: 1,
		},
		{
			name: "able to allocate 2 devices in affinity group of size 2",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-5": {
							Dimensions: dimLevel(0, "g56"),
						},
						"gpu-6": {
							Dimensions: dimLevel(0, "g56"),
						},
						"gpu-7": {
							Dimensions: dimLevel(0, "g78"),
						},
						"gpu-8": {
							Dimensions: dimLevel(0, "g78"),
						},
					},
				},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			isRandom:           true,
			expectedResultSize: 2,
		},
		{
			name: "able to allocate 3 devices in affinity size of group 2",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   3,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g34"),
						},
					},
				},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			isRandom:           true,
			expectedResultSize: 3,
		},
		{
			name: "able to allocate 4 devices in affinity size of group 2",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g34"),
						},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
				Success:          true,
			},
		},
		{
			name: "able to allocate 2 devices in affinity size of group 4",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g1234"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g1234"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g1234"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g1234"),
						},
					},
				},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			isRandom:           true,
			expectedResultSize: 2,
		},
		{
			name: "allocate all reusable devices first",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2"},
					DeviceRequest:   2,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g34"),
						},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2"},
				Success:          true,
			},
		},
		{
			name: "allocate the reusable devices with the best affinity to each other first",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-3", "gpu-4"},
					DeviceRequest:   2, // should allocate gpu-3 and gpu-4
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-2": {
							Dimensions: dimLevel(0, "g12"),
						},
						"gpu-3": {
							Dimensions: dimLevel(0, "g34"),
						},
						"gpu-4": {
							Dimensions: dimLevel(0, "g34"),
						},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-3", "gpu-4"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-3", "gpu-4"},
				Success:          true,
			},
		},
		{
			name: "supports bin-packing of 1 allocated device",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-3"}, // gpu-4 is already allocated, so we should allocate gpu-3 to support bin-packing
					DeviceRequest:   1,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3"},
			// gpu-4 is already allocated, so we allocate gpu-3 for bin-packing
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-3"},
				Success:          true,
			},
		},
		{
			name: "supports bin-packing of 1 request with only available devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   1,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
			},
			sortedDevices: []string{"gpu1", "gpu2", "gpu-3", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-3"},
				Success:          true,
			},
		},
		{
			name: "supports of bin-packing of 2 allocated devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-3", "gpu-5", "gpu-6"}, // gpu-5 and gou-6 should be allocated because gpu-7 is already allocated and this supports bin-packing
					DeviceRequest:   2,
				},
				// Level 0: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g1234")},
						"gpu-2": {Dimensions: dimLevel(0, "g1234")},
						"gpu-3": {Dimensions: dimLevel(0, "g1234")},
						"gpu-4": {Dimensions: dimLevel(0, "g1234")},
						"gpu-5": {Dimensions: dimLevel(0, "g5678")},
						"gpu-6": {Dimensions: dimLevel(0, "g5678")},
						"gpu-7": {Dimensions: dimLevel(0, "g5678")},
						"gpu-8": {Dimensions: dimLevel(0, "g5678")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-8"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-5", "gpu-6"},
				Success:          true,
			},
		},
		{
			name: "bin-packing of more allocated devices are preferred over less allocated devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-2", "gpu-3", "gpu-6", "gpu-7", "gpu-9", "gpu-10"},
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8], [gpu-9, gpu-10, gpu-11, gpu-12]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-2":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-3":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-4":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-5":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-6":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-7":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-8":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-9":  {Dimensions: dimLevel(0, "g9-12")},
						"gpu-10": {Dimensions: dimLevel(0, "g9-12")},
						"gpu-11": {Dimensions: dimLevel(0, "g9-12")},
						"gpu-12": {Dimensions: dimLevel(0, "g9-12")},
					},
				},
			},
			sortedDevices: []string{"gpu-2", "gpu-3", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12"},
			// [gpu-1, gpu-2, gpu-3, gpu-4] already has 2 allocated devices, [gpu-5, gpu-6, gpu-7, gpu-8] has 1 allocated device, [gpu-9, gpu-10, gpu-11, gpu-12] has no allocated devices
			// To support bin-packing, we will allocate gpu-2 and gpu-3 first, then allocate gpu-6 and gpu-7
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-2", "gpu-3", "gpu-6", "gpu-7"},
				Success:          true,
			},
		},
		{
			name: "bin-packing of devices should be maintained with different affinityPriority dimensions",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-2", "gpu-3", "gpu-6", "gpu-7", "gpu-9", "gpu-10"},
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8], [gpu-9, gpu-10, gpu-11, gpu-12]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"numa"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1":  {Dimensions: dimName("numa", "numa-0")},
						"gpu-2":  {Dimensions: dimName("numa", "numa-0")},
						"gpu-3":  {Dimensions: dimName("numa", "numa-0")},
						"gpu-4":  {Dimensions: dimName("numa", "numa-0")},
						"gpu-5":  {Dimensions: dimName("numa", "numa-1")},
						"gpu-6":  {Dimensions: dimName("numa", "numa-1")},
						"gpu-7":  {Dimensions: dimName("numa", "numa-1")},
						"gpu-8":  {Dimensions: dimName("numa", "numa-1")},
						"gpu-9":  {Dimensions: dimName("numa", "numa-2")},
						"gpu-10": {Dimensions: dimName("numa", "numa-2")},
						"gpu-11": {Dimensions: dimName("numa", "numa-2")},
						"gpu-12": {Dimensions: dimName("numa", "numa-2")},
					},
				},
			},
			sortedDevices: []string{"gpu-2", "gpu-3", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-2", "gpu-3", "gpu-6", "gpu-7"},
				Success:          true,
			},
		},
		{
			name: "finds first level of device affinity, then finds second level of device affinity",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-7"}, // gpu-1 and gpu-2 have affinity in 1st level, gpu-5 and gpu-7 have affinity in 2nd level
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
					},
				},
			},
			// Allocate gpu-1, gpu-2 in first level, then allocate gpu-3 as it has affinity with gpu-1 and gpu-2
			// Then allocate gpu-5 as gpu-6 is already allocated
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-7", "gpu-8"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5"},
				Success:          true,
			},
		},
		{
			name: "priority levels can be non-consecutive",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-7"}, // gpu-1 and gpu-2 have affinity in 1st level, gpu-5 and gpu-7 have affinity in 2nd level
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				// Level 2: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(2, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(2, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(2, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(2, "g1234"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(2, "g5678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(2, "g5678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(2, "g5678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(2, "g5678"))},
					},
				},
			},
			// Allocate gpu-1, gpu-2 in first level, then allocate gpu-3 as it has affinity with gpu-1 and gpu-2
			// Then allocate gpu-5 as gpu-6 is already allocated
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-7", "gpu-8"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5"},
				Success:          true,
			},
		},
		{
			name: "allocate reusable devices first, then allocate available devices with affinity to the allocated reusable devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-5"},
					DeviceRequest:   4,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
				MachineState: map[v1.ResourceName]state.AllocationMap{
					"gpu": {
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
						"gpu-4": {},
						"gpu-5": {},
						"gpu-6": {},
						"gpu-7": {},
						"gpu-8": {},
					},
				},
			},
			// gpu-1 and gpu-5 are already allocated as they are reusable
			// gpu-2 and gpu-6 should be allocated as they have affinity to the already allocated gpu-1 and gpu-5
			sortedDevices: []string{"gpu-1", "gpu-5", "gpu-2", "gpu-6", "gpu-7", "gpu-8", "gpu-3", "gpu-4"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6"},
				Success:          true,
			},
		},
		{
			name: "after allocating available devices with affinity to reusable devices, still not enough devices, allocate more available devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-5"},
					DeviceRequest:   6,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7"},
			// gpu-1 and gpu-5 are allocated because they are reusable devices
			// gpu-2 and gpu-6 should be allocated as they have affinity to the already allocated gpu-1 and gpu-5
			// gpu-3 and gpu-4 should be allocated as they have affinity to each other
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6"},
				Success:          true,
			},
		},
		{
			name: "allocation of reusable devices in descending order of intersection size",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-9"},
					DeviceRequest:   6,
				},
				// 1 level: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8], [gpu-9, gpu-10, gpu-11, gpu-12]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-2":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-3":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-4":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-5":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-6":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-7":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-8":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-9":  {Dimensions: dimLevel(0, "g9-12")},
						"gpu-10": {Dimensions: dimLevel(0, "g9-12")},
						"gpu-11": {Dimensions: dimLevel(0, "g9-12")},
						"gpu-12": {Dimensions: dimLevel(0, "g9-12")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-9"},
			// Should allocate gpu-1, gpu-2, gpu-3 and gpu-4 first because they have the most intersection size with an affinity group
			// Followed by gpu-5 and gpu-6 as they have the second most intersection size with an affinity group
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6"},
				Success:          true,
			},
		},
		{
			name: "allocation of available devices in descending order of intersection size after allocating all reusable devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-5", "gpu-9", "gpu-10"},
					DeviceRequest:   8,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-2":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-3":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-4":  {Dimensions: dimLevel(0, "g1234")},
						"gpu-5":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-6":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-7":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-8":  {Dimensions: dimLevel(0, "g5678")},
						"gpu-9":  {Dimensions: dimLevel(0, "g9-12")},
						"gpu-10": {Dimensions: dimLevel(0, "g9-12")},
						"gpu-11": {Dimensions: dimLevel(0, "g9-12")},
						"gpu-12": {Dimensions: dimLevel(0, "g9-12")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11"},
			// gpu-6 and gpu-12 is allocated
			// Allocate gpu-1, gpu-5, gpu-9 and gpu-10 first because they are reusable devices
			// Allocate gpu-2, gpu-3 and gpu-4 next because they have the most intersection with an affinity group
			// Between (gpu-7, gpu-8) and (gpu-10, gpu-11), allocate gpu-10 and gpu-11 because the affinity group has fewer unallocated devices (bin-packing).
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-9", "gpu-10", "gpu-11"},
				Success:          true,
			},
		},
		{
			name: "allocate available devices that have affinity with reusable devices from highest to lowest priority",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-5"},
					DeviceRequest:   6,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8], [gpu-9, gpu-10], [gpu-11, gpu-12]
				// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8], [gpu-9, gpu-10, gpu-11, gpu-12]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1":  {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2":  {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3":  {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4":  {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-5":  {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-6":  {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-7":  {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-8":  {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-9":  {Dimensions: dims(dimLevel(0, "g9-10"), dimLevel(1, "g9-12"))},
						"gpu-10": {Dimensions: dims(dimLevel(0, "g9-10"), dimLevel(1, "g9-12"))},
						"gpu-11": {Dimensions: dims(dimLevel(0, "g11-12"), dimLevel(1, "g9-12"))},
						"gpu-12": {Dimensions: dims(dimLevel(0, "g11-12"), dimLevel(1, "g9-12"))},
					},
				},
				MachineState: map[v1.ResourceName]state.AllocationMap{
					"gpu": {
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
						"gpu-4": {},
						"gpu-5": {},
						"gpu-6": {},
						"gpu-7": {},
						"gpu-8": {},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12"},
			// Allocate gpu-1 and gpu-5 first because they are reusable devices
			// Allocate gpu-2 next because they have affinity with gpu-1 at the highest affinity priority (level 0)
			// Allocate gpu-6 and gpu-8 next because they have affinity with gpu-5 at the next highest affinity priority (level 1)
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
				Success:          true,
			},
		},
		{
			name: "allocation of odd number of devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-3", "gpu-5"},
					DeviceRequest:   5,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-7", "gpu-8"},
			// Allocate gpu-1, gpu-3 and gpu-5 first because they are reusable devices
			// Allocate gpu-2 and gpu-4 next because they have affinity with gpu-1 and gpu-3 at the highest affinity priority (level 0)
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5"},
				Success:          true,
			},
		},
		{
			name: "allocation of available devices for 1st level of affinity priority if there are no reusable devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{},
					DeviceRequest:   2,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-3", "gpu-4"},
			// No reusable devices to allocate.
			// Allocate gpu-1 and gpu-2 because they have the best affinity to each other.
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-3", "gpu-4"},
				Success:          true,
			},
		},
		{
			name: "when first priority level is not able to determine an allocation, go to the next priority level to allocate",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			// All of the above devices are reusable devices
			// At first priority level, they all give an intersection size of 2, but there are 6 of them, and we only need 4 of them
			// Since there is another priority level, we go to that priority level to get the best device affinity
			// (gpu-1 and gpu-2), (gpu-5 and gpu-6), (gpu-7, gpu-8) are affinity groups at priority level 0
			// But (gpu-5, gpu-6, gpu-7, gpu-8) are affinity groups at priority level 1, so we allocate gpu-5, gpu-6, gpu-7, gpu-8
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-5", "gpu-6", "gpu-7", "gpu-8"},
				Success:          true,
			},
		},
		{
			name: "able to get the same allocation for in 2 priority levels with different affinityPriority dimensions",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"numa", "socket"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: map[string]string{
								"numa":   "numa-0",
								"socket": "socket-0",
							},
						},
						"gpu-2": {
							Dimensions: map[string]string{
								"numa":   "numa-0",
								"socket": "socket-0",
							},
						},
						"gpu-3": {
							Dimensions: map[string]string{
								"numa":   "numa-1",
								"socket": "socket-0",
							},
						},
						"gpu-4": {
							Dimensions: map[string]string{
								"numa":   "numa-1",
								"socket": "socket-0",
							},
						},
						"gpu-5": {
							Dimensions: map[string]string{
								"numa":   "numa-2",
								"socket": "socket-1",
							},
						},
						"gpu-6": {
							Dimensions: map[string]string{
								"numa":   "numa-2",
								"socket": "socket-1",
							},
						},
						"gpu-7": {
							Dimensions: map[string]string{
								"numa":   "numa-3",
								"socket": "socket-1",
							},
						},
						"gpu-8": {
							Dimensions: map[string]string{
								"numa":   "numa-3",
								"socket": "socket-1",
							},
						},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-5", "gpu-6", "gpu-7", "gpu-8"},
				Success:          true,
			},
		},
		{
			name: "reusable devices are bin-packed, so we allocate the remaining available devices without considering affinity with the reusable devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-4"},
					DeviceRequest:   4,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-4", "gpu-5", "gpu-7", "gpu-8"},
			// gpu-1 and gpu-4 are allocated because they are reusable devices.
			// gpu-2 and gpu-3 are already allocated so we cannot find any affinity to the reusable devices.
			// allocate gpu-7 and gpu-8 because they have affinity to one another.
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-1", "gpu-4", "gpu-7", "gpu-8"},
				Success:          true,
			},
		},
		{
			name: "insufficient devices to allocate, causing an error",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2"},
					DeviceRequest:   4,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2"},
			// gpu-3 and gpu-4 are already allocated
			// gpu-1 and gpu-2 are not enough to satisfy the request
			expectedResult: &allocate.AllocationResult{
				Success: false,
			},
			expectedErr: true,
		},
		{
			name: "if there is another priority level, allocate only the max intersection and then go to the next priority level and allocate",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-5", "gpu-6", "gpu-7"},
					DeviceRequest:   3,
				},
				// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
				// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
				DeviceTopology: &machine.DeviceTopology{
					// Two affinity levels: pairs at level 0, quads at level 1.
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
					},
				},
				MachineState: map[v1.ResourceName]state.AllocationMap{
					"gpu": {
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
						"gpu-4": {},
						"gpu-5": {},
						"gpu-6": {},
						"gpu-7": {},
						"gpu-8": {},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-5", "gpu-6", "gpu-7"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-5", "gpu-6", "gpu-7"},
				Success:          true,
			},
		},
		{
			name: "4 devices in affinity priority 0, 8 devices in affinity priority 1, allocate 8 devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"0": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-2": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-3": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-4": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-5": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-6": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-7": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-8": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-9": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-10": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-11": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-12": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-13": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-14": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-15": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-16": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
					},
				},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
			isRandom:           true,
			expectedResultSize: 2,
		},
		{
			name: "allocate first level of affinity priority devices first, then second level of affinity priority, both have affinity to reusable devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-0"},
					DeviceRequest:   3,
				},
				// Level 0: [gpu-0, gpu-1], [gpu-4, gpu-5], [gpu-2, gpu-3], [gpu-6, gpu-7]
				// Level 1: [gpu-0, gpu-1, gpu-4, gpu-5], [gpu-2, gpu-3, gpu-6, gpu-7]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {
							Dimensions: dims(dimLevel(0, "g01"), dimLevel(1, "g0145")),
						},
						"gpu-1": {
							Dimensions: dims(dimLevel(0, "g01"), dimLevel(1, "g0145")),
						},
						"gpu-2": {
							Dimensions: dims(dimLevel(0, "g23"), dimLevel(1, "g2367")),
						},
						"gpu-3": {
							Dimensions: dims(dimLevel(0, "g23"), dimLevel(1, "g2367")),
						},
						"gpu-4": {
							Dimensions: dims(dimLevel(0, "g45"), dimLevel(1, "g0145")),
						},
						"gpu-5": {
							Dimensions: dims(dimLevel(0, "g45"), dimLevel(1, "g0145")),
						},
						"gpu-6": {
							Dimensions: dims(dimLevel(0, "g67"), dimLevel(1, "g2367")),
						},
						"gpu-7": {
							Dimensions: dims(dimLevel(0, "g67"), dimLevel(1, "g2367")),
						},
					},
				},
			},
			sortedDevices: []string{"gpu-0", "gpu-1", "gpu-2", "gpu-4", "gpu-6", "gpu-7"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-0", "gpu-1", "gpu-4"},
				Success:          true,
			},
		},
		{
			name: "affinity to reusable devices is maintained with different affinityPriority dimensions",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-0"},
					DeviceRequest:   3,
				},
				// Level 0: [gpu-0, gpu-1], [gpu-4, gpu-5], [gpu-2, gpu-3], [gpu-6, gpu-7]
				// Level 1: [gpu-0, gpu-1, gpu-4, gpu-5], [gpu-2, gpu-3, gpu-6, gpu-7]
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"numa", "socket"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {
							Dimensions: map[string]string{
								"numa":   "numa-0",
								"socket": "socket-0",
							},
						},
						"gpu-1": {
							Dimensions: map[string]string{
								"numa":   "numa-0",
								"socket": "socket-0",
							},
						},
						"gpu-2": {
							Dimensions: map[string]string{
								"numa":   "numa-2",
								"socket": "socket-1",
							},
						},
						"gpu-3": {
							Dimensions: map[string]string{
								"numa":   "numa-2",
								"socket": "socket-1",
							},
						},
						"gpu-4": {
							Dimensions: map[string]string{
								"numa":   "numa-1",
								"socket": "socket-0",
							},
						},
						"gpu-5": {
							Dimensions: map[string]string{
								"numa":   "numa-1",
								"socket": "socket-0",
							},
						},
						"gpu-6": {
							Dimensions: map[string]string{
								"numa":   "numa-3",
								"socket": "socket-1",
							},
						},
						"gpu-7": {
							Dimensions: map[string]string{
								"numa":   "numa-3",
								"socket": "socket-1",
							},
						},
					},
				},
			},
			sortedDevices: []string{"gpu-0", "gpu-1", "gpu-2", "gpu-4", "gpu-6", "gpu-7"},
			expectedResult: &allocate.AllocationResult{
				AllocatedDevices: []string{"gpu-0", "gpu-1", "gpu-4"},
				Success:          true,
			},
		},
		{
			name: "fallback to canonical strategy when no affinity is available",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
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
				Success:          true,
			},
		},
		{
			name: "fallback to canonical strategy and allocate all reusable devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2"},
					DeviceRequest:   2,
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
				Success:          true,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceBindingStrategy := NewDeviceAffinityStrategy()
			if tt.ctx.GPUQRMPluginConfig == nil {
				tt.ctx.GPUQRMPluginConfig = qrmconfig.NewGPUQRMPluginConfig()
				// Keep tests focused on affinity logic rather than admission strictness.
				tt.ctx.GPUQRMPluginConfig.RequiredDeviceAffinity = false
			}
			tt.ctx.Emitter = metrics.DummyMetrics{}
			result, err := deviceBindingStrategy.Bind(tt.ctx, tt.sortedDevices)
			if (err != nil) != tt.expectedErr {
				t.Errorf("Bind() error = %v, expectedErr %v", err, tt.expectedErr)
			}
			verifyAllocationResult(t, result, tt.expectedResult, tt.isRandom, tt.expectedResultSize)
		})
	}
}

func TestBind_Dimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                          string
		ctx                           *allocate.AllocationContext
		sortedDevices                 []string
		expectedErr                   bool
		expectedAffinityPriorityLevel int
	}{
		{
			name: "1 level of device affinity, 2 devices in a group",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				DeviceTopology: &machine.DeviceTopology{
					// Single priority dimension; each pair of devices forms an affinity group.
					PriorityDimensions: []string{"0"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
			},
			sortedDevices:                 []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			expectedAffinityPriorityLevel: 0,
		},
		{
			name: "2 devices in affinity priority 0, 4 devices in affinity priority 1",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				DeviceTopology: &machine.DeviceTopology{
					// Two affinity levels: pairs at level 0, quads at level 1.
					// Level 0: [gpu-1, gpu-2], [gpu-3, gpu-4], [gpu-5, gpu-6], [gpu-7, gpu-8]
					// Level 1: [gpu-1, gpu-2, gpu-3, gpu-4], [gpu-5, gpu-6, gpu-7, gpu-8]
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g56"), dimLevel(1, "g5678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g78"), dimLevel(1, "g5678"))},
					},
				},
			},
			sortedDevices:                 []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			expectedAffinityPriorityLevel: 1,
		},
		{
			name: "2 devices in affinity priority 0, 4 devices in affinity priority 2, levels are non-consecutive",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "2"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							Dimensions: dims(dimLevel(0, "g12"), dimLevel(2, "g1234")),
						},
						"gpu-2": {
							Dimensions: dims(dimLevel(0, "g12"), dimLevel(2, "g1234")),
						},
						"gpu-3": {
							Dimensions: dims(dimLevel(0, "g34"), dimLevel(2, "g1234")),
						},
						"gpu-4": {
							Dimensions: dims(dimLevel(0, "g34"), dimLevel(2, "g1234")),
						},
						"gpu-5": {
							Dimensions: dims(dimLevel(0, "g56"), dimLevel(2, "g5678")),
						},
						"gpu-6": {
							Dimensions: dims(dimLevel(0, "g56"), dimLevel(2, "g5678")),
						},
						"gpu-7": {
							Dimensions: dims(dimLevel(0, "g78"), dimLevel(2, "g5678")),
						},
						"gpu-8": {
							Dimensions: dims(dimLevel(0, "g78"), dimLevel(2, "g5678")),
						},
					},
				},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			// Priority levels are positional indices in DeviceTopology.PriorityDimensions.
			// Here, dimension name "2" is the second (index=1) priority dimension.
			expectedAffinityPriorityLevel: 1,
		},
		{
			name: "4 devices in affinity priority 0, 8 devices in affinity priority 1, allocate 4 devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
					DeviceRequest:   4,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"0": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-2": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-3": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-4": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-5": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-6": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-7": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-8": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-9": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-10": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-11": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-12": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-13": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-14": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-15": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-16": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
					},
				},
			},
			sortedDevices:                 []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
			expectedAffinityPriorityLevel: 0,
		},
		{
			name: "4 devices in affinity priority 0, 8 devices in affinity priority 1, allocate 8 devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
					DeviceRequest:   8,
				},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						"0": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-2": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-3": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-4": {
							Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678")),
						},
						"gpu-5": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-6": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-7": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-8": {
							Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678")),
						},
						"gpu-9": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-10": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-11": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-12": {
							Dimensions: dims(dimLevel(0, "g9-12"), dimLevel(1, "g9-16")),
						},
						"gpu-13": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-14": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-15": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
						"gpu-16": {
							Dimensions: dims(dimLevel(0, "g13-16"), dimLevel(1, "g9-16")),
						},
					},
				},
			},
			sortedDevices:                 []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8", "gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
			expectedAffinityPriorityLevel: 1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceBindingStrategy := NewDeviceAffinityStrategy()
			if tt.ctx.GPUQRMPluginConfig == nil {
				tt.ctx.GPUQRMPluginConfig = qrmconfig.NewGPUQRMPluginConfig()
				// Keep tests focused on affinity logic rather than admission strictness.
				tt.ctx.GPUQRMPluginConfig.RequiredDeviceAffinity = false
			}
			tt.ctx.Emitter = metrics.DummyMetrics{}
			result, err := deviceBindingStrategy.Bind(tt.ctx, tt.sortedDevices)
			if (err != nil) != tt.expectedErr {
				t.Errorf("Bind() error = %v, expectedErr %v", err, tt.expectedErr)
			}

			verifyResultIsAffinity(t, result, tt.ctx.DeviceTopology, tt.expectedAffinityPriorityLevel)
		})
	}
}

func TestBind_Dimensions_RequiredStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		ctx                *allocate.AllocationContext
		sortedDevices      []string
		expectedErr        bool
		expectedResultSize int
		expectedPriority   int
	}{
		{
			name: "required affinity: single-level topology with groups of 2, request 4 devices",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					// Single priority dimension; four affinity groups with 2 devices each.
					PriorityDimensions: []string{"0"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			expectedResultSize: 4,
			// With a single priority dimension, the only affinity level index is 0.
			expectedPriority: 0,
		},
		{
			name: "required affinity: single-level topology with groups of 2, request 5 devices using exactly three groups",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   5,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					// Same four size-2 groups as previous tests, but we only expose
					// devices from three groups through sortedDevices below.
					PriorityDimensions: []string{"0"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			// Only expose three of the four groups as candidates; the strict rule
			// ceil(5/2)=3 requires using exactly three groups at the last priority.
			// This ensures we exercise the new validation path with a "just enough"
			// topology.
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6"},
			expectedResultSize: 5,
			expectedPriority:   0,
		},
		{
			name: "required affinity: allocate strictly within a satisfiable affinity group",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						// One affinity group of size 4 at priority 1.
						"gpu-1": {Dimensions: dimLevel(0, "g1234")},
						"gpu-2": {Dimensions: dimLevel(0, "g1234")},
						"gpu-3": {Dimensions: dimLevel(0, "g1234")},
						"gpu-4": {Dimensions: dimLevel(0, "g1234")},
						// Extra devices that should not be needed.
						"gpu-5": {},
						"gpu-6": {},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6"},
			expectedResultSize: 4,
			expectedPriority:   0,
		},
		{
			name: "required affinity: restrict allocation to smallest satisfiable affinity level",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						// Priority 0: affinity group size 2 (gpu-1,gpu-2).
						// Priority 1: affinity group size 4 (gpu-1,gpu-2,gpu-3,gpu-4) which supersets priority 0.
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dimLevel(1, "g1234")},
						"gpu-4": {Dimensions: dimLevel(1, "g1234")},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedResultSize: 2,
			expectedPriority:   0,
		},
		{
			name: "required affinity: fail when no topology affinity is present",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0"},
					Devices: map[string]machine.DeviceInfo{
						// No devices declare any Dimensions.
						"gpu-1": {},
						"gpu-2": {},
						"gpu-3": {},
						"gpu-4": {},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedErr:   true,
		},
		{
			name: "required affinity: fail when available devices are split across different size-2 groups",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   2,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						// Priority 0 has two disjoint affinity groups of size 2: {1,2} and {3,4}.
						// Priority 1 has a size-4 affinity group {1,2,3,4} that supersets both size-2 groups.
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			// Only two devices are available, and they are from different priority-0 groups.
			sortedDevices: []string{"gpu-1", "gpu-3"},
			expectedErr:   true,
		},
		{
			name: "required affinity: fail when request=4 devices are split across different size-4 groups",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						// Priority 0: two disjoint size-4 groups {1,2,3,4} and {5,6,7,8}.
						// Priority 1: one size-8 group {1..8} that supersets both size-4 groups.
						"gpu-1": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			// Four available devices, but split across the two size-4 groups at priority 0.
			sortedDevices: []string{"gpu-1", "gpu-2", "gpu-5", "gpu-6"},
			expectedErr:   true,
		},
		{
			name: "required affinity: fail when ceil(request/maxGroupSize) groups cannot be satisfied",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   5,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					// Four logical size-2 groups, but only two groups worth of
					// devices are actually available in sortedDevices below. The
					// strict rule requires ceil(5/2)=3 groups at the last priority,
					// which is impossible given only two groups with any candidates.
					PriorityDimensions: []string{"0"},
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {Dimensions: dimLevel(0, "g12")},
						"gpu-2": {Dimensions: dimLevel(0, "g12")},
						"gpu-3": {Dimensions: dimLevel(0, "g34")},
						"gpu-4": {Dimensions: dimLevel(0, "g34")},
						"gpu-5": {Dimensions: dimLevel(0, "g56")},
						"gpu-6": {Dimensions: dimLevel(0, "g56")},
						"gpu-7": {Dimensions: dimLevel(0, "g78")},
						"gpu-8": {Dimensions: dimLevel(0, "g78")},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			// Only two full groups worth of devices are available. Regardless of
			// how the allocator behaves internally, strict affinity semantics
			// cannot be satisfied for request=5, so we expect an error.
			sortedDevices: []string{"gpu-1", "gpu-3", "gpu-5", "gpu-6", "gpu-7"},
			expectedErr:   true,
		},
		{
			name: "required affinity: allocate 4 devices from priority-1 size-4 group",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   4,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						// Priority 0: size-2 groups {1,2} and {3,4}.
						// Priority 1: size-4 group {1,2,3,4} (superset of both size-2 groups).
						"gpu-1": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g12"), dimLevel(1, "g1234"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g34"), dimLevel(1, "g1234"))},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
			expectedResultSize: 4,
			expectedPriority:   1,
		},
		{
			name: "required affinity: allocate 6 devices from priority-1 size-8 group",
			ctx: &allocate.AllocationContext{
				ResourceReq: &pluginapi.ResourceRequest{
					PodUid:        "pod-1",
					ContainerName: "container-1",
				},
				DeviceReq: &pluginapi.DeviceRequest{
					DeviceName:      "gpu",
					ReusableDevices: nil,
					DeviceRequest:   6,
				},
				GPUQRMPluginConfig: &qrmconfig.GPUQRMPluginConfig{RequiredDeviceAffinity: true},
				DeviceTopology: &machine.DeviceTopology{
					PriorityDimensions: []string{"0", "1"},
					Devices: map[string]machine.DeviceInfo{
						// Priority 0: two size-4 groups {1,2,3,4} and {5,6,7,8}.
						// Priority 1: one size-8 group {1,2,3,4,5,6,7,8} that supersets both size-4 groups.
						"gpu-1": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-2": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-3": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-4": {Dimensions: dims(dimLevel(0, "g1234"), dimLevel(1, "g12345678"))},
						"gpu-5": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
						"gpu-6": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
						"gpu-7": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
						"gpu-8": {Dimensions: dims(dimLevel(0, "g5678"), dimLevel(1, "g12345678"))},
					},
				},
				Emitter: metrics.DummyMetrics{},
			},
			sortedDevices:      []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
			expectedResultSize: 6,
			expectedPriority:   1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceBindingStrategy := NewDeviceAffinityStrategy()
			result, err := deviceBindingStrategy.Bind(tt.ctx, tt.sortedDevices)
			if (err != nil) != tt.expectedErr {
				t.Fatalf("Bind() error = %v, expectedErr %v", err, tt.expectedErr)
			}
			if tt.expectedErr {
				if result == nil || result.Success {
					t.Fatalf("expected allocation to fail, got result=%v", result)
				}
				return
			}
			if result == nil || !result.Success {
				t.Fatalf("expected allocation to succeed, got result=%v", result)
			}
			if len(result.AllocatedDevices) != tt.expectedResultSize {
				t.Fatalf("allocated size = %d, want %d", len(result.AllocatedDevices), tt.expectedResultSize)
			}
			verifyResultIsStrictAffinity(t, result, tt.ctx.DeviceTopology, tt.expectedPriority)
		})
	}
}

func verifyAllocationResult(
	t *testing.T, result *allocate.AllocationResult, expectedResult *allocate.AllocationResult, isRandom bool,
	expectedResultSize int,
) {
	if isRandom {
		if len(result.AllocatedDevices) != expectedResultSize {
			t.Errorf("result.AllocatedDevices = %v, expectedResultSize = %v", result.AllocatedDevices, expectedResultSize)
		}
		return
	}
	if (result == nil) != (expectedResult == nil) {
		t.Errorf("result = %v, expectedResult = %v", result, expectedResult)
		return
	}
	if result.Success != expectedResult.Success {
		t.Errorf("result.Success = %v, expectedResult.Success = %v", result.Success, expectedResult.Success)
		return
	}
	if len(result.AllocatedDevices) != len(expectedResult.AllocatedDevices) {
		t.Errorf("result.AllocatedDevices = %v, expectedResult.AllocatedDevices = %v", result.AllocatedDevices, expectedResult.AllocatedDevices)
		return
	}
	if diff := cmp.Diff(result.AllocatedDevices, expectedResult.AllocatedDevices,
		cmpopts.SortSlices(func(a, b string) bool { return a < b }),
	); diff != "" {
		t.Errorf("Bind() mismatch (-got +want):\n%s", diff)
	}
}

func verifyResultIsAffinity(
	t *testing.T, result *allocate.AllocationResult, topology *machine.DeviceTopology,
	expectedAffinityPriorityLevel int,
) {
	deviceGroups := topology.GroupDeviceAffinity()
	if len(deviceGroups) == 0 {
		t.Errorf("expected affinity groups but found none")
		return
	}
	if expectedAffinityPriorityLevel >= len(deviceGroups) {
		t.Errorf("expected affinity priority level %d, got only %d affinity levels", expectedAffinityPriorityLevel, len(deviceGroups))
		return
	}
	priorityLevelDevices := deviceGroups[expectedAffinityPriorityLevel]
	if result == nil {
		t.Errorf("result is nil")
		return
	}

	// Allocation is considered affinity-compliant if all allocated devices are contained
	// within any single affinity group at the expected priority level.
	for _, deviceIDs := range priorityLevelDevices {
		groupSet := make(map[string]struct{}, len(deviceIDs))
		for _, id := range deviceIDs {
			groupSet[id] = struct{}{}
		}

		ok := true
		for _, alloc := range result.AllocatedDevices {
			if _, exists := groupSet[alloc]; !exists {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}

	t.Errorf("result = %v, did not find it within an affinity group", result.AllocatedDevices)
}

// verifyResultIsStrictAffinity verifies that an allocation respects required
// device affinity at the given priority level. Instead of requiring all
// devices to belong to a single affinity group, it ensures that every
// allocated device has at least one peer sharing the same dimension value at
// the specified priority level.
func verifyResultIsStrictAffinity(
	t *testing.T, result *allocate.AllocationResult, topology *machine.DeviceTopology,
	expectedAffinityPriorityLevel int,
) {
	if result == nil {
		t.Errorf("result is nil")
		return
	}
	if topology == nil || len(topology.PriorityDimensions) == 0 {
		t.Errorf("topology has no priority dimensions")
		return
	}
	if expectedAffinityPriorityLevel < 0 || expectedAffinityPriorityLevel >= len(topology.PriorityDimensions) {
		t.Errorf("expected affinity priority level %d out of range (have %d levels)", expectedAffinityPriorityLevel, len(topology.PriorityDimensions))
		return
	}

	dimName := topology.PriorityDimensions[expectedAffinityPriorityLevel]
	valueCounts := make(map[string]int)

	for _, id := range result.AllocatedDevices {
		info, ok := topology.Devices[id]
		if !ok {
			t.Errorf("device %q not found in topology", id)
			return
		}
		value, ok := info.Dimensions[dimName]
		if !ok {
			t.Errorf("device %q has no dimension %q", id, dimName)
			return
		}
		valueCounts[value]++
	}

	// Strict affinity is satisfied if there is at least one affinity group
	// (dimension value) that contains more than one allocated device at the
	// specified priority level. This allows odd-sized allocations such as
	// 5 devices from four 2-device groups, as long as some devices share
	// the same dimension value.
	hasPair := false
	for _, count := range valueCounts {
		if count >= 2 {
			hasPair = true
			break
		}
	}
	if !hasPair && len(result.AllocatedDevices) > 1 {
		t.Errorf("expected at least one pair of devices sharing dimension %q, got allocation %v", dimName, result.AllocatedDevices)
	}
}
