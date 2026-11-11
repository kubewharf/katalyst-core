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
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type ReleaseWitness struct {
	ConvergenceID             string
	Source                    DomainID
	Destination               DomainID
	CPUs                      machine.CPUSet
	SourceEvidenceID          SnapshotID
	SourceBoundaryFingerprint string
}

type DomainGate struct {
	convergenceID   string
	allowedCPUs     machine.CPUSet
	witnesses       []ReleaseWitness
	allowedEntering map[DomainID]machine.CPUSet
	pending         map[DomainID]machine.CPUSet
	cleanupPending  map[DomainID]machine.CPUSet
}

func NewReleaseWitness(
	convergenceID string,
	source, destination DomainID,
	releaseCandidate machine.CPUSet,
	after *CompleteSnapshot,
) ReleaseWitness {
	released := machine.NewCPUSet()
	if after != nil {
		released = releaseCandidate.Difference(after.DomainUnion[source])
	}
	return ReleaseWitness{
		ConvergenceID:             convergenceID,
		Source:                    source,
		Destination:               destination,
		CPUs:                      released,
		SourceEvidenceID:          sourceEvidenceID(after, source),
		SourceBoundaryFingerprint: sourceBoundaryFingerprint(after, source),
	}
}

func ValidateReleaseWitness(
	witness ReleaseWitness,
	convergenceID string,
	source, destination DomainID,
	freshSource *CompleteSnapshot,
) bool {
	if freshSource == nil ||
		witness.ConvergenceID != convergenceID ||
		witness.Source != source ||
		witness.Destination != destination ||
		witness.SourceEvidenceID == (SnapshotID{}) ||
		witness.SourceEvidenceID != sourceEvidenceID(freshSource, source) ||
		witness.SourceBoundaryFingerprint != sourceBoundaryFingerprint(freshSource, source) {
		return false
	}
	return witness.CPUs.Intersection(freshSource.DomainUnion[source]).IsEmpty()
}

func NewDomainGate(
	convergenceID string,
	snapshot *CompleteSnapshot,
	desired map[DomainID]machine.CPUSet,
	allowedCPUs machine.CPUSet,
	witnesses []ReleaseWitness,
) (*DomainGate, error) {
	gate := &DomainGate{
		convergenceID:   convergenceID,
		allowedCPUs:     allowedCPUs.Clone(),
		witnesses:       append([]ReleaseWitness(nil), witnesses...),
		allowedEntering: make(map[DomainID]machine.CPUSet, len(desired)),
		pending:         make(map[DomainID]machine.CPUSet, len(desired)),
		cleanupPending:  make(map[DomainID]machine.CPUSet, len(desired)),
	}
	if strings.TrimSpace(convergenceID) == "" {
		return nil, fmt.Errorf("domain gate requires non-empty ConvergenceID")
	}
	if allowedCPUs.IsEmpty() {
		return nil, fmt.Errorf("domain gate requires explicit non-empty AllowedCPUs")
	}
	if snapshot == nil {
		return gate, nil
	}
	gate.Revalidate(convergenceID, snapshot, desired)
	return gate, nil
}

// Revalidate rebuilds all expand authorization from a fresh snapshot. Both the
// recomputable source evidence ID and its scan boundary must still match;
// destination-only writes do not change either source-scoped value.
func (g *DomainGate) Revalidate(convergenceID string, snapshot *CompleteSnapshot, desired map[DomainID]machine.CPUSet) {
	if g == nil {
		return
	}
	g.allowedEntering = make(map[DomainID]machine.CPUSet, len(desired))
	g.pending = make(map[DomainID]machine.CPUSet, len(desired))
	g.cleanupPending = make(map[DomainID]machine.CPUSet, len(desired))
	if snapshot == nil || convergenceID == "" || convergenceID != g.convergenceID {
		return
	}
	owned := machine.NewCPUSet()
	for _, cpus := range snapshot.DomainUnion {
		owned = owned.Union(cpus)
	}
	unowned := g.allowedCPUs.Difference(owned)
	for _, cpu := range unowned.ToSliceInt() {
		var destination DomainID
		count := 0
		for domain, target := range desired {
			if target.Contains(cpu) {
				destination = domain
				count++
			}
		}
		if count == 1 {
			safe := machine.NewCPUSet(cpu)
			g.allowedEntering[destination] = g.allowedEntering[destination].Union(safe)
		}
	}
	for _, witness := range g.witnesses {
		if witness.Destination == "" || witness.Source == witness.Destination {
			continue
		}
		if !ValidateReleaseWitness(witness, convergenceID, witness.Source, witness.Destination, snapshot) {
			continue
		}
		heldOutsideDestination := machine.NewCPUSet()
		for domain, cpus := range snapshot.DomainUnion {
			if domain != witness.Destination {
				heldOutsideDestination = heldOutsideDestination.Union(cpus)
			}
		}
		g.allowedEntering[witness.Destination] = g.allowedEntering[witness.Destination].Union(
			witness.CPUs.Difference(heldOutsideDestination).
				Intersection(desired[witness.Destination]).Intersection(g.allowedCPUs))
	}
	g.recomputePending(snapshot, desired)
}

