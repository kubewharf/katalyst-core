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
	"errors"
	"fmt"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	ErrInvalidPendingProtection      = errors.New("invalid pending protection")
	ErrPendingProtectionScopeUnknown = errors.New(
		"pending protection scope is outside controlled primary topology")
)

type PendingProtectionSource string

const (
	PendingProtectionSourceExistingPod PendingProtectionSource = "existing_pod"
	PendingProtectionSourceExpectedPod PendingProtectionSource = "expected_pod"
)

type PendingProtection struct {
	ScopeRel string
	CPUs     machine.CPUSet
	PodUID   string
	Source   PendingProtectionSource

	// SharedProjectedCPUs is the per-container subset of CPUs on this pending
	// protection that are eligible for the shared non-reclaim projection. It is
	// the union of the CPUs of the eligible non-SNB shared ramp-up containers
	// (CheckShared && RampUp && !CheckSharedNUMABinding) observed while the
	// prepare snapshot was HardPartition=false && overlap=false. The remaining
	// CPUs (CPUs - SharedProjectedCPUs) stay exact and are folded whole-core.
	// Empty means the whole protection is exact. It is internal bulkhead
	// bookkeeping, carried per container from pendingContainerCPUSet; never a
	// public/business or proto contract.
	SharedProjectedCPUs machine.CPUSet
}

// PendingProtectionUnion returns the union of all pending protection CPUs. It
// is the single source for the folded pending set consumed by the read-only
// topology shadow partition.
func PendingProtectionUnion(protections []PendingProtection) machine.CPUSet {
	out := machine.NewCPUSet()
	for _, protection := range protections {
		out = out.Union(protection.CPUs)
	}
	return out
}

func pendingProtectionUnion(protections []PendingProtection) machine.CPUSet {
	return PendingProtectionUnion(protections)
}

// pendingRequiredCPUSetByRel expands each pending protection to the controlled
// primary ancestor closure that owns its scope, unioning the protected CPUs at
// every matching ancestor.
//
// Invariants:
//   - A protection whose scope resolves to NO controlled primary node is
//     rejected with ErrPendingProtectionScopeUnknown (fail-closed). This
//     prevents a pending container from "escaping" the bulkhead boundary into
//     an uncontrolled cgroup.
//   - Only DomainPrimary nodes receive the widened CPUs; reclaim and system
//     domains are deliberately excluded so pending protections never leak
//     into the reclaim side.
//   - The returned map is consumed by widenPrimaryTargetsWithScopedProtections,
//     which unions these CPUs into the effective target before the
//     parent-superset check.
func pendingRequiredCPUSetByRel(
	dag *TopoDAG,
	protections []PendingProtection,
) (map[string]machine.CPUSet, error) {
	if dag == nil {
		return nil, fmt.Errorf("%w: nil topology DAG", ErrInvalidPendingProtection)
	}

	required := make(map[string]machine.CPUSet)
	for _, protection := range protections {
		scope, err := cleanHierarchyRel(protection.ScopeRel)
		if err != nil || scope != protection.ScopeRel || protection.CPUs.IsEmpty() {
			return nil, fmt.Errorf("%w: pod=%q scope=%q cpus=%s",
				ErrInvalidPendingProtection,
				protection.PodUID,
				protection.ScopeRel,
				protection.CPUs.String(),
			)
		}

		matched := false
		for _, node := range dag.Nodes() {
			if node.Domain != DomainPrimary ||
				(scope != node.Rel && !strings.HasPrefix(scope, node.Rel+"/")) {
				continue
			}
			required[node.Rel] = required[node.Rel].Union(protection.CPUs)
			matched = true
		}
		if !matched {
			return nil, fmt.Errorf("%w: pod=%q scope=%q",
				ErrPendingProtectionScopeUnknown, protection.PodUID, scope)
		}
	}
	return required, nil
}
