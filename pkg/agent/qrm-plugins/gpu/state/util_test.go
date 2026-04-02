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

package state

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGenerateDefaultResourceState_MultiDevices(t *testing.T) {
	t.Parallel()

	registry := machine.NewDeviceTopologyRegistry()
	registry.RegisterDeviceTopologyProvider("gpu-1", machine.NewDeviceTopologyProviderStub())
	registry.RegisterDeviceTopologyProvider("gpu-2", machine.NewDeviceTopologyProviderStub())

	topo1 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"d1": {Health: "Healthy"},
		},
		UpdateTime: 100,
	}
	topo2 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"d2": {Health: "Healthy"},
		},
		UpdateTime: 200,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)
	_ = registry.SetDeviceTopology("gpu-2", topo2)

	generator := NewGenericDefaultResourceStateGenerator([]string{"gpu-1", "gpu-2"}, registry, 1)
	state, err := generator.GenerateDefaultResourceState()

	assert.NoError(t, err)
	assert.Len(t, state, 1)
	assert.Contains(t, state, "d2")
}

func TestGenerateDefaultResourceState_PartialMissing(t *testing.T) {
	t.Parallel()

	registry := machine.NewDeviceTopologyRegistry()
	registry.RegisterDeviceTopologyProvider("gpu-1", machine.NewDeviceTopologyProviderStub())

	topo1 := &machine.DeviceTopology{
		Devices: map[string]machine.DeviceInfo{
			"d1": {Health: "Healthy"},
		},
		UpdateTime: 100,
	}

	_ = registry.SetDeviceTopology("gpu-1", topo1)

	// One device missing, pick the existing one (d1)
	generator := NewGenericDefaultResourceStateGenerator([]string{"gpu-1", "non-existent"}, registry, 1)
	state, err := generator.GenerateDefaultResourceState()

	assert.NoError(t, err)
	assert.Len(t, state, 1)
	assert.Contains(t, state, "d1")
}
