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
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestDeviceTopologyClone(t *testing.T) {
	t.Parallel()

	t.Run("nil topology", func(t *testing.T) {
		t.Parallel()
		var topology *DeviceTopology
		assert.Nil(t, topology.Clone())
	})

	t.Run("deep copy", func(t *testing.T) {
		t.Parallel()
		topology := &DeviceTopology{
			Devices: map[string]DeviceInfo{
				"gpu-0": {
					Health:     "Healthy",
					NumaNodes:  []int{0, 1},
					Dimensions: DeviceDimensions{"socket": "0"},
				},
			},
			PriorityDimensions: []string{"socket"},
			UpdateTime:         100,
		}

		cloned := topology.Clone()
		cloned.Devices["gpu-1"] = DeviceInfo{}
		clonedInfo := cloned.Devices["gpu-0"]
		clonedInfo.NumaNodes[0] = 2
		clonedInfo.Dimensions["socket"] = "1"
		cloned.Devices["gpu-0"] = clonedInfo
		cloned.PriorityDimensions[0] = "numa"

		assert.NotContains(t, topology.Devices, "gpu-1")
		assert.Equal(t, []int{0, 1}, topology.Devices["gpu-0"].NumaNodes)
		assert.Equal(t, "0", topology.Devices["gpu-0"].Dimensions["socket"])
		assert.Equal(t, []string{"socket"}, topology.PriorityDimensions)
		assert.Equal(t, int64(100), cloned.UpdateTime)
	})

	t.Run("preserves nil nested fields", func(t *testing.T) {
		t.Parallel()
		topology := &DeviceTopology{
			Devices: map[string]DeviceInfo{"gpu-0": {}},
		}

		cloned := topology.Clone()

		assert.Nil(t, cloned.Devices["gpu-0"].NumaNodes)
		assert.Nil(t, cloned.Devices["gpu-0"].Dimensions)
		assert.Nil(t, cloned.PriorityDimensions)
	})

	t.Run("preserves non-nil empty slices", func(t *testing.T) {
		t.Parallel()
		topology := &DeviceTopology{
			Devices: map[string]DeviceInfo{
				"gpu-0": {NumaNodes: []int{}},
			},
			PriorityDimensions: []string{},
		}

		cloned := topology.Clone()

		assert.NotNil(t, cloned.Devices["gpu-0"].NumaNodes)
		assert.Empty(t, cloned.Devices["gpu-0"].NumaNodes)
		assert.NotNil(t, cloned.PriorityDimensions)
		assert.Empty(t, cloned.PriorityDimensions)
	})
}

func TestDeviceTopologyRegistry_TopologyChangeNotifiers(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())

	callCount := 0
	registry.RegisterTopologyChangeNotifier(func() {
		callCount++
	})

	gpuTopology1 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
		},
	}

	// First set should trigger the notifier
	err := registry.SetDeviceTopology("gpu", gpuTopology1)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Setting identical topology should not trigger the notifier
	gpuTopology1Clone := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
		},
	}
	err = registry.SetDeviceTopology("gpu", gpuTopology1Clone)
	assert.NoError(t, err)
	assert.Equal(t, 1, callCount)

	// Setting different topology should trigger the notifier
	gpuTopology2 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
			"gpu-1": {NumaNodes: []int{1}},
		},
	}
	err = registry.SetDeviceTopology("gpu", gpuTopology2)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestDeviceTopologyRegistry_SetDeviceTopologyUsesCallerClone(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProvider())

	input := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}, Dimensions: DeviceDimensions{"socket": "0"}},
		},
	}
	topology := input.Clone()
	assert.NoError(t, registry.SetDeviceTopology("gpu", topology))

	input.Devices["gpu-1"] = DeviceInfo{}
	info := input.Devices["gpu-0"]
	info.NumaNodes[0] = 1
	info.Dimensions["socket"] = "1"
	input.Devices["gpu-0"] = info

	published, err := registry.GetDeviceTopology("gpu")
	assert.NoError(t, err)
	assert.Same(t, topology, published)
	assert.NotContains(t, published.Devices, "gpu-1")
	assert.Equal(t, []int{0}, published.Devices["gpu-0"].NumaNodes)
	assert.Equal(t, "0", published.Devices["gpu-0"].Dimensions["socket"])
}

type retainingDeviceTopologyProvider struct {
	topology *DeviceTopology
	setCalls int
	setErr   error
}

func (p *retainingDeviceTopologyProvider) SetDeviceTopology(topology *DeviceTopology) error {
	p.setCalls++
	p.topology = topology
	return p.setErr
}

