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

package dynamicpolicy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestTargetDrivenHardReclaimConfigGate(t *testing.T) {
	t.Parallel()
	// old preferred footprint = 30 cpus; frozen target = 28; donatable excess = 2.
	oldPreferred := machine.NewCPUSet()
	for c := 0; c < 15; c++ {
		oldPreferred = oldPreferred.Union(machine.NewCPUSet(c, c+16))
	}
	require.Equal(t, 30, oldPreferred.Size())

	descs := []advisorBlockDescriptor{
		{BlockID: "d1", Class: advisorBlockClassDedicated, NUMAID: 0, Quantity: 28, OldPreferred: oldPreferred},
		{BlockID: "mandatory-reclaim-numa-0", Class: advisorBlockClassMandatoryReclaim, NUMAID: 0, Quantity: 4},
	}

	// Without config set, all conditions false -> legacy (TargetDriven stays false).
	legacy := (&DynamicPolicy{}).applyTargetDrivenReclaimGate(descs)
	require.False(t, legacy[0].TargetDriven)
	require.Equal(t, 0, legacy[0].SourceTarget)
	require.Equal(t, 0, legacy[0].ReclaimQuota)

	// Single dedicated source per NUMA: not ambiguous.
	require.Empty(t, identifyAmbiguousTargetDrivenNUMAs(descs))

	// Two dedicated sources on the same NUMA (different BlockIDs): NUMA0 ambiguous.
	multi := []advisorBlockDescriptor{
		{BlockID: "d1", Class: advisorBlockClassDedicated, NUMAID: 0},
		{BlockID: "d2", Class: advisorBlockClassDedicated, NUMAID: 0},
	}
	ambiguous := identifyAmbiguousTargetDrivenNUMAs(multi)
	require.Contains(t, ambiguous, 0)
}
