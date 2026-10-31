/*
Copyright 2026 The Katalyst Authors.

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

func TestCoreAlignmentUsesActualSiblingsOnNonUniformSMT(t *testing.T) {
	t.Parallel()

	topology := &machine.CPUTopology{
		NumCPUs:    3,
		NumCores:   2,
		NumSockets: 2,
		CPUDetails: machine.CPUDetails{
			0: {SocketID: 0, NUMANodeID: 0, CoreID: 0},
			1: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
			2: {SocketID: 1, NUMANodeID: 1, CoreID: 0},
		},
	}
	smt2Core := machine.NewCPUSet(1, 2)

	t.Run("candidate selection", func(t *testing.T) {
		t.Parallel()
		selected := takeCoreAlignedCPUSet(topology, smt2Core, machine.NewCPUSet(), 2)
		require.True(t, selected.Equals(smt2Core),
			"selected %s, want complete SMT2 core %s", selected.String(), smt2Core.String())
	})

	t.Run("alignment assertion", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, assertCoreAligned(smt2Core, topology))
		require.ErrorContains(t, assertCoreAligned(machine.NewCPUSet(1), topology), "1 of 2 siblings")
	})
}