func (p *retainingDeviceTopologyProvider) GetDeviceTopology() (*DeviceTopology, error) {
	return p.topology, nil
}

func TestDeviceTopologyRegistry_SetDeviceTopologyIsolatesCachedTopologyFromProvider(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	provider := &retainingDeviceTopologyProvider{}
	registry.RegisterDeviceTopologyProvider("gpu", provider)

	topology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {
				NumaNodes:  []int{0},
				Dimensions: DeviceDimensions{"socket": "0"},
			},
		},
		PriorityDimensions: []string{"socket"},
		UpdateTime:         100,
	}
	expected := topology.Clone()
	assert.NoError(t, registry.SetDeviceTopology("gpu", topology))

	provider.topology.Devices["gpu-1"] = DeviceInfo{}
	info := provider.topology.Devices["gpu-0"]
	info.NumaNodes[0] = 1
	info.Dimensions["socket"] = "1"
	provider.topology.Devices["gpu-0"] = info
	provider.topology.PriorityDimensions[0] = "numa"
	provider.topology.UpdateTime = 200

	cached, err := registry.getLastTopology("gpu")
	assert.NoError(t, err)
	assert.NotSame(t, provider.topology, cached)
	assert.Equal(t, expected, cached)
}

type mutatingDeviceAffinityProvider struct {
	dimensionValue string
}

func (p *mutatingDeviceAffinityProvider) SetDeviceAffinity(topology *DeviceTopology) {
	info := topology.Devices["gpu-0"]
	info.Dimensions = DeviceDimensions{"socket": p.dimensionValue}
	topology.Devices["gpu-0"] = info
}

func (*mutatingDeviceAffinityProvider) WatchTopologyChanged(stopCh <-chan struct{}) <-chan struct{} {
	return nil
}

func TestDeviceTopologyRegistry_UpdateTopology(t *testing.T) {
	t.Run("clones cached topology before updating affinity", func(t *testing.T) {
		registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
		registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProvider())
		affinityProvider := &mutatingDeviceAffinityProvider{dimensionValue: "0"}
		registry.RegisterTopologyAffinityProvider("gpu", affinityProvider)

		assert.NoError(t, registry.SetDeviceTopology("gpu", &DeviceTopology{
			Devices: map[string]DeviceInfo{"gpu-0": {}},
		}))
		previous, err := registry.getLastTopology("gpu")
		assert.NoError(t, err)

		affinityProvider.dimensionValue = "1"
		registry.updateTopology("gpu")

		current, err := registry.getLastTopology("gpu")
		assert.NoError(t, err)
		assert.NotSame(t, previous, current)
		assert.Equal(t, "0", previous.Devices["gpu-0"].Dimensions["socket"])
		assert.Equal(t, "1", current.Devices["gpu-0"].Dimensions["socket"])
	})

	t.Run("does nothing without cached topology", func(t *testing.T) {
		registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
		provider := &retainingDeviceTopologyProvider{}
		registry.RegisterDeviceTopologyProvider("gpu", provider)

		registry.updateTopology("gpu")

		assert.Zero(t, provider.setCalls)
		assert.Nil(t, provider.topology)
	})

	t.Run("preserves cached topology when provider update fails", func(t *testing.T) {
		registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
		provider := &retainingDeviceTopologyProvider{}
		registry.RegisterDeviceTopologyProvider("gpu", provider)
		affinityProvider := &mutatingDeviceAffinityProvider{dimensionValue: "0"}
		registry.RegisterTopologyAffinityProvider("gpu", affinityProvider)

		assert.NoError(t, registry.SetDeviceTopology("gpu", &DeviceTopology{
			Devices: map[string]DeviceInfo{"gpu-0": {}},
		}))
		cached, err := registry.getLastTopology("gpu")
		assert.NoError(t, err)

		provider.setErr = errors.New("set topology failed")
		affinityProvider.dimensionValue = "1"
		registry.updateTopology("gpu")

		current, err := registry.getLastTopology("gpu")
		assert.NoError(t, err)
		assert.Same(t, cached, current)
		assert.NotSame(t, cached, provider.topology)
		assert.Equal(t, "0", current.Devices["gpu-0"].Dimensions["socket"])
		assert.Equal(t, "1", provider.topology.Devices["gpu-0"].Dimensions["socket"])
	})
}

