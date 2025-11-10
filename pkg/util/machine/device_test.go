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

package machine

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeviceTopologyRegistry_GetDeviceNUMAAffinity(t *testing.T) {
	t.Parallel()

	npuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"npu-0": {NumaNodes: []int{0}},
			"npu-1": {NumaNodes: []int{1}},
			"npu-2": {NumaNodes: []int{0, 1}},
		},
	}

	gpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
			"gpu-1": {NumaNodes: []int{1}},
			"gpu-2": {NumaNodes: []int{2}},
		},
	}

	xpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"xpu-0": {NumaNodes: []int{0}},
			"xpu-1": {NumaNodes: []int{1}},
			"xpu-2": {NumaNodes: nil},
		},
	}

	dpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"dpu-0": {NumaNodes: []int{1}},
			"dpu-1": {NumaNodes: []int{0}},
			"dpu-2": {NumaNodes: []int{}},
		},
	}

	// Register device topology providers
	registry := NewDeviceTopologyRegistry()
	registry.RegisterDeviceTopologyProvider("npu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("xpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("dpu", NewDeviceTopologyProviderStub())
	err := registry.SetDeviceTopology("npu", npuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("gpu", gpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("xpu", xpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("dpu", dpuTopology)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		deviceA     string
		deviceB     string
		expected    map[string][]string
		expectedErr bool
	}{
		{
			name:    "npu to gpu affinity",
			deviceA: "npu",
			deviceB: "gpu",
			expected: map[string][]string{
				"npu-0": {"gpu-0"},
				"npu-1": {"gpu-1"},
				"npu-2": {},
			},
		},
		{
			name:        "non-existent device A",
			deviceA:     "invalid device",
			deviceB:     "gpu",
			expectedErr: true,
		},
		{
			name:        "non-existent device B",
			deviceA:     "npu",
			deviceB:     "invalid device",
			expectedErr: true,
		},
		{
			name:    "devices with empty numa nodes are not considered to have affinity with each other",
			deviceA: "xpu",
			deviceB: "dpu",
			expected: map[string][]string{
				"xpu-0": {"dpu-1"},
				"xpu-1": {"dpu-0"},
				"xpu-2": {},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := registry.GetDeviceNUMAAffinity(tt.deviceA, tt.deviceB)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				evaluateDeviceNUMAAffinity(t, actual, tt.expected)
			}
		})
	}
}

func TestDeviceTopology_GroupDeviceAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		deviceTopology         *DeviceTopology
		expectedDeviceAffinity map[AffinityPriority][]DeviceIDs
	}{
		{
			name: "test simple affinity of 2 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-1"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[AffinityPriority][]DeviceIDs{
				0: {DeviceIDs([]string{"npu-0", "npu-1"}), DeviceIDs([]string{"npu-2", "npu-3"})},
			},
		},
		{
			name: "test simple affinity of 4 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-2", "npu-3"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-1", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-1", "npu-2"},
						},
					},
					"npu-4": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-5", "npu-6", "npu-7"},
						},
					},
					"npu-5": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-4", "npu-6", "npu-7"},
						},
					},
					"npu-6": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-4", "npu-5", "npu-7"},
						},
					},
					"npu-7": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-4", "npu-5", "npu-6"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[AffinityPriority][]DeviceIDs{
				0: {DeviceIDs([]string{"npu-0", "npu-1", "npu-2", "npu-3"}), DeviceIDs([]string{"npu-4", "npu-5", "npu-6", "npu-7"})},
			},
		},
		{
			name: "device topology includes self for one affinity level",
			deviceTopology: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-1"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-1"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2", "npu-3"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[AffinityPriority][]DeviceIDs{
				0: {DeviceIDs([]string{"npu-0", "npu-1"}), DeviceIDs([]string{"npu-2", "npu-3"})},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with 2 affinity priority level",
			deviceTopology: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-1"},
							1: {"npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0"},
							1: {"npu-0", "npu-2", "npu-3"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-3"},
							1: {"npu-0", "npu-1", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2"},
							1: {"npu-0", "npu-1", "npu-2"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[AffinityPriority][]DeviceIDs{
				0: {DeviceIDs([]string{"npu-0", "npu-1"}), DeviceIDs([]string{"npu-2", "npu-3"})},
				1: {DeviceIDs([]string{"npu-0", "npu-1", "npu-2", "npu-3"})},
			},
		},
		{
			name: "device topology includes self for 2 affinity levels",
			deviceTopology: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-1"},
							1: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-1"},
							1: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2", "npu-3"},
							1: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2", "npu-3"},
							1: {"npu-0", "npu-1", "npu-2", "npu-3"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[AffinityPriority][]DeviceIDs{
				0: {DeviceIDs([]string{"npu-0", "npu-1"}), DeviceIDs([]string{"npu-2", "npu-3"})},
				1: {DeviceIDs([]string{"npu-0", "npu-1", "npu-2", "npu-3"})},
			},
		},
		{
			name: "unsorted device topology has no effect on result",
			deviceTopology: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"npu-0": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-2", "npu-1", "npu-3"},
						},
					},
					"npu-1": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-3", "npu-0", "npu-2"},
						},
					},
					"npu-2": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-1", "npu-0", "npu-3"},
						},
					},
					"npu-3": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-0", "npu-2", "npu-1"},
						},
					},
					"npu-4": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-6", "npu-5", "npu-7"},
						},
					},
					"npu-5": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-7", "npu-4", "npu-6"},
						},
					},
					"npu-6": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-5", "npu-4", "npu-7"},
						},
					},
					"npu-7": {
						DeviceAffinity: map[AffinityPriority]DeviceIDs{
							0: {"npu-6", "npu-4", "npu-5"},
						},
					},
				},
			},
			expectedDeviceAffinity: map[AffinityPriority][]DeviceIDs{
				0: {DeviceIDs([]string{"npu-0", "npu-1", "npu-2", "npu-3"}), DeviceIDs([]string{"npu-4", "npu-5", "npu-6", "npu-7"})},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceAffinity := tt.deviceTopology.GroupDeviceAffinity()
			evaluateDeviceAffinity(t, deviceAffinity, tt.expectedDeviceAffinity)
		})
	}
}

