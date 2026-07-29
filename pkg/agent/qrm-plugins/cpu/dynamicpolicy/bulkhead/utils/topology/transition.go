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

type nodeTransition struct {
	node     *TopoNode
	domain   cpusetDomain
	observed machine.CPUSet
	target   machine.CPUSet

	entering machine.CPUSet

	crossDomainEntering machine.CPUSet
	crossDomainLeaving  machine.CPUSet
}

type transitionPlan struct {
	drainReclaimToPrimary []nodeTransition
	expandPrimary         []nodeTransition
	drainPrimaryToReclaim []nodeTransition
	expandReclaim         []nodeTransition
}

func buildTransitionPlan(dag *TopoDAG, snapshot domainSnapshot) transitionPlan {
	plan := transitionPlan{}
	for _, node := range dag.Nodes() {
		domain := domainOf(node.Role)
		if domain == cpusetDomainUnknown {
			continue
		}
		observed := snapshot.observedByRel[node.Rel]
		target := snapshot.targetByRel[node.Rel]
		t := classifyNodeTransition(node, domain, observed, target, snapshot)
		plan.appendPhase(t)
	}
	return plan
}

func classifyNodeTransition(node *TopoNode, domain cpusetDomain, observed, target machine.CPUSet, snapshot domainSnapshot) nodeTransition {
	t := nodeTransition{
		node:     node,
		domain:   domain,
		observed: observed.Clone(),
		target:   target.Clone(),
		entering: target.Difference(observed),
	}
	leaving := observed.Difference(target)
	switch domain {
	case cpusetDomainPrimary:
		t.crossDomainEntering = t.entering.Intersection(snapshot.observedReclaimDomain)
		t.crossDomainLeaving = leaving.Intersection(snapshot.targetReclaimDomain)
	case cpusetDomainReclaim:
		t.crossDomainEntering = t.entering.Intersection(snapshot.observedPrimaryDomain)
		t.crossDomainLeaving = leaving.Intersection(snapshot.targetPrimaryDomain)
	}
	return t
}

func (p *transitionPlan) appendPhase(t nodeTransition) {
	switch t.domain {
	case cpusetDomainPrimary:
		if !t.crossDomainLeaving.IsEmpty() {
			p.drainPrimaryToReclaim = append(p.drainPrimaryToReclaim, t)
		}
		if !t.entering.IsEmpty() {
			p.expandPrimary = append(p.expandPrimary, t)
		}
	case cpusetDomainReclaim:
		if !t.crossDomainLeaving.IsEmpty() {
			p.drainReclaimToPrimary = append(p.drainReclaimToPrimary, t)
		}
		if !t.entering.IsEmpty() {
			p.expandReclaim = append(p.expandReclaim, t)
		}
	}
}
