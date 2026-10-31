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

package metacache

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
)

func TestSetSupportedWantedFeatureGates_MergeByType(t *testing.T) {
	t.Parallel()

	cpuFG := func(name string) *advisorsvc.FeatureGate {
		return &advisorsvc.FeatureGate{Name: name, Type: finders.FeatureGateTypeCPU}
	}
	memFG := func(name string) *advisorsvc.FeatureGate {
		return &advisorsvc.FeatureGate{Name: name, Type: finders.FeatureGateTypeMemory}
	}

	t.Run("writes preserve other-type entries", func(t *testing.T) {
		t.Parallel()
		mc := NewDummyMetaCacheImp()

		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeCPU, map[string]*advisorsvc.FeatureGate{
			"cpu_a": cpuFG("cpu_a"),
			"cpu_b": cpuFG("cpu_b"),
		}))
		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeMemory, map[string]*advisorsvc.FeatureGate{
			"mem_a": memFG("mem_a"),
		}))

		got, err := mc.GetSupportedWantedFeatureGates()
		require.NoError(t, err)
		assert.Len(t, got, 3)
		assert.Contains(t, got, "cpu_a")
		assert.Contains(t, got, "cpu_b")
		assert.Contains(t, got, "mem_a")
	})

	t.Run("refreshing one type removes stale entries of same type only", func(t *testing.T) {
		t.Parallel()
		mc := NewDummyMetaCacheImp()

		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeCPU, map[string]*advisorsvc.FeatureGate{
			"cpu_a": cpuFG("cpu_a"),
			"cpu_b": cpuFG("cpu_b"),
		}))
		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeMemory, map[string]*advisorsvc.FeatureGate{
			"mem_a": memFG("mem_a"),
		}))

		// cpu_a is no longer wanted; cpu_b stays; a new cpu_c appears
		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeCPU, map[string]*advisorsvc.FeatureGate{
			"cpu_b": cpuFG("cpu_b"),
			"cpu_c": cpuFG("cpu_c"),
		}))

		got, err := mc.GetSupportedWantedFeatureGates()
		require.NoError(t, err)
		assert.NotContains(t, got, "cpu_a")
		assert.Contains(t, got, "cpu_b")
		assert.Contains(t, got, "cpu_c")
		// memory slice untouched
		assert.Contains(t, got, "mem_a")
	})

	t.Run("empty map for a type clears all entries of that type", func(t *testing.T) {
		t.Parallel()
		mc := NewDummyMetaCacheImp()

		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeCPU, map[string]*advisorsvc.FeatureGate{
			"cpu_a": cpuFG("cpu_a"),
		}))
		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeMemory, map[string]*advisorsvc.FeatureGate{
			"mem_a": memFG("mem_a"),
		}))

		require.NoError(t, mc.SetSupportedWantedFeatureGates(finders.FeatureGateTypeCPU, map[string]*advisorsvc.FeatureGate{}))

		got, err := mc.GetSupportedWantedFeatureGates()
		require.NoError(t, err)
		assert.NotContains(t, got, "cpu_a")
		assert.Contains(t, got, "mem_a")
	})
}