func evaluateDeviceNUMAAffinity(t *testing.T, expectedDeviceNUMAAffinity, actualDeviceNUMAAffinity map[string][]string) {
	if len(actualDeviceNUMAAffinity) != len(expectedDeviceNUMAAffinity) {
		t.Errorf("deviceNUMAAffinity lengths don't match, expected %d, got %d", len(expectedDeviceNUMAAffinity), len(actualDeviceNUMAAffinity))
		return
	}

	for device, expected := range expectedDeviceNUMAAffinity {
		actual, ok := actualDeviceNUMAAffinity[device]
		if !ok {
			t.Errorf("expected device numa affinity for device %v, but it is not found", device)
			return
		}

		assert.ElementsMatch(t, expected, actual, "device numa affinity are not equal")
	}
}

func evaluateDeviceAffinity(t *testing.T, expectedDeviceAffinity, actualDeviceAffinity map[AffinityPriority][]DeviceIDs) {
	if len(actualDeviceAffinity) != len(expectedDeviceAffinity) {
		t.Errorf("expected %d affinities, got %d", len(expectedDeviceAffinity), len(actualDeviceAffinity))
		return
	}

	for priority, expected := range expectedDeviceAffinity {
		actual, ok := actualDeviceAffinity[priority]
		if !ok {
			t.Errorf("expected affinities for priority %v, but it is not found", priority)
			return
		}

		if !equalDeviceIDsGroupsIgnoreOrder(t, expected, actual) {
			return
		}
	}
}

func equalDeviceIDsGroupsIgnoreOrder(t *testing.T, expected, actual []DeviceIDs) bool {
	if len(expected) != len(actual) {
		t.Errorf("expected %d devices, got %d", len(expected), len(actual))
		return false
	}

	// Convert each DeviceIDs slice into a normalized, comparable form
	normalize := func(groups []DeviceIDs) []string {
		res := make([]string, len(groups))
		for i, group := range groups {
			sorted := append([]string{}, group...)
			sort.Strings(sorted)
			res[i] = strings.Join(sorted, ",")
		}
		sort.Strings(res)
		return res
	}

	normalizedExp := normalize(expected)
	normalizedAct := normalize(actual)

	for i := range normalizedExp {
		if normalizedExp[i] != normalizedAct[i] {
			t.Errorf("expected %s, got %s", normalizedAct[i], normalizedExp[i])
			return false
		}
	}

	return true
}
