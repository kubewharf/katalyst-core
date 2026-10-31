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

func TestReleaseWitnessBindsSnapshotSourceBoundaryAndEdge(t *testing.T) {
	t.Parallel()

	after := planSnapshot(map[string]EntryState{
		"source": {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0)},
	}, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0),
	})
	after.ScanBoundary = ScanBoundary{Purpose: ScanForWitness, Roots: []string{"source"}}
	after.ID = fingerprintSnapshot(after)

	witness := NewReleaseWitness("convergence-1", "source", "destination", machine.NewCPUSet(0, 1), after)
	if got, want := witness.CPUs, machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("released CPUs = %s, want %s", got.String(), want.String())
	}
	if !ValidateReleaseWitness(witness, "convergence-1", "source", "destination", after) {
		t.Fatal("fresh matching source boundary rejected witness")
	}
	if ValidateReleaseWitness(witness, "convergence-2", "source", "destination", after) {
		t.Fatal("witness reused across convergence")
	}
	if ValidateReleaseWitness(witness, "convergence-1", "source", "other", after) {
		t.Fatal("witness reused across edge")
	}
	changed := planSnapshot(map[string]EntryState{
		"source": {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0)},
	}, map[DomainID]machine.CPUSet{"source": machine.NewCPUSet(0)})
	changed.ScanBoundary = after.ScanBoundary
	changed.ID = fingerprintSnapshot(changed)
	if ValidateReleaseWitness(witness, "convergence-1", "source", "destination", changed) {
		t.Fatal("witness accepted after source boundary identity change")
	}
}

func TestDomainGateDoesNotGrowDestinationAfterSourceRecreateOrRecovery(t *testing.T) {
	t.Parallel()

	afterDrain := planSnapshot(map[string]EntryState{
		"source":      {Identity: CgroupIdentity{Device: 1, Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"destination": {Identity: CgroupIdentity{Device: 1, Inode: 2}, CPUs: machine.NewCPUSet(2), Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(2),
	})
	afterDrain.DomainByRel = map[string]DomainID{"source": "source", "destination": "destination"}
	afterDrain.ScanBoundary = ScanBoundary{
		Purpose: ScanForPlan,
		Roots:   []string{"destination", "source"},
	}
	afterDrain.ID = fingerprintSnapshot(afterDrain)
	witness := NewReleaseWitness(
		"convergence", "source", "destination", machine.NewCPUSet(1), afterDrain)
	desired := map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(1, 2),
	}

	tests := []struct {
		name     string
		identity CgroupIdentity
	}{
		{name: "same-name source recreated", identity: CgroupIdentity{Device: 1, Inode: 9}},
		{name: "source ownership recovered", identity: CgroupIdentity{Device: 1, Inode: 1}},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fresh := clonePlanSnapshot(afterDrain)
			fresh.Entries["source"] = EntryState{
				Identity: tc.identity, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
			}
			fresh.DomainUnion["source"] = machine.NewCPUSet(0, 1)
			fresh.ID = fingerprintSnapshot(fresh)

			gate, err := NewDomainGate(
				"convergence", fresh, desired, machine.NewCPUSet(0, 1, 2), []ReleaseWitness{witness})
			if err != nil {
				t.Fatalf("NewDomainGate: %v", err)
			}
			got := gate.AllowedGrowTarget(
				"destination", desired["destination"], fresh.DomainUnion["destination"])
			if got.Contains(1) {
				t.Fatalf("destination grow target %s overlaps restored source ownership %s",
					got.String(), fresh.DomainUnion["source"].String())
			}
			if overlap := got.Intersection(fresh.DomainUnion["source"]); !overlap.IsEmpty() {
				t.Fatalf("destination grow target %s overlaps source on %s", got.String(), overlap.String())
			}
		})
	}
}

func TestDomainGateRejectsWitnessWhenThirdDomainCurrentlyOwnsCPU(t *testing.T) {
	t.Parallel()

	afterDrain := planSnapshot(nil, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(2),
	})
	witness := NewReleaseWitness("convergence", "source", "destination", machine.NewCPUSet(1), afterDrain)

	thirdDomainClaimed := clonePlanSnapshot(afterDrain)
	thirdDomainClaimed.DomainUnion["third"] = machine.NewCPUSet(1)
	thirdDomainClaimed.ID = fingerprintSnapshot(thirdDomainClaimed)
	gate, err := NewDomainGate("convergence", thirdDomainClaimed, map[DomainID]machine.CPUSet{
		"source":      machine.NewCPUSet(0),
		"destination": machine.NewCPUSet(1, 2),
		"third":       machine.NewCPUSet(),
	}, machine.NewCPUSet(0, 1, 2), []ReleaseWitness{witness})
	if err != nil {
		t.Fatalf("NewDomainGate: %v", err)
	}
	if got := gate.AllowedEntering("destination"); got.Contains(1) {
		t.Fatalf("third-domain-owned CPU consumed from witness: %s", got.String())
	}
}

