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
	"reflect"
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-10", "gpu-11", "gpu-12"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-11", "gpu-12"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-12"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
								1: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-10", "gpu-11", "gpu-12"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-11", "gpu-12"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-12"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-10", "gpu-11", "gpu-12"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-11", "gpu-12"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-12"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
								1: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-10"},
								1: {"gpu-10", "gpu-11", "gpu-12"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9"},
								1: {"gpu-9", "gpu-11", "gpu-12"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-12"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-11"},
								1: {"gpu-9", "gpu-10", "gpu-11"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
								1: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
								1: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
								1: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"0": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3", "gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11", "gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11", "gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11", "gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11", "gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-13": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-14": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-15": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-16": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
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
				DeviceTopology: &machine.DeviceTopology{
					Devices: map[string]machine.DeviceInfo{
						"gpu-0": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-4", "gpu-5"},
							},
						},
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-0"},
								1: {"gpu-0", "gpu-4", "gpu-5"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-3", "gpu-6", "gpu-7"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-6", "gpu-7"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-0", "gpu-1", "gpu-5"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-0", "gpu-1", "gpu-4"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-2", "gpu-3", "gpu-7"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-2", "gpu-3", "gpu-6"},
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceBindingStrategy := NewDeviceAffinityStrategy()
			result, err := deviceBindingStrategy.Bind(tt.ctx, tt.sortedDevices)
			if (err != nil) != tt.expectedErr {
				t.Errorf("Bind() error = %v, expectedErr %v", err, tt.expectedErr)
			}
			verifyAllocationResult(t, result, tt.expectedResult, tt.isRandom, tt.expectedResultSize)
		})
	}
}

func TestBind_DeviceAffinity(t *testing.T) {
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
							},
						},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2"},
								1: {"gpu-2", "gpu-3", "gpu-4"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1"},
								1: {"gpu-1", "gpu-3", "gpu-4"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6"},
								1: {"gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5"},
								1: {"gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-8"},
								1: {"gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-7"},
								1: {"gpu-5", "gpu-6", "gpu-7"},
							},
						},
					},
				},
			},
			sortedDevices:                 []string{"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
								1: {"gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
								1: {"gpu-1", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-10", "gpu-11", "gpu-12"},
								1: {"gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-11", "gpu-12"},
								1: {"gpu-9", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-13": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-14", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-14": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-15", "gpu-16"},
							},
						},
						"gpu-15": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-16"},
							},
						},
						"gpu-16": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-15"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15"},
							},
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
					Devices: map[string]machine.DeviceInfo{
						"gpu-1": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-2", "gpu-3", "gpu-4"},
								1: {"gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-2": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-3", "gpu-4"},
								1: {"gpu-1", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-3": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-4"},
								1: {"gpu-1", "gpu-2", "gpu-4", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-4": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-1", "gpu-2", "gpu-3"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-5", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-5": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-6", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-6", "gpu-7", "gpu-8"},
							},
						},
						"gpu-6": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-7", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-7", "gpu-8"},
							},
						},
						"gpu-7": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-8"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-8"},
							},
						},
						"gpu-8": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-5", "gpu-6", "gpu-7"},
								1: {"gpu-1", "gpu-2", "gpu-3", "gpu-4", "gpu-5", "gpu-6", "gpu-7"},
							},
						},
						"gpu-9": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-10", "gpu-11", "gpu-12"},
								1: {"gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-10": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-11", "gpu-12"},
								1: {"gpu-9", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-11": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-12"},
								1: {"gpu-9", "gpu-10", "gpu-12", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-12": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-9", "gpu-10", "gpu-11"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-13", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-13": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-14", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-14", "gpu-15", "gpu-16"},
							},
						},
						"gpu-14": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-15", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-15", "gpu-16"},
							},
						},
						"gpu-15": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-16"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-16"},
							},
						},
						"gpu-16": {
							DeviceAffinity: map[machine.AffinityPriority]machine.DeviceIDs{
								0: {"gpu-13", "gpu-14", "gpu-15"},
								1: {"gpu-9", "gpu-10", "gpu-11", "gpu-12", "gpu-13", "gpu-14", "gpu-15"},
							},
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
			result, err := deviceBindingStrategy.Bind(tt.ctx, tt.sortedDevices)
			if (err != nil) != tt.expectedErr {
				t.Errorf("Bind() error = %v, expectedErr %v", err, tt.expectedErr)
			}

			verifyResultIsAffinity(t, result, tt.ctx.DeviceTopology, tt.expectedAffinityPriorityLevel)
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
	affinityMap := topology.GroupDeviceAffinity()
	priorityLevelDevices := affinityMap[machine.AffinityPriority(expectedAffinityPriorityLevel)]

	sort.Slice(result.AllocatedDevices, func(i, j int) bool {
		return result.AllocatedDevices[i] < result.AllocatedDevices[j]
	})

	for _, deviceIDs := range priorityLevelDevices {
		sort.Slice(deviceIDs, func(i, j int) bool { return deviceIDs[i] < deviceIDs[j] })
		if reflect.DeepEqual(deviceIDs, machine.DeviceIDs(result.AllocatedDevices)) {
			return
		}
	}

	t.Errorf("result = %v, did not find it within an affinity group", result.AllocatedDevices)
}
