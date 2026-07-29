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

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

type domainGate struct {
	releasedToPrimary machine.CPUSet
	releasedToReclaim machine.CPUSet

	pendingToPrimary machine.CPUSet
	pendingToReclaim machine.CPUSet

	cleanupPendingPrimary machine.CPUSet
	cleanupPendingReclaim machine.CPUSet

	safeUnownedToPrimary machine.CPUSet
	safeUnownedToReclaim machine.CPUSet
}

func newDomainGate(snapshot domainSnapshot) domainGate {
	gate := domainGate{
		releasedToPrimary:     machine.NewCPUSet(),
		releasedToReclaim:     machine.NewCPUSet(),
		safeUnownedToPrimary:  machine.NewCPUSet(),
		safeUnownedToReclaim:  machine.NewCPUSet(),
		pendingToPrimary:      machine.NewCPUSet(),
		pendingToReclaim:      machine.NewCPUSet(),
		cleanupPendingPrimary: machine.NewCPUSet(),
		cleanupPendingReclaim: machine.NewCPUSet(),
	}
	gate.recomputePending(snapshot)
	return gate
}

func (g *domainGate) recomputePending(snapshot domainSnapshot) {
	primaryLeaving := snapshot.observedPrimaryDomain.Difference(snapshot.targetPrimaryDomain)
	reclaimLeaving := snapshot.observedReclaimDomain.Difference(snapshot.targetReclaimDomain)

	g.pendingToPrimary = reclaimLeaving.Intersection(snapshot.targetPrimaryDomain)
	g.pendingToReclaim = primaryLeaving.Intersection(snapshot.targetReclaimDomain)
	g.cleanupPendingPrimary = primaryLeaving.
		Difference(snapshot.targetReclaimDomain)
	g.cleanupPendingReclaim = reclaimLeaving.
		Difference(snapshot.targetPrimaryDomain)
	g.safeUnownedToPrimary = snapshot.safeUnownedToPrimary()
	g.safeUnownedToReclaim = snapshot.safeUnownedToReclaim()
}

func (g *domainGate) publishReleased(fromDomain cpusetDomain, releaseBatch machine.CPUSet, snapshot domainSnapshot) machine.CPUSet {
	var stillOwned machine.CPUSet
	switch fromDomain {
	case cpusetDomainPrimary:
		stillOwned = snapshot.observedPrimaryDomain
	case cpusetDomainReclaim:
		stillOwned = snapshot.observedReclaimDomain
	default:
		return machine.NewCPUSet()
	}
	actuallyReleased := releaseBatch.Difference(stillOwned)
	switch fromDomain {
	case cpusetDomainPrimary:
		g.releasedToReclaim = g.releasedToReclaim.Union(actuallyReleased.Intersection(snapshot.targetReclaimDomain))
	case cpusetDomainReclaim:
		g.releasedToPrimary = g.releasedToPrimary.Union(actuallyReleased.Intersection(snapshot.targetPrimaryDomain))
	}
	g.recomputePending(snapshot)
	return actuallyReleased
}

func (g *domainGate) allowedGrowTarget(domain cpusetDomain, desired machine.CPUSet, observed machine.CPUSet) machine.CPUSet {
	switch domain {
	case cpusetDomainPrimary:
		allowed := observed.Union(g.releasedToPrimary).Union(g.safeUnownedToPrimary)
		return desired.Intersection(allowed).Difference(g.pendingToPrimary)
	case cpusetDomainReclaim:
		allowed := observed.Union(g.releasedToReclaim).Union(g.safeUnownedToReclaim)
		return desired.Intersection(allowed).Difference(g.pendingToReclaim)
	default:
		return machine.NewCPUSet()
	}
}
