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

package util

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestResolveAffinityDeviceByAccompanyResource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		affinity   map[string]map[string]machine.DeviceIDs
		priorities []string
		expected   map[string]string
	}{
		{
			name: "first non-empty priority and lexical first resource",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g0": {"fine": {"r1", "r0"}, "coarse": {"r2"}},
				"g1": {"fine": {}, "coarse": {"r3", "r2"}},
			},
			priorities: []string{"fine", "coarse"},
			expected:   map[string]string{"g0": "r0", "g1": "r2"},
		},
		{
			name: "empty priorities use numa fallback",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g0": {machine.DimensionNuma: {"r1", "r0"}},
			},
			expected: map[string]string{"g0": "r0"},
		},
		{
			name: "missing matching dimension omits device",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g0": {"other": {"r0"}},
				"g1": {},
			},
			priorities: []string{"fine"},
			expected:   map[string]string{},
		},
		{
			name: "one resource may serve multiple accompany devices",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g0": {"fine": {"r0"}},
				"g1": {"fine": {"r0"}},
			},
			priorities: []string{"fine"},
			expected:   map[string]string{"g0": "r0", "g1": "r0"},
		},
		{
			name: "evenly split one affinity group",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g3": {"pcie": {"r1", "r0"}},
				"g1": {"pcie": {"r1", "r0"}},
				"g0": {"pcie": {"r1", "r0"}},
				"g2": {"pcie": {"r1", "r0"}},
			},
			priorities: []string{"pcie"},
			expected: map[string]string{
				"g0": "r0",
				"g1": "r0",
				"g2": "r1",
				"g3": "r1",
			},
		},
		{
			name: "balance uneven affinity group",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g0": {"pcie": {"r0", "r1"}},
				"g1": {"pcie": {"r0", "r1"}},
				"g2": {"pcie": {"r0", "r1"}},
				"g3": {"pcie": {"r0", "r1"}},
				"g4": {"pcie": {"r0", "r1"}},
			},
			priorities: []string{"pcie"},
			expected: map[string]string{
				"g0": "r0",
				"g1": "r0",
				"g2": "r0",
				"g3": "r1",
				"g4": "r1",
			},
		},
		{
			name: "split independent affinity groups",
			affinity: map[string]map[string]machine.DeviceIDs{
				"g0": {"pcie": {"r1", "r0"}},
				"g1": {"pcie": {"r1", "r0"}},
				"g2": {"pcie": {"r1", "r0"}},
				"g3": {"pcie": {"r1", "r0"}},
				"g4": {"pcie": {"r3", "r2"}},
				"g5": {"pcie": {"r3", "r2"}},
				"g6": {"pcie": {"r3", "r2"}},
				"g7": {"pcie": {"r3", "r2"}},
			},
			priorities: []string{"pcie"},
			expected: map[string]string{
				"g0": "r0",
				"g1": "r0",
				"g2": "r1",
				"g3": "r1",
				"g4": "r2",
				"g5": "r2",
				"g6": "r3",
				"g7": "r3",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := ResolveAffinityDeviceByAccompanyResource(tt.affinity, tt.priorities)

			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestResolveAffinityDeviceByAccompanyResourceDoesNotMutateInput(t *testing.T) {
	t.Parallel()

	g0ResourceIDs := machine.DeviceIDs{"r1", "r0"}
	g1ResourceIDs := machine.DeviceIDs{"r1", "r0"}
	affinity := map[string]map[string]machine.DeviceIDs{
		"g0": {"fine": g0ResourceIDs},
		"g1": {"fine": g1ResourceIDs},
	}

	assert.Equal(t, map[string]string{"g0": "r0", "g1": "r1"},
		ResolveAffinityDeviceByAccompanyResource(affinity, []string{"fine"}))
	assert.Equal(t, machine.DeviceIDs{"r1", "r0"}, g0ResourceIDs)
	assert.Equal(t, machine.DeviceIDs{"r1", "r0"}, g1ResourceIDs)
}

func TestIsSharedDeviceAllocationRequested(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations map[string]string
		key         string
		expected    bool
	}{
		{name: "true", annotations: map[string]string{"key": "true"}, key: "key", expected: true},
		{name: "trimmed mixed case true", annotations: map[string]string{"key": " TRUE "}, key: "key", expected: true},
		{name: "false", annotations: map[string]string{"key": "false"}, key: "key"},
		{name: "empty", annotations: map[string]string{"key": ""}, key: "key"},
		{name: "invalid", annotations: map[string]string{"key": "yes"}, key: "key"},
		{name: "missing", annotations: map[string]string{}, key: "key"},
		{name: "empty key", annotations: map[string]string{"": "true"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, IsSharedDeviceAllocationRequested(tt.annotations, tt.key))
		})
	}
}