func TestReleaseWitnessRecomputesSourceEvidenceAndBoundary(t *testing.T) {
	t.Parallel()

	after := planSnapshot(map[string]EntryState{
		"source":      {Identity: CgroupIdentity{Device: 1, Inode: 1}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"destination": {Identity: CgroupIdentity{Device: 1, Inode: 2}, CPUs: machine.NewCPUSet(2), Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		"source": machine.NewCPUSet(0), "destination": machine.NewCPUSet(2),
	})
	after.DomainByRel = map[string]DomainID{"source": "source", "destination": "destination"}
	after.ScanBoundary = ScanBoundary{Purpose: ScanForPlan, Roots: []string{"destination", "source"}}
	after.ID = fingerprintSnapshot(after)
	witness := NewReleaseWitness("plan", "source", "destination", machine.NewCPUSet(1), after)
	if witness.SourceEvidenceID == (SnapshotID{}) {
		t.Fatal("witness has zero SourceEvidenceID")
	}

	destinationChanged := clonePlanSnapshot(after)
	destinationChanged.Entries["destination"] = EntryState{
		Identity: after.Entries["destination"].Identity, CPUs: machine.NewCPUSet(1, 2), Mems: "0",
	}
	destinationChanged.DomainUnion["destination"] = machine.NewCPUSet(1, 2)
	destinationChanged.ID = fingerprintSnapshot(destinationChanged)
	if destinationChanged.ID == after.ID {
		t.Fatal("destination write did not change full SnapshotID")
	}
	if !ValidateReleaseWitness(witness, "plan", "source", "destination", destinationChanged) {
		t.Fatal("legal destination-only write invalidated source witness")
	}

	zeroID := witness
	zeroID.SourceEvidenceID = SnapshotID{}
	if ValidateReleaseWitness(zeroID, "plan", "source", "destination", after) {
		t.Fatal("zero SourceEvidenceID authorized expand")
	}
	tamperedID := witness
	tamperedID.SourceEvidenceID[0]++
	if ValidateReleaseWitness(tamperedID, "plan", "source", "destination", after) {
		t.Fatal("tampered SourceEvidenceID authorized expand")
	}
	tamperedBoundary := witness
	tamperedBoundary.SourceBoundaryFingerprint += "-tampered"
	if ValidateReleaseWitness(tamperedBoundary, "plan", "source", "destination", after) {
		t.Fatal("tampered source boundary authorized expand")
	}

	mutations := map[string]func(*CompleteSnapshot){
		"source identity": func(s *CompleteSnapshot) {
			entry := s.Entries["source"]
			entry.Identity.Inode++
			s.Entries["source"] = entry
		},
		"source ownership": func(s *CompleteSnapshot) {
			s.DomainUnion["source"] = machine.NewCPUSet(0, 1)
		},
		"source cpus": func(s *CompleteSnapshot) {
			entry := s.Entries["source"]
			entry.CPUs = machine.NewCPUSet(0, 1)
			s.Entries["source"] = entry
		},
		"source mems": func(s *CompleteSnapshot) {
			entry := s.Entries["source"]
			entry.Mems = "0-1"
			s.Entries["source"] = entry
		},
	}
	for name, mutate := range mutations {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fresh := clonePlanSnapshot(after)
			mutate(fresh)
			fresh.ID = fingerprintSnapshot(fresh)
			if ValidateReleaseWitness(witness, "plan", "source", "destination", fresh) {
				t.Fatalf("source change %q did not invalidate witness", name)
			}
		})
	}
}

func clonePlanSnapshot(in *CompleteSnapshot) *CompleteSnapshot {
	out := *in
	out.Entries = make(map[string]EntryState, len(in.Entries))
	for rel, entry := range in.Entries {
		entry.CPUs = entry.CPUs.Clone()
		out.Entries[rel] = entry
	}
	out.DomainUnion = make(map[DomainID]machine.CPUSet, len(in.DomainUnion))
	for domain, cpus := range in.DomainUnion {
		out.DomainUnion[domain] = cpus.Clone()
	}
	out.DomainByRel = make(map[string]DomainID, len(in.DomainByRel))
	for rel, domain := range in.DomainByRel {
		out.DomainByRel[rel] = domain
	}
	return &out
}