func TestDeviceTopologyRegistry_UpdateTopologyDoesNotMutatePublishedSnapshot(t *testing.T) {
	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProvider())
	affinityProvider := &mutatingDeviceAffinityProvider{dimensionValue: "0"}
	registry.RegisterTopologyAffinityProvider("gpu", affinityProvider)

	assert.NoError(t, registry.SetDeviceTopology("gpu", &DeviceTopology{
		Devices: map[string]DeviceInfo{"gpu-0": {}},
	}))
	previous, err := registry.GetDeviceTopology("gpu")
	assert.NoError(t, err)

	affinityProvider.dimensionValue = "1"
	registry.updateTopology("gpu")

	current, err := registry.GetDeviceTopology("gpu")
	assert.NoError(t, err)
	assert.NotSame(t, previous, current)
	assert.Equal(t, "0", previous.Devices["gpu-0"].Dimensions["socket"])
	assert.Equal(t, "1", current.Devices["gpu-0"].Dimensions["socket"])
}

func TestDeviceTopologyRegistry_GetAffinityDevices(t *testing.T) {
	t.Parallel()

	npuTopology := &DeviceTopology{
		PriorityDimensions: []string{"socket", "numa"},
		Devices: map[string]DeviceInfo{
			"npu-0": {
				Dimensions: map[string]string{
					"socket": "0",
				},
			},
			"npu-1": {
				Dimensions: map[string]string{
					"socket": "1",
					"numa":   "0",
				},
			},
			"npu-2": {
				Dimensions: map[string]string{
					"socket": "0",
				},
			},
		},
	}

	gpuTopology := &DeviceTopology{
		PriorityDimensions: []string{"socket", "numa"},
		Devices: map[string]DeviceInfo{
			"gpu-0": {
				Dimensions: map[string]string{
					"socket": "0",
					"numa":   "0",
				},
			},
			"gpu-1": {
				Dimensions: map[string]string{
					"socket": "1",
					"numa":   "0",
				},
			},
			"gpu-2": {
				Dimensions: map[string]string{
					"socket": "2",
					"numa":   "1",
				},
			},
		},
	}

	xpuTopology := &DeviceTopology{
		PriorityDimensions: []string{"socket"},
		Devices: map[string]DeviceInfo{
			"xpu-0": {
				Dimensions: map[string]string{
					"socket": "0",
				},
			},
			"xpu-1": {
				Dimensions: map[string]string{
					"socket": "1",
				},
			},
			"xpu-2": {},
		},
	}

	dpuTopology := &DeviceTopology{
		PriorityDimensions: []string{"socket"},
		Devices: map[string]DeviceInfo{
			"dpu-0": {
				Dimensions: map[string]string{
					"socket": "1",
				},
			},
			"dpu-1": {
				Dimensions: map[string]string{
					"socket": "0",
				},
			},
			"dpu-2": {},
		},
	}

	// Topologies with disjoint affinity dimensions to ensure no cross-device affinity
	apuTopology := &DeviceTopology{
		PriorityDimensions: []string{"pcie"},
		Devices: map[string]DeviceInfo{
			"apu-0": {
				Dimensions: map[string]string{
					"pcie": "0",
				},
			},
			"apu-1": {
				Dimensions: map[string]string{
					"pcie": "1",
				},
			},
		},
	}

	bpuTopology := &DeviceTopology{
		PriorityDimensions: []string{"fabric"},
		Devices: map[string]DeviceInfo{
			"bpu-0": {
				Dimensions: map[string]string{
					"fabric": "0",
				},
			},
			"bpu-1": {
				Dimensions: map[string]string{
					"fabric": "1",
				},
			},
		},
	}

	// Topologies with only NUMA nodes (no dimensions) for fallback test
	numaTopoA := &DeviceTopology{
		PriorityDimensions: []string{},
		Devices: map[string]DeviceInfo{
			"devA-0": {NumaNodes: []int{0, 1}},
			"devA-1": {NumaNodes: []int{2}},
			"devA-2": {NumaNodes: []int{1, 3}},
		},
	}
	numaTopoB := &DeviceTopology{
		PriorityDimensions: []string{},
		Devices: map[string]DeviceInfo{
			"devB-0": {NumaNodes: []int{0}},
			"devB-1": {NumaNodes: []int{1, 2}},
			"devB-2": {NumaNodes: []int{3}},
			"devB-3": {NumaNodes: []int{4}},
		},
	}
	// Topology where neither dimensions nor numa nodes match
	emptyMatchTopoA := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"matchA-0": {NumaNodes: []int{99}},
		},
	}
	emptyMatchTopoB := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"matchB-0": {NumaNodes: []int{100}},
		},
	}

	// Register device topology providers
	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("npu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("xpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("dpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("apu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("bpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("numaA", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("numaB", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("emptyMatchA", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("emptyMatchB", NewDeviceTopologyProviderStub())
	err := registry.SetDeviceTopology("npu", npuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("gpu", gpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("xpu", xpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("dpu", dpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("apu", apuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("bpu", bpuTopology)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("numaA", numaTopoA)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("numaB", numaTopoB)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("emptyMatchA", emptyMatchTopoA)
	assert.NoError(t, err)
	err = registry.SetDeviceTopology("emptyMatchB", emptyMatchTopoB)
	assert.NoError(t, err)

	tests := []struct {
		name        string
		deviceA     string
		deviceB     string
		expected    map[string]map[string][]string
		expectedErr bool
	}{
		{
			name:    "npu to gpu affinity",
			deviceA: "npu",
			deviceB: "gpu",
			expected: map[string]map[string][]string{
				"npu-0": {"socket": {"gpu-0"}},
				"npu-1": {"socket": {"gpu-1"}, "numa": {"gpu-0", "gpu-1"}},
				"npu-2": {"socket": {"gpu-0"}},
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
			name:    "devices with empty affinity are not considered to have affinity with each other",
			deviceA: "xpu",
			deviceB: "dpu",
			expected: map[string]map[string][]string{
				"xpu-0": {"socket": {"dpu-1"}},
				"xpu-1": {"socket": {"dpu-0"}},
			},
		},
		{
			name:     "no matching affinity returns empty map",
			deviceA:  "apu",
			deviceB:  "bpu",
			expected: map[string]map[string][]string{},
		},
		{
			name:    "numa fallback when no dimensions match",
			deviceA: "numaA",
			deviceB: "numaB",
			expected: map[string]map[string][]string{
				"devA-0": {"numa": {"devB-0", "devB-1"}},
				"devA-1": {"numa": {"devB-1"}},
				"devA-2": {"numa": {"devB-1", "devB-2"}},
			},
		},
		{
			name:     "no matching numa nodes returns empty",
			deviceA:  "emptyMatchA",
			deviceB:  "emptyMatchB",
			expected: map[string]map[string][]string{},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := registry.GetAffinityDevices(tt.deviceA, tt.deviceB)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				evaluateDeviceAffinityMap(t, tt.expected, actual)
			}
		})
	}
}

func evaluateDeviceAffinityMap(t *testing.T, expected map[string]map[string][]string, actual map[string]map[string]DeviceIDs) {
	if len(actual) != len(expected) {
		t.Errorf("deviceAffinity lengths don't match, expected %d, got %d", len(expected), len(actual))
		return
	}

	for device, expectedAffinity := range expected {
		affinityByDim, ok := actual[device]
		if !ok {
			t.Errorf("expected device affinity for device %v, but it is not found", device)
			return
		}

		for dimName, expectedDevices := range expectedAffinity {
			actualDevices, ok := affinityByDim[dimName]
			if !ok {
				t.Errorf("expected affinity for dimension %s for device %s, but it is not found", dimName, device)
				return
			}
			assert.ElementsMatch(t, expectedDevices, actualDevices, "device affinity devices are not equal for device %s dimension %s", device, dimName)
		}
	}
}

func TestDeviceTopologyRegistry_HasAnyDeviceAffinity(t *testing.T) {
	t.Parallel()

	// gpu and rdma share NUMA node 0 → affinity exists.
	gpuTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"gpu-0": {NumaNodes: []int{0}},
		},
	}
	rdmaTopology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"rdma-0": {NumaNodes: []int{0}},
		},
	}
	// disjoint topologies for negative cases.
	apuTopology := &DeviceTopology{
		PriorityDimensions: []string{"pcie"},
		Devices: map[string]DeviceInfo{
			"apu-0": {Dimensions: map[string]string{"pcie": "0"}, NumaNodes: []int{50}},
		},
	}
	bpuTopology := &DeviceTopology{
		PriorityDimensions: []string{"fabric"},
		Devices: map[string]DeviceInfo{
			"bpu-0": {Dimensions: map[string]string{"fabric": "0"}, NumaNodes: []int{99}},
		},
	}

	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	registry.RegisterDeviceTopologyProvider("gpu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("rdma", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("apu", NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("bpu", NewDeviceTopologyProviderStub())
	assert.NoError(t, registry.SetDeviceTopology("gpu", gpuTopology))
	assert.NoError(t, registry.SetDeviceTopology("rdma", rdmaTopology))
	assert.NoError(t, registry.SetDeviceTopology("apu", apuTopology))
	assert.NoError(t, registry.SetDeviceTopology("bpu", bpuTopology))

	tests := []struct {
		name     string
		setA     []string
		setB     []string
		expected bool
	}{
		{name: "affinity exists", setA: []string{"gpu"}, setB: []string{"rdma"}, expected: true},
		{name: "no affinity", setA: []string{"apu"}, setB: []string{"bpu"}, expected: false},
		{name: "found across multiple A", setA: []string{"apu", "gpu"}, setB: []string{"rdma"}, expected: true},
		{name: "unknown devices return false", setA: []string{"nonexistent"}, setB: []string{"also-missing"}, expected: false},
		{name: "empty A", setA: nil, setB: []string{"gpu"}, expected: false},
		{name: "empty B", setA: []string{"gpu"}, setB: nil, expected: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := registry.HasAnyDeviceAffinity(tc.setA, tc.setB)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestHasAnyDeviceAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		topoA    *DeviceTopology
		topoB    *DeviceTopology
		expected bool
	}{
		{
			name: "matching dimension value",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {Dimensions: map[string]string{"pcie": "0"}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {Dimensions: map[string]string{"pcie": "0"}},
			}},
			expected: true,
		},
		{
			name: "matching numa node",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {NumaNodes: []int{0, 1}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {NumaNodes: []int{1, 2}},
			}},
			expected: true,
		},
		{
			name: "dimension key shared but values differ",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {Dimensions: map[string]string{"pcie": "0"}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {Dimensions: map[string]string{"pcie": "1"}},
			}},
			expected: false,
		},
		{
			name: "dimension keys disjoint",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {Dimensions: map[string]string{"pcie": "0"}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {Dimensions: map[string]string{"fabric": "0"}},
			}},
			expected: false,
		},
		{
			name: "numa nodes disjoint",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {NumaNodes: []int{0}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {NumaNodes: []int{1}},
			}},
			expected: false,
		},
		{
			name: "match found across multiple devices",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {NumaNodes: []int{0}},
				"a-1": {NumaNodes: []int{2}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {NumaNodes: []int{1}},
				"b-1": {NumaNodes: []int{2}},
			}},
			expected: true,
		},
		{
			name: "dimension match preferred when both present",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {Dimensions: map[string]string{"pcie": "0"}, NumaNodes: []int{9}},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {Dimensions: map[string]string{"pcie": "0"}, NumaNodes: []int{0}},
			}},
			expected: true,
		},
		{
			name:     "empty topoA",
			topoA:    &DeviceTopology{Devices: map[string]DeviceInfo{}},
			topoB:    &DeviceTopology{Devices: map[string]DeviceInfo{"b-0": {NumaNodes: []int{0}}}},
			expected: false,
		},
		{
			name:     "empty topoB",
			topoA:    &DeviceTopology{Devices: map[string]DeviceInfo{"a-0": {NumaNodes: []int{0}}}},
			topoB:    &DeviceTopology{Devices: map[string]DeviceInfo{}},
			expected: false,
		},
		{
			name: "device without dimensions or numa nodes",
			topoA: &DeviceTopology{Devices: map[string]DeviceInfo{
				"a-0": {},
			}},
			topoB: &DeviceTopology{Devices: map[string]DeviceInfo{
				"b-0": {NumaNodes: []int{0}, Dimensions: map[string]string{"pcie": "0"}},
			}},
			expected: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hasAnyDeviceAffinity(tc.topoA, tc.topoB)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestDeviceTopology_GetDeviceNUMANodes(t *testing.T) {
	t.Parallel()

	topology := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"device-0": {NumaNodes: []int{0}},
			"device-1": {NumaNodes: []int{0, 1}},
			"device-2": {},
			"device-3": {NumaNodes: []int{2}},
		},
	}
	var nilTopology *DeviceTopology
	tests := []struct {
		name      string
		topology  *DeviceTopology
		deviceIDs []string
		expected  CPUSet
	}{
		{
			name:      "combine device NUMA nodes",
			topology:  topology,
			deviceIDs: []string{"device-0", "device-1"},
			expected:  NewCPUSet(0, 1),
		},
		{
			name:     "no devices",
			topology: topology,
			expected: NewCPUSet(),
		},
		{
			name:      "unknown device",
			topology:  topology,
			deviceIDs: []string{"unknown"},
			expected:  NewCPUSet(),
		},
		{
			name:      "device without NUMA nodes",
			topology:  topology,
			deviceIDs: []string{"device-2"},
			expected:  NewCPUSet(FallbackNUMANodeID),
		},
		{
			name:      "continue after device without NUMA nodes",
			topology:  topology,
			deviceIDs: []string{"device-2", "device-3"},
			expected:  NewCPUSet(FallbackNUMANodeID, 2),
		},
		{
			name:      "nil topology",
			topology:  nilTopology,
			deviceIDs: []string{"device-0"},
			expected:  NewCPUSet(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.True(t, tt.topology.GetDeviceNUMANodes(tt.deviceIDs...).Equals(tt.expected))
		})
	}
}