// AllowedEntering returns the set of CPUs the gate authorizes to enter domain
// on this convergence. The returned set is a clone; mutating it never affects
// the gate.
func (g *DomainGate) AllowedEntering(domain DomainID) machine.CPUSet {
	if g == nil {
		return machine.NewCPUSet()
	}
	return g.allowedEntering[domain].Clone()
}

// AllowedGrowTarget clamps a desired grow target to the gate-authorized set.
//
// Contract: the grow delta (desired − observed) may only contain CPUs that are
// (a) explicitly unowned (belonging to exactly one desired domain) or
// (b) released by a valid ReleaseWitness from another domain. Any other CPU in
// the desired delta is silently dropped, NOT rejected. The caller is expected
// to surface pending/blocked CPUs separately; the gate itself never errors.
//
// Tradeoff: silent clamping vs. loud rejection. Clamping keeps convergence
// progressing on a partial target; rejection would stall the entire round on
// one blocked CPU. The pending set (recomputePending) tracks what was clamped
// so diagnostics can report it.
func (g *DomainGate) AllowedGrowTarget(domain DomainID, desired, observed machine.CPUSet) machine.CPUSet {
	if g == nil {
		return observed.Clone()
	}
	return observed.Union(desired.Difference(observed).Intersection(g.allowedEntering[domain]))
}

func (g *DomainGate) recomputePending(snapshot *CompleteSnapshot, desired map[DomainID]machine.CPUSet) {
	if g == nil || snapshot == nil {
		return
	}
	// Observed ownership must follow semantic evidence (OwnershipByRel), not the
	// physical DomainUnion which retains dormant v1 containment envelopes.
	semanticObserved := semanticOwnershipUnionByDomain(snapshot, snapshot.DomainByRel)
	for destination, target := range desired {
		pending := machine.NewCPUSet()
		for source, observed := range semanticObserved {
			if source == destination {
				continue
			}
			pending = pending.Union(observed.Difference(desired[source]).Intersection(target))
		}
		g.pending[destination] = pending.Difference(g.allowedEntering[destination])
		cleanup := semanticObserved[destination].Difference(target)
		for other, otherTarget := range desired {
			if other != destination {
				cleanup = cleanup.Difference(otherTarget)
			}
		}
		g.cleanupPending[destination] = cleanup
	}
}

func sourceBoundaryFingerprint(snapshot *CompleteSnapshot, source DomainID) string {
	if snapshot == nil {
		return ""
	}
	hash := sha256.New()
	writeHashString(hash, "bulkhead-source-boundary-v1")
	writeHashString(hash, string(source))
	for _, root := range snapshot.ScanBoundary.Roots {
		if snapshotRelBelongsToDomain(snapshot, root, source) {
			writeHashString(hash, root)
		}
	}
	return string(hash.Sum(nil))
}

func sourceEvidenceID(snapshot *CompleteSnapshot, source DomainID) SnapshotID {
	if snapshot == nil {
		return SnapshotID{}
	}
	hash := sha256.New()
	writeHashString(hash, "bulkhead-source-evidence-v1")
	writeHashString(hash, string(source))
	rels := make([]string, 0)
	for rel := range snapshot.Entries {
		if snapshotRelBelongsToDomain(snapshot, rel, source) {
			rels = append(rels, rel)
		}
	}
	sort.Strings(rels)
	for _, rel := range rels {
		entry := snapshot.Entries[rel]
		writeHashString(hash, rel)
		writeHashUint64(hash, entry.Identity.Device)
		writeHashUint64(hash, entry.Identity.Inode)
		writeHashString(hash, entry.CPUs.String())
		writeHashString(hash, entry.Mems)
		for _, child := range snapshot.Children[rel] {
			writeHashString(hash, child.Name)
			writeHashUint64(hash, child.Identity.Device)
			writeHashUint64(hash, child.Identity.Inode)
		}
	}
	writeHashString(hash, snapshot.DomainUnion[source].String())
	var id SnapshotID
	copy(id[:], hash.Sum(nil))
	return id
}

func snapshotRelBelongsToDomain(snapshot *CompleteSnapshot, rel string, source DomainID) bool {
	if len(snapshot.DomainByRel) > 0 {
		return snapshot.DomainByRel[rel] == source
	}
	for _, root := range snapshot.ScanBoundary.Roots {
		if rel == root || len(rel) > len(root) && rel[:len(root)] == root && rel[len(root)] == '/' {
			return true
		}
	}
	return len(snapshot.ScanBoundary.Roots) == 0
}
