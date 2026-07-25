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

type transitionKind string

const (
	transitionNoop              transitionKind = "noop"
	transitionGrow              transitionKind = "grow"
	transitionShrink            transitionKind = "shrink"
	transitionIntersectReplace  transitionKind = "intersect_replace"
	transitionDomainLocalBridge transitionKind = "domain_local_bridge"
	transitionCrossDomain       transitionKind = "cross_domain_transfer"
)

type nodeTransition struct {
	node     *TopoNode
	domain   cpusetDomain
	kind     transitionKind
	observed machine.CPUSet
	target   machine.CPUSet

	entering machine.CPUSet
	leaving  machine.CPUSet

	crossDomainEntering machine.CPUSet
	crossDomainLeaving  machine.CPUSet
	domainLocalEntering machine.CPUSet

	bridgeTarget machine.CPUSet
}

type transitionPlan struct {
	byRel map[string]nodeTransition

	drainReclaimToPrimary []nodeTransition
	expandPrimary         []nodeTransition
	drainPrimaryToReclaim []nodeTransition
	expandReclaim         []nodeTransition
	cleanupPrimary        []nodeTransition
	cleanupReclaim        []nodeTransition
}

func buildTransitionPlan(dag *TopoDAG, snapshot domainSnapshot, gate domainGate) transitionPlan {
	plan := transitionPlan{byRel: map[string]nodeTransition{}}
	for _, node := range dag.Nodes() {
		domain := domainOf(node.Role)
		if domain == cpusetDomainUnknown {
			continue
		}
		observed := snapshot.observedByRel[node.Rel]
		target := snapshot.targetByRel[node.Rel]
		t := classifyNodeTransition(node, domain, observed, target, snapshot, gate)
		plan.byRel[node.Rel] = t
		plan.appendPhase(t)
	}
	return plan
}

func classifyNodeTransition(node *TopoNode, domain cpusetDomain, observed, target machine.CPUSet, snapshot domainSnapshot, gate domainGate) nodeTransition {
	t := nodeTransition{
		node:     node,
		domain:   domain,
		observed: observed.Clone(),
		target:   target.Clone(),
		entering: target.Difference(observed),
		leaving:  observed.Difference(target),
	}
	switch domain {
	case cpusetDomainPrimary:
		t.crossDomainEntering = t.entering.Intersection(snapshot.observedReclaimDomain)
		t.crossDomainLeaving = t.leaving.Intersection(snapshot.targetReclaimDomain)
		t.domainLocalEntering = t.entering.
			Difference(snapshot.observedReclaimDomain).
			Intersection(snapshot.allowedCPUs.Union(gate.safeUnownedToPrimary).Union(observed))
	case cpusetDomainReclaim:
		t.crossDomainEntering = t.entering.Intersection(snapshot.observedPrimaryDomain)
		t.crossDomainLeaving = t.leaving.Intersection(snapshot.targetPrimaryDomain)
		t.domainLocalEntering = t.entering.
			Difference(snapshot.observedPrimaryDomain).
			Intersection(snapshot.allowedCPUs.Union(gate.safeUnownedToReclaim).Union(observed))
	}
	t.kind = classifyTransitionKind(node, t, snapshot)
	if t.kind == transitionDomainLocalBridge {
		t.bridgeTarget = observed.Union(target)
	}
	return t
}

func classifyTransitionKind(node *TopoNode, t nodeTransition, snapshot domainSnapshot) transitionKind {
	if t.observed.Equals(t.target) {
		return transitionNoop
	}
	if !t.crossDomainEntering.IsEmpty() || !t.crossDomainLeaving.IsEmpty() {
		return transitionCrossDomain
	}
	if t.target.IsSubsetOf(t.observed) {
		return transitionShrink
	}
	if t.observed.IsSubsetOf(t.target) {
		return transitionGrow
	}
	if !t.observed.Intersection(t.target).IsEmpty() {
		return transitionIntersectReplace
	}
	if allowDomainLocalBridge(node, t, snapshot) {
		return transitionDomainLocalBridge
	}
	return transitionCrossDomain
}

func allowDomainLocalBridge(node *TopoNode, t nodeTransition, snapshot domainSnapshot) bool {
	if node == nil || t.domain == cpusetDomainUnknown || !t.crossDomainEntering.IsEmpty() || !t.crossDomainLeaving.IsEmpty() {
		return false
	}
	switch node.Role {
	case TopoNodeRoleReclaimNUMABucket:
		parent := parentNodeOf(node)
		if parent == nil {
			return false
		}
		parentTarget := snapshot.targetByRel[parent.Rel]
		return t.target.IsSubsetOf(parentTarget)
	default:
		return false
	}
}

func (p *transitionPlan) appendPhase(t nodeTransition) {
	if t.kind == transitionNoop {
		return
	}
	switch t.domain {
	case cpusetDomainPrimary:
		if !t.crossDomainLeaving.IsEmpty() {
			p.drainPrimaryToReclaim = append(p.drainPrimaryToReclaim, t)
		}
		if !t.entering.IsEmpty() {
			p.expandPrimary = append(p.expandPrimary, t)
		}
		if !t.leaving.Difference(t.crossDomainLeaving).IsEmpty() {
			p.cleanupPrimary = append(p.cleanupPrimary, t)
		}
	case cpusetDomainReclaim:
		if !t.crossDomainLeaving.IsEmpty() {
			p.drainReclaimToPrimary = append(p.drainReclaimToPrimary, t)
		}
		if !t.entering.IsEmpty() {
			p.expandReclaim = append(p.expandReclaim, t)
		}
		if !t.leaving.Difference(t.crossDomainLeaving).IsEmpty() {
			p.cleanupReclaim = append(p.cleanupReclaim, t)
		}
	}
}