func TestDeviceTopology_GroupDeviceAffinity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		deviceTopology         *DeviceTopology
		expectedDeviceAffinity [][]DeviceIDs
		expectedNil            bool
	}{
		{
			name:        "no affinity groups when PriorityDimensions is empty",
			expectedNil: true,
			deviceTopology: &DeviceTopology{
				PriorityDimensions: nil,
				Devices: map[string]DeviceInfo{
					"npu-0": {
						Dimensions: map[string]string{"pcie": "0"},
					},
					"npu-1": {
						Dimensions: map[string]string{"pcie": "0"},
					},
				},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"pcie": "0"}},
					"npu-1": {Dimensions: map[string]string{"pcie": "0"}},
					"npu-2": {Dimensions: map[string]string{"pcie": "1"}},
					"npu-3": {Dimensions: map[string]string{"pcie": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
			},
		},
		{
			name: "test simple affinity of 4 devices to 1 group with only affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"numa": "0"}},
					"npu-4": {Dimensions: map[string]string{"numa": "1"}},
					"npu-5": {Dimensions: map[string]string{"numa": "1"}},
					"npu-6": {Dimensions: map[string]string{"numa": "1"}},
					"npu-7": {Dimensions: map[string]string{"numa": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1", "npu-2", "npu-3"}, {"npu-4", "npu-5", "npu-6", "npu-7"}},
			},
		},
		{
			name: "device topology includes self for one affinity level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"numa": "1"}},
					"npu-3": {Dimensions: map[string]string{"numa": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
			},
		},
		{
			name: "test simple affinity of 2 devices to 1 group with 2 affinity priority level",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
				{{"npu-0", "npu-1", "npu-2", "npu-3"}},
			},
		},
		{
			name: "device topology includes self for 2 affinity levels",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"pcie", "numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"pcie": "0", "numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"pcie": "1", "numa": "0"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1"}, {"npu-2", "npu-3"}},
				{{"npu-0", "npu-1", "npu-2", "npu-3"}},
			},
		},
		{
			name: "unsorted device topology has no effect on result",
			deviceTopology: &DeviceTopology{
				PriorityDimensions: []string{"numa"},
				Devices: map[string]DeviceInfo{
					"npu-0": {Dimensions: map[string]string{"numa": "0"}},
					"npu-1": {Dimensions: map[string]string{"numa": "0"}},
					"npu-2": {Dimensions: map[string]string{"numa": "0"}},
					"npu-3": {Dimensions: map[string]string{"numa": "0"}},
					"npu-4": {Dimensions: map[string]string{"numa": "1"}},
					"npu-5": {Dimensions: map[string]string{"numa": "1"}},
					"npu-6": {Dimensions: map[string]string{"numa": "1"}},
					"npu-7": {Dimensions: map[string]string{"numa": "1"}},
				},
			},
			expectedDeviceAffinity: [][]DeviceIDs{
				{{"npu-0", "npu-1", "npu-2", "npu-3"}, {"npu-4", "npu-5", "npu-6", "npu-7"}},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deviceAffinity := tt.deviceTopology.GroupDeviceAffinity()
			if tt.expectedNil {
				assert.Nil(t, deviceAffinity)
				return
			}
			evaluateDeviceAffinity(t, deviceAffinity, tt.expectedDeviceAffinity)
		})
	}
}

