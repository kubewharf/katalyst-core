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
	"context"
	"fmt"

	"k8s.io/klog/v2"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const defaultDomainPipelineMaxRounds = 8

type domainPhasePipeline struct {
	dag          *TopoDAG
	cg           cgroupclient.CgroupClient
	targetByRel  map[string]machine.CPUSet
	cpuDetails   machine.CPUDetails
	reservedCPUs machine.CPUSet
	cache        *applyCache
	gate         domainGate
	gateReady    bool
	round        int
	maxRounds    int
}

type domainPhaseRound struct {
	index    int
	snapshot domainSnapshot
	gate     domainGate
	plan     transitionPlan
}

type domainPhaseExecutor struct {
	writer safeCPSetWriter
}

type domainDrainResult struct {
	fromDomain cpusetDomain
	release    machine.CPUSet
}

func newDomainPhaseExecutor(writer safeCPSetWriter) domainPhaseExecutor {
	return domainPhaseExecutor{writer: writer}
}

func (e domainPhaseExecutor) executeDrainPhase(fromDomain cpusetDomain, transitions []nodeTransition) (domainDrainResult, error) {
	result := domainDrainResult{
		fromDomain: fromDomain,
		release:    machine.NewCPUSet(),
	}
	for _, transition := range transitions {
		if transition.node == nil || transition.domain != fromDomain || transition.crossDomainLeaving.IsEmpty() {
			continue
		}
		// Drain only removes the CPUs that are handed to the sibling domain
		// (crossDomainLeaving). Cross-domain entering CPUs must not be pulled in
		// here: at drain time the parent is still mid-shrink and does not yet
		// contain them, so writing the final target would violate the cgroup v1
		// parent-subset constraint (EACCES). Entering CPUs are added later by
		// the expand phase, after the source domain publishes released CPUs and
		// the parent bridge is grown first.
		drainTarget := transition.observed.Difference(transition.crossDomainLeaving)
		if drainTarget.Equals(transition.observed) {
			continue
		}
		if drainTarget.IsEmpty() && e.writer.cg.Version(e.writer.ctx) == cgroupclient.CgroupVersionV1 {
			continue
		}
		logTransitionTarget("drain", transition, drainTarget, e.writer.targetByRel)
		if err := e.writer.shrinkParentWithLiveChildUnion(transition.node, drainTarget); err != nil {
			return result, err
		}
		result.release = result.release.Union(transition.crossDomainLeaving)
	}
	return result, nil
}

func (e domainPhaseExecutor) executeExpandPhase(transitions []nodeTransition, gate domainGate) error {
	for _, transition := range transitions {
		if transition.node == nil || transition.entering.IsEmpty() {
			continue
		}
		target := gate.allowedGrowTarget(transition.domain, transition.target, transition.observed)
		if target.Equals(transition.observed) {
			continue
		}
		logTransitionTarget("expand", transition, target, e.writer.targetByRel)
		if err := e.writer.growNodeWithParentBridge(transition.node, target); err != nil {
			return err
		}
	}
	return nil
}

func logTransitionTarget(phase string, transition nodeTransition, phaseTarget machine.CPUSet, targetByRel map[string]machine.CPUSet) {
	if transition.node == nil || !klog.V(4).Enabled() {
		return
	}
	targetByRelValue := "<missing>"
	if target, ok := targetByRel[transition.node.Rel]; ok {
		targetByRelValue = target.String()
	}
	parentRel := ""
	if parent := parentNodeOf(transition.node); parent != nil {
		parentRel = parent.Rel
	}
	general.InfofV(4, "topo_dag_writer: phase_target phase=%s rel=%q role=%v parent=%q domain=%s observed=%s target=%s phaseTarget=%s leaving=%s entering=%s crossDomainLeaving=%s targetByRel=%s nodeCPUs=%s metadata=%v",
		phase, transition.node.Rel, transition.node.Role, parentRel, transition.domain, transition.observed.String(), transition.target.String(), phaseTarget.String(),
		transition.leaving.String(), transition.entering.String(), transition.crossDomainLeaving.String(), targetByRelValue, transition.node.CPUs.String(), transition.node.Metadata)
}

func newDomainPhasePipeline(dag *TopoDAG, cg cgroupclient.CgroupClient, targetByRel map[string]machine.CPUSet, cpuDetails machine.CPUDetails, reservedCPUs machine.CPUSet, cache *applyCache) *domainPhasePipeline {
	return &domainPhasePipeline{
		dag:          dag,
		cg:           cg,
		targetByRel:  cloneCPUSetMap(targetByRel),
		cpuDetails:   cpuDetails,
		reservedCPUs: reservedCPUs.Clone(),
		cache:        cache,
		maxRounds:    defaultDomainPipelineMaxRounds,
	}
}

func (p *domainPhasePipeline) nextRound(ctx context.Context) (domainPhaseRound, error) {
	if p.round >= p.maxRounds {
		return domainPhaseRound{}, fmt.Errorf("domain phase pipeline exceeded max rounds: %d", p.maxRounds)
	}
	snapshot, err := buildDomainSnapshot(ctx, p.cg, p.dag, p.targetByRel, p.cpuDetails, p.reservedCPUs, p.cache)
	if err != nil {
		return domainPhaseRound{}, err
	}
	if !p.gateReady {
		p.gate = newDomainGate(snapshot)
		p.gateReady = true
	} else {
		p.gate.recomputePending(snapshot)
	}
	round := domainPhaseRound{
		index:    p.round,
		snapshot: snapshot,
		gate:     p.gate,
		plan:     buildTransitionPlan(p.dag, snapshot, p.gate),
	}
	p.round++
	return round, nil
}

// A transfer first drains CPUs from nodes that must shrink, then publishes the
// released CPUs for the receiving domain to expand. This ordering avoids
// assigning the same CPUs to sibling domains during the transition and keeps
// intermediate parent/child cpusets within their current constraints.
func (p *domainPhasePipeline) executeTransferCycle(ctx context.Context, defaultMems string, res *DAGApplyResult) error {
	executor := newDomainPhaseExecutor(newSafeCPUSetWriterForDAG(ctx, p.cg, p.dag, p.targetByRel, defaultMems, res).withCPUDetails(p.cpuDetails))

	round, err := p.nextRound(ctx)
	if err != nil {
		return err
	}
	reclaimDrain, err := executor.executeDrainPhase(cpusetDomainReclaim, round.plan.drainReclaimToPrimary)
	if err != nil {
		return err
	}
	if !reclaimDrain.release.IsEmpty() {
		refreshed, err := p.nextRound(ctx)
		if err != nil {
			return err
		}
		p.gate.publishReleased(cpusetDomainReclaim, reclaimDrain.release, refreshed.snapshot)
		if err := executor.executeExpandPhase(refreshed.plan.expandPrimary, p.gate); err != nil {
			return err
		}
	}

	round, err = p.nextRound(ctx)
	if err != nil {
		return err
	}
	primaryDrain, err := executor.executeDrainPhase(cpusetDomainPrimary, round.plan.drainPrimaryToReclaim)
	if err != nil {
		return err
	}
	if !primaryDrain.release.IsEmpty() {
		refreshed, err := p.nextRound(ctx)
		if err != nil {
			return err
		}
		p.gate.publishReleased(cpusetDomainPrimary, primaryDrain.release, refreshed.snapshot)
		if err := executor.executeExpandPhase(refreshed.plan.expandReclaim, p.gate); err != nil {
			return err
		}
	}
	return nil
}
