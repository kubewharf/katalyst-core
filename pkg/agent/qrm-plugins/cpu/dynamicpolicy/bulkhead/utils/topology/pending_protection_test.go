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

package topology

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func mustBuildPendingProtectionDAG(t *testing.T) *TopoDAG {
	t.Helper()

	dag, err := BuildDAG([]NodeSpec{
		{
			Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
			CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true, ControlledRoot: true,
		},
		{
			Rel: "kubepods/burstable", ParentRel: "kubepods",
			Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1),
		},
		{
			Rel: "kubepods/burstable/pod-old", ParentRel: "kubepods/burstable",
			Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1),
		},
		{
			Rel: "kubepods/besteffort", ParentRel: "kubepods",
			Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0, 1),
		},
		{
			Rel: "reclaimed", Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
			CPUs: machine.NewCPUSet(2, 3), TrustAnchor: true, ControlledRoot: true,
		},
	})
	require.NoError(t, err)
	return dag
}

func TestPendingProtectionClosureScopesCPUsToControlledAncestors(t *testing.T) {
	t.Parallel()

	dag := mustBuildPendingProtectionDAG(t)
	protections := []PendingProtection{{
		ScopeRel: "kubepods/burstable/pod-new",
		CPUs:     machine.NewCPUSet(2, 3),
		PodUID:   "pod-new",
		Source:   PendingProtectionSourceExpectedPod,
	}}

	required, err := pendingRequiredCPUSetByRel(dag, protections)

	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(2, 3), required["kubepods"])
	require.Equal(t, machine.NewCPUSet(2, 3), required["kubepods/burstable"])
	require.NotContains(t, required, "kubepods/besteffort")
	require.NotContains(t, required, "kubepods/burstable/pod-old")
	require.NotContains(t, required, "reclaimed")
}

func TestPendingProtectionClosureMergesOnlySharedAncestors(t *testing.T) {
	t.Parallel()

	dag := mustBuildPendingProtectionDAG(t)
	protections := []PendingProtection{
		{
			ScopeRel: "kubepods/burstable/pod-shared",
			CPUs:     machine.NewCPUSet(2),
			PodUID:   "pod-shared",
			Source:   PendingProtectionSourceExpectedPod,
		},
		{
			ScopeRel: "kubepods/besteffort/pod-best-effort",
			CPUs:     machine.NewCPUSet(3),
			PodUID:   "pod-best-effort",
			Source:   PendingProtectionSourceExpectedPod,
		},
	}

	required, err := pendingRequiredCPUSetByRel(dag, protections)

	require.NoError(t, err)
	require.Equal(t, machine.NewCPUSet(2, 3), required["kubepods"])
	require.Equal(t, machine.NewCPUSet(2), required["kubepods/burstable"])
	require.Equal(t, machine.NewCPUSet(3), required["kubepods/besteffort"])
	require.NotContains(t, required, "kubepods/burstable/pod-old")
}

func TestPendingProtectionClosureRejectsUnknownScope(t *testing.T) {
	t.Parallel()

	dag := mustBuildPendingProtectionDAG(t)
	_, err := pendingRequiredCPUSetByRel(dag, []PendingProtection{{
		ScopeRel: "unmanaged/pod-new",
		CPUs:     machine.NewCPUSet(2, 3),
		PodUID:   "pod-new",
		Source:   PendingProtectionSourceExpectedPod,
	}})

	require.ErrorIs(t, err, ErrPendingProtectionScopeUnknown)
}

func TestPendingProtectionClosureRejectsEmptyScopeAndCPUSet(t *testing.T) {
	t.Parallel()

	dag := mustBuildPendingProtectionDAG(t)
	tests := []PendingProtection{
		{
			CPUs:   machine.NewCPUSet(2, 3),
			PodUID: "pod-empty-scope",
			Source: PendingProtectionSourceExpectedPod,
		},
		{
			ScopeRel: "kubepods/burstable/pod-empty-cpus",
			CPUs:     machine.NewCPUSet(),
			PodUID:   "pod-empty-cpus",
			Source:   PendingProtectionSourceExpectedPod,
		},
	}

	for _, protection := range tests {
		_, err := pendingRequiredCPUSetByRel(dag, []PendingProtection{protection})
		require.ErrorIs(t, err, ErrInvalidPendingProtection)
	}
}