func evaluateDeviceAffinity(t *testing.T, expectedDeviceAffinity, actualDeviceAffinity [][]DeviceIDs) {
	if len(actualDeviceAffinity) != len(expectedDeviceAffinity) {
		t.Errorf("expected %d affinities, got %d", len(expectedDeviceAffinity), len(actualDeviceAffinity))
		return
	}

	for priority := range expectedDeviceAffinity {
		if !equalDeviceIDsGroupsIgnoreOrder(t, expectedDeviceAffinity[priority], actualDeviceAffinity[priority]) {
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

func TestDeviceTopologyRegistry_runAffinityProviders(t *testing.T) {
	t.Parallel()

	stopCh := make(chan struct{})

	// Set up the device topology registry and register the affinity provider stub
	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	affinityProviderWithValidChannel := newAffinityProviderStub(false)
	registry.RegisterDeviceTopologyProvider("test", NewDeviceTopologyProviderStub())
	registry.RegisterTopologyAffinityProvider("test", affinityProviderWithValidChannel)
	registry.lastDeviceTopologies["test"] = &DeviceTopology{}

	affinityProviderWithNilChannel := newAffinityProviderStub(true)
	registry.RegisterDeviceTopologyProvider("test-nil-chan", NewDeviceTopologyProviderStub())
	registry.RegisterTopologyAffinityProvider("test-nil-chan", affinityProviderWithNilChannel)
	registry.lastDeviceTopologies["test-nil-chan"] = &DeviceTopology{}

	go registry.runAffinityProviders(stopCh)

	time.Sleep(50 * time.Millisecond) // small delay to ensure watcher is ready

	providerStub, ok := affinityProviderWithValidChannel.(*deviceAffinityProviderStub)
	assert.True(t, ok)

	// Trigger change
	providerStub.TriggerChange()

	time.Sleep(100 * time.Millisecond)

	assert.True(t, providerStub.WasSetCalled())

	providerStubWithNilChannel, ok := affinityProviderWithNilChannel.(*deviceAffinityProviderStub)
	assert.True(t, ok)

	providerStubWithNilChannel.TriggerChange()

	time.Sleep(100 * time.Millisecond)

	// nil channel should not have SetDeviceAffinity called
	assert.False(t, providerStubWithNilChannel.WasSetCalled())

	close(stopCh)
}

func TestDeviceTopologyRegistry_GetDeviceTopologies(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	gpu1Provider := NewDeviceTopologyProviderStub()
	gpu2Provider := NewDeviceTopologyProviderStub()
	registry.RegisterDeviceTopologyProvider("gpu-1", gpu1Provider)
	registry.RegisterDeviceTopologyProvider("gpu-2", gpu2Provider)

	topo1 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Unhealthy", NumaNodes: []int{0}},
			"d2": {Health: "Healthy", NumaNodes: []int{1}},
		},
		PriorityDimensions: []string{"NUMA"},
		UpdateTime:         100,
	}
	topo2 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Healthy", NumaNodes: []int{0}},
			"d3": {Health: "Healthy", NumaNodes: []int{2}},
		},
		UpdateTime: 200,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)
	_ = registry.SetDeviceTopology("gpu-2", topo2)

	tests := []struct {
		name        string
		deviceNames []string
		expectedLen int
		expectOk    bool
		checkHealth map[string]string
	}{
		{
			name:        "get topologies from two existing devices",
			deviceNames: []string{"gpu-1", "gpu-2"},
			expectedLen: 2, // both topo1 and topo2
			expectOk:    true,
		},
		{
			name:        "one device missing, pick existing one",
			deviceNames: []string{"gpu-1", "non-existent"},
			expectedLen: 1, // only topo1
			expectOk:    true,
		},
		{
			name:        "all devices missing",
			deviceNames: []string{"invalid-1", "invalid-2"},
			expectOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			topologies, ok := registry.GetDeviceTopologies(tt.deviceNames)
			assert.Equal(t, tt.expectOk, ok)
			if tt.expectOk {
				assert.Len(t, topologies, tt.expectedLen)
			}
		})
	}
}

