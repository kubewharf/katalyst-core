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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestTransitionPlanClassifiesCrossDomainDisjointAsTransfer(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	snapshot := domainSnapshot{
		observedByRel:         map[string]machine.CPUSet{"primary": machine.NewCPUSet(0), "reclaim": machine.NewCPUSet(2)},
		targetByRel:           map[string]machine.CPUSet{"primary": machine.NewCPUSet(2), "reclaim": machine.NewCPUSet(0)},
		observedPrimaryDomain: machine.NewCPUSet(0),
		targetPrimaryDomain:   machine.NewCPUSet(2),
		observedReclaimDomain: machine.NewCPUSet(2),
		targetReclaimDomain:   machine.NewCPUSet(0),
		allowedCPUs:           machine.NewCPUSet(0, 2),
		safeUnownedToPrimary:  machine.NewCPUSet(),
		safeUnownedToReclaim:  machine.NewCPUSet(),
	}
	gate := newDomainGate(snapshot)

	plan := buildTransitionPlan(dag, snapshot, gate)
	primary := plan.byRel["primary"]
	if primary.kind != transitionCrossDomain {
		t.Fatalf("primary kind = %s, want %s", primary.kind, transitionCrossDomain)
	}
	if !primary.crossDomainEntering.Equals(machine.NewCPUSet(2)) {
		t.Fatalf("crossDomainEntering = %s, want 2", primary.crossDomainEntering.String())
	}
	if !primary.bridgeTarget.IsEmpty() {
		t.Fatalf("bridgeTarget = %s, want empty for cross-domain transfer", primary.bridgeTarget.String())
	}
	if got := len(plan.drainReclaimToPrimary); got != 1 {
		t.Fatalf("drainReclaimToPrimary len = %d, want 1", got)
	}
	if got := len(plan.expandPrimary); got != 1 {
		t.Fatalf("expandPrimary len = %d, want 1", got)
	}
}

func TestTransitionPlanAllowsReclaimNUMABucketDomainLocalBridge(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0, 1)},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1), Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	snapshot := domainSnapshot{
		observedByRel:         map[string]machine.CPUSet{"reclaim": machine.NewCPUSet(0, 1), "reclaim/numa-0": machine.NewCPUSet(0)},
		targetByRel:           map[string]machine.CPUSet{"reclaim": machine.NewCPUSet(0, 1), "reclaim/numa-0": machine.NewCPUSet(1)},
		observedPrimaryDomain: machine.NewCPUSet(),
		targetPrimaryDomain:   machine.NewCPUSet(),
		observedReclaimDomain: machine.NewCPUSet(0, 1),
		targetReclaimDomain:   machine.NewCPUSet(0, 1),
		allowedCPUs:           machine.NewCPUSet(0, 1),
		safeUnownedToPrimary:  machine.NewCPUSet(),
		safeUnownedToReclaim:  machine.NewCPUSet(),
	}
	gate := newDomainGate(snapshot)

	plan := buildTransitionPlan(dag, snapshot, gate)
	bucket := plan.byRel["reclaim/numa-0"]
	if bucket.kind != transitionDomainLocalBridge {
		t.Fatalf("bucket kind = %s, want %s", bucket.kind, transitionDomainLocalBridge)
	}
	if got, want := bucket.bridgeTarget, machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("bridgeTarget = %s, want %s", got.String(), want.String())
	}
	if !bucket.crossDomainEntering.IsEmpty() || !bucket.crossDomainLeaving.IsEmpty() {
		t.Fatalf("cross-domain fields must be empty, entering=%s leaving=%s", bucket.crossDomainEntering.String(), bucket.crossDomainLeaving.String())
	}
}