func TestDomainGateAllowsWitnessedAndUniquelyDesiredSafeUnownedOnly(t *testing.T) {
	t.Parallel()

	snapshot := planSnapshot(nil, map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(0),
		"b": machine.NewCPUSet(1),
	})
	snapshot.ScanBoundary = ScanBoundary{Purpose: ScanForWitness}
	snapshot.ID = fingerprintSnapshot(snapshot)
	witness := ReleaseWitness{
		ConvergenceID: "plan", Source: "a", Destination: "b", CPUs: machine.NewCPUSet(2),
		SourceEvidenceID: sourceEvidenceID(snapshot, "a"), SourceBoundaryFingerprint: sourceBoundaryFingerprint(snapshot, "a"),
	}
	gate, err := NewDomainGate("plan", snapshot, map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(0, 3),
		"b": machine.NewCPUSet(1, 2, 3, 4),
	}, machine.NewCPUSet(0, 1, 2, 3, 4), []ReleaseWitness{witness})
	if err != nil {
		t.Fatalf("NewDomainGate: %v", err)
	}
	// CPU 2 is witnessed. CPU 4 is unowned and uniquely desired by b. CPU 3 is
	// unowned but desired by two domains, so it must remain unauthorized.
	if got, want := gate.AllowedEntering("b"), machine.NewCPUSet(2, 4); !got.Equals(want) {
		t.Fatalf("allowed entering b = %s, want %s", got.String(), want.String())
	}
	if got, want := gate.AllowedGrowTarget("b", machine.NewCPUSet(1, 2, 3, 4), machine.NewCPUSet(1)), machine.NewCPUSet(1, 2, 4); !got.Equals(want) {
		t.Fatalf("allowed grow target b = %s, want %s", got.String(), want.String())
	}
}

func TestNilDomainGateGrowTargetNeverDeletesObservedCPUs(t *testing.T) {
	t.Parallel()

	var gate *DomainGate
	if got, want := gate.AllowedGrowTarget("destination", machine.NewCPUSet(0), machine.NewCPUSet(0, 1)), machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("nil gate grow target = %s, want observed CPUs preserved %s", got.String(), want.String())
	}
}

func TestDomainGateRejectsEmptyAndStaleConvergenceIDAndRevokesStaleWitness(t *testing.T) {
	t.Parallel()

	snapshot := planSnapshot(nil, map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1),
	})
	witness := ReleaseWitness{
		ConvergenceID: "plan-1", Source: "a", Destination: "b", CPUs: machine.NewCPUSet(2),
		SourceEvidenceID: sourceEvidenceID(snapshot, "a"), SourceBoundaryFingerprint: sourceBoundaryFingerprint(snapshot, "a"),
	}
	desired := map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(0, 2), "b": machine.NewCPUSet(1, 2),
	}
	if _, err := NewDomainGate("", snapshot, desired, machine.NewCPUSet(0, 1, 2), []ReleaseWitness{witness}); err == nil {
		t.Fatal("NewDomainGate accepted empty ConvergenceID")
	}
	gate, err := NewDomainGate("plan-2", snapshot, desired, machine.NewCPUSet(0, 1, 2), []ReleaseWitness{witness})
	if err != nil {
		t.Fatalf("NewDomainGate stale plan: %v", err)
	}
	if got := gate.AllowedEntering("b"); got.Contains(2) {
		t.Fatalf("stale plan witness authorized CPU 2: %s", got.String())
	}
	gate, err = NewDomainGate("plan-1", snapshot, desired, machine.NewCPUSet(0, 1, 2), []ReleaseWitness{witness})
	if err != nil {
		t.Fatalf("NewDomainGate current plan: %v", err)
	}
	if got := gate.AllowedEntering("b"); !got.Contains(2) {
		t.Fatalf("current witness did not authorize CPU 2: %s", got.String())
	}
	fresh := planSnapshot(nil, map[DomainID]machine.CPUSet{
		"a": machine.NewCPUSet(0), "b": machine.NewCPUSet(1),
	})
	fresh.Entries = map[string]EntryState{
		"a/new-owner": {Identity: CgroupIdentity{Inode: 9}, CPUs: machine.NewCPUSet(2)},
	}
	fresh.DomainUnion["a"] = machine.NewCPUSet(0, 2)
	fresh.ID = fingerprintSnapshot(fresh)
	gate.Revalidate("plan-1", fresh, desired)
	if got := gate.AllowedEntering("b"); got.Contains(2) {
		t.Fatalf("stale witness was not revoked before expand: %s", got.String())
	}
}