func TestDeviceInfo_GetDimensions(t *testing.T) {
	t.Parallel()

	deviceInfo := DeviceInfo{
		Dimensions: DeviceDimensions{
			"numa":   "0",
			"":       "1",
			"socket": "",
			"pcie":   "2",
		},
	}

	dimensions := deviceInfo.GetDimensions()
	// GetDimensions currently returns the raw DeviceDimensions map without
	// additional filtering or ordering. Verify that behavior here.
	assert.Equal(t, deviceInfo.Dimensions, dimensions)
}

func TestDeviceTopologyRegistry_GetLatestDeviceTopology(t *testing.T) {
	t.Parallel()

	registry := NewDeviceTopologyRegistry(metrics.DummyMetrics{})
	gpu1Provider := NewDeviceTopologyProviderStub()
	gpu2Provider := NewDeviceTopologyProviderStub()
	registry.RegisterDeviceTopologyProvider("gpu-1", gpu1Provider)
	registry.RegisterDeviceTopologyProvider("gpu-2", gpu2Provider)

	topo1 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Unhealthy", NumaNodes: []int{0}},
			"d2": {Health: "Healthy", NumaNodes: []int{1}},
		},
		PriorityDimensions: []string{"NUMA"},
		UpdateTime:         100,
	}
	topo2 := &DeviceTopology{
		Devices: map[string]DeviceInfo{
			"d1": {Health: "Healthy", NumaNodes: []int{0}},
			"d3": {Health: "Healthy", NumaNodes: []int{2}},
		},
		UpdateTime: 200,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)
	_ = registry.SetDeviceTopology("gpu-2", topo2)

	tests := []struct {
		name         string
		deviceNames  []string
		expectedLen  int
		expectedName string
		expectErr    bool
		checkHealth  map[string]string
	}{
		{
			name:         "pick latest from two existing devices",
			deviceNames:  []string{"gpu-1", "gpu-2"},
			expectedLen:  2, // Only topo2.Devices (d1, d3)
			expectedName: "gpu-2",
			checkHealth:  map[string]string{"d1": "Healthy", "d3": "Healthy"},
		},
		{
			name:         "one device missing, pick existing one",
			deviceNames:  []string{"gpu-1", "non-existent"},
			expectedLen:  2, // Only topo1.Devices (d1, d2)
			expectedName: "gpu-1",
			checkHealth:  map[string]string{"d1": "Unhealthy", "d2": "Healthy"},
		},
		{
			name:        "all devices missing",
			deviceNames: []string{"invalid-1", "invalid-2"},
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			latest, latestName, err := registry.GetLatestDeviceTopology(tt.deviceNames)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, latest.Devices, tt.expectedLen)
				assert.Equal(t, tt.expectedName, latestName)
				for id, health := range tt.checkHealth {
					assert.Equal(t, health, latest.Devices[id].Health)
				}
			}
		})
	}
}

func TestGetAffinityFromDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		topoA    *DeviceTopology
		topoB    *DeviceTopology
		expected map[string]map[string]DeviceIDs
	}{
		{
			name: "common keys (numa, pcie) with matching values are grouped per dimension",
			topoA: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"a0": {
						Dimensions: DeviceDimensions{"pcie": "p0", "numa": "0"},
					},
				},
			},
			topoB: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"b0": {
						Dimensions: DeviceDimensions{"numa": "0", "pcie": "p0"},
					},
					"b1": {
						Dimensions: DeviceDimensions{"numa": "0", "pcie": "p1"},
					},
					"b2": {
						Dimensions: DeviceDimensions{"numa": "1", "pcie": "p0"},
					},
				},
			},
			expected: map[string]map[string]DeviceIDs{
				"a0": {
					"numa": {"b0", "b1"},
					"pcie": {"b0", "b2"},
				},
			},
		},
		{
			name: "no common dimension keys produces empty result",
			topoA: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"a0": {
						Dimensions: DeviceDimensions{"pcie": "p0"},
					},
				},
			},
			topoB: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"b0": {
						Dimensions: DeviceDimensions{"socket": "s0"},
					},
				},
			},
			expected: map[string]map[string]DeviceIDs{},
		},
		{
			name: "common key with no matching value omits device A entry",
			topoA: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"a0": {
						Dimensions: DeviceDimensions{"numa": "0"},
					},
				},
			},
			topoB: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"b0": {
						Dimensions: DeviceDimensions{"numa": "1"},
					},
				},
			},
			expected: map[string]map[string]DeviceIDs{},
		},
		{
			name: "partial overlap: only the shared key with matching value is reported",
			topoA: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"a0": {
						Dimensions: DeviceDimensions{"numa": "0", "pcie": "p0"},
					},
				},
			},
			topoB: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"b0": {
						// Has numa key with matching value; no pcie key.
						Dimensions: DeviceDimensions{"numa": "0"},
					},
				},
			},
			expected: map[string]map[string]DeviceIDs{
				"a0": {
					"numa": {"b0"},
				},
			},
		},
		{
			name: "multiple devices in A produce per-device groupings",
			topoA: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"a0": {
						Dimensions: DeviceDimensions{"numa": "0"},
					},
					"a1": {
						Dimensions: DeviceDimensions{"numa": "1"},
					},
				},
			},
			topoB: &DeviceTopology{
				Devices: map[string]DeviceInfo{
					"b0": {
						Dimensions: DeviceDimensions{"numa": "0"},
					},
					"b1": {
						Dimensions: DeviceDimensions{"numa": "1"},
					},
				},
			},
			expected: map[string]map[string]DeviceIDs{
				"a0": {"numa": {"b0"}},
				"a1": {"numa": {"b1"}},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := getAffinityFromDimensions(tt.topoA, tt.topoB)
			assert.Len(t, got, len(tt.expected))
			for deviceA, expectedDimGroups := range tt.expected {
				gotDimGroups, ok := got[deviceA]
				assert.True(t, ok, "expected device %s in result", deviceA)
				assert.Len(t, gotDimGroups, len(expectedDimGroups))
				for dimKey, expectedIDs := range expectedDimGroups {
					gotIDs, ok := gotDimGroups[dimKey]
					assert.True(t, ok, "expected dimension key %s for device %s", dimKey, deviceA)
					assert.ElementsMatch(t, expectedIDs, gotIDs)
				}
			}
		})
	}
}
