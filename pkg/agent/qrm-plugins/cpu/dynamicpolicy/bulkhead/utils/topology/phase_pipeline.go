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
	"strings"

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
	crossDomain  bool
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
	writer := e.writer.withTargetByRel(e.drainTargetByRel(fromDomain, transitions))
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
		logTransitionTarget("drain", transition, drainTarget, writer.targetByRel)
		if err := writer.shrinkParentWithLiveChildUnion(transition.node, drainTarget); err != nil {
			if IsDeferConvergenceError(err) {
				if writer.res != nil {
					writer.res.Deferred++
				}
				continue
			}
			return result, err
		}
		result.release = result.release.Union(transition.crossDomainLeaving)
	}
	return result, nil
}

func (e domainPhaseExecutor) drainTargetByRel(fromDomain cpusetDomain, transitions []nodeTransition) map[string]machine.CPUSet {
	targets := cloneCPUSetMap(e.writer.targetByRel)
	explicit := map[string]struct{}{}
	for _, transition := range transitions {
		if transition.node == nil || transition.domain != fromDomain || transition.crossDomainLeaving.IsEmpty() {
			continue
		}
		for rel := range e.writer.controlledByRel {
			if rel == transition.node.Rel || !isDescendantRel(transition.node.Rel, rel) {
				continue
			}
			if _, ok := explicit[rel]; ok {
				continue
			}
			current, err := e.writer.cg.ReadCPUSet(e.writer.ctx, rel)
			if err == nil {
				targets[rel] = current
			}
		}
		drainTarget := transition.observed.Difference(transition.crossDomainLeaving)
		if drainTarget.IsEmpty() && e.writer.cg.Version(e.writer.ctx) == cgroupclient.CgroupVersionV1 {
			drainTarget = transition.observed
		}
		targets[transition.node.Rel] = drainTarget
		explicit[transition.node.Rel] = struct{}{}
	}
	return targets
}

func isDescendantRel(parentRel, childRel string) bool {
	parentRel = strings.Trim(parentRel, "/")
	childRel = strings.Trim(childRel, "/")
	return parentRel != "" && strings.HasPrefix(childRel, parentRel+"/")
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
		if !transition.observed.IsSubsetOf(target) {
			if err := e.writer.shrinkParentWithLiveChildUnion(transition.node, target); err != nil {
				if IsDeferConvergenceError(err) {
					continue
				}
				return err
			}
			continue
		}
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
	leaving := transition.observed.Difference(transition.target)
	general.InfofV(4, "topo_dag_writer: phase_target phase=%s rel=%q role=%v parent=%q domain=%s observed=%s target=%s phaseTarget=%s leaving=%s entering=%s crossDomainLeaving=%s targetByRel=%s nodeCPUs=%s metadata=%v",
		phase, transition.node.Rel, transition.node.Role, parentRel, transition.domain, transition.observed.String(), transition.target.String(), phaseTarget.String(),
		leaving.String(), transition.entering.String(), transition.crossDomainLeaving.String(), targetByRelValue, transition.node.CPUs.String(), transition.node.Metadata)
}

func logTransferCycleRound(stage string, round domainPhaseRound) {
	general.Infof("topo_dag_writer: transfer_cycle round=%d stage=%s observedPrimaryDomain=%s targetPrimaryDomain=%s observedReclaimDomain=%s targetReclaimDomain=%s unowned=%s safeUnownedToPrimary=%s safeUnownedToReclaim=%s pendingToPrimary=%s pendingToReclaim=%s releasedToPrimary=%s releasedToReclaim=%s cleanupPendingPrimary=%s cleanupPendingReclaim=%s drainReclaimToPrimary=%s expandPrimary=%s drainPrimaryToReclaim=%s expandReclaim=%s",
		round.index, stage,
		round.snapshot.observedPrimaryDomain.String(),
		round.snapshot.targetPrimaryDomain.String(),
		round.snapshot.observedReclaimDomain.String(),
		round.snapshot.targetReclaimDomain.String(),
		round.snapshot.unownedCPUs().String(),
		round.snapshot.safeUnownedToPrimary().String(),
		round.snapshot.safeUnownedToReclaim().String(),
		round.gate.pendingToPrimary.String(),
		round.gate.pendingToReclaim.String(),
		round.gate.releasedToPrimary.String(),
		round.gate.releasedToReclaim.String(),
		round.gate.cleanupPendingPrimary.String(),
		round.gate.cleanupPendingReclaim.String(),
		summarizeTransitions(round.plan.drainReclaimToPrimary),
		summarizeTransitions(round.plan.expandPrimary),
		summarizeTransitions(round.plan.drainPrimaryToReclaim),
		summarizeTransitions(round.plan.expandReclaim))
}

func logTransferCyclePublish(round domainPhaseRound, fromDomain cpusetDomain, releaseBatch, actuallyReleased machine.CPUSet) {
	stillOwned := machine.NewCPUSet()
	switch fromDomain {
	case cpusetDomainPrimary:
		stillOwned = round.snapshot.observedPrimaryDomain
	case cpusetDomainReclaim:
		stillOwned = round.snapshot.observedReclaimDomain
	}
	general.Infof("topo_dag_writer: transfer_cycle_publish round=%d fromDomain=%s releaseBatch=%s stillOwned=%s actuallyReleased=%s observedPrimaryDomain=%s observedReclaimDomain=%s targetPrimaryDomain=%s targetReclaimDomain=%s pendingToPrimary=%s pendingToReclaim=%s releasedToPrimary=%s releasedToReclaim=%s",
		round.index, fromDomain, releaseBatch.String(), stillOwned.String(), actuallyReleased.String(),
		round.snapshot.observedPrimaryDomain.String(),
		round.snapshot.observedReclaimDomain.String(),
		round.snapshot.targetPrimaryDomain.String(),
		round.snapshot.targetReclaimDomain.String(),
		round.gate.pendingToPrimary.String(),
		round.gate.pendingToReclaim.String(),
		round.gate.releasedToPrimary.String(),
		round.gate.releasedToReclaim.String())
}

func summarizeTransitions(transitions []nodeTransition) string {
	if len(transitions) == 0 {
		return "none"
	}
	out := ""
	for i, transition := range transitions {
		if transition.node == nil {
			continue
		}
		if out != "" {
			out += ";"
		}
		leaving := transition.observed.Difference(transition.target)
		out += fmt.Sprintf("%d:%s domain=%s observed=%s target=%s leaving=%s entering=%s crossLeaving=%s crossEntering=%s",
			i, transition.node.Rel, transition.domain,
			transition.observed.String(), transition.target.String(),
			leaving.String(), transition.entering.String(),
			transition.crossDomainLeaving.String(), transition.crossDomainEntering.String())
	}
	if out == "" {
		return "none"
	}
	return out
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
	// Each transfer-cycle round is a new safety gate. Dynamic kubelet children
	// can be created between drain and publish, so child discovery must be fresh
	// for every gate snapshot; otherwise a cached empty child list can hide a new
	// primary owner and publish the same CPU to reclaim.
	p.cache.resetChildren()
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
		plan:     buildTransitionPlan(p.dag, snapshot),
	}
	if planHasCrossDomainTransfer(round.plan) {
		p.crossDomain = true
	}
	p.round++
	return round, nil
}

func (p *domainPhasePipeline) constrainedTargets() map[string]machine.CPUSet {
	out := cloneCPUSetMap(p.targetByRel)
	if !p.gateReady {
		return out
	}
	for _, node := range p.dag.Nodes() {
		switch domainOf(node.Role) {
		case cpusetDomainPrimary:
			out[node.Rel] = out[node.Rel].Difference(p.gate.pendingToPrimary)
		case cpusetDomainReclaim:
			out[node.Rel] = out[node.Rel].Difference(p.gate.pendingToReclaim)
		}
	}
	return out
}

func (p *domainPhasePipeline) shouldConstrainTargets() bool {
	if !p.gateReady || !p.crossDomain {
		return false
	}
	return !p.gate.pendingToPrimary.IsEmpty() || !p.gate.pendingToReclaim.IsEmpty()
}

func planHasCrossDomainTransfer(plan transitionPlan) bool {
	for _, transitions := range [][]nodeTransition{
		plan.drainReclaimToPrimary,
		plan.expandPrimary,
		plan.drainPrimaryToReclaim,
		plan.expandReclaim,
	} {
		for _, transition := range transitions {
			if !transition.crossDomainLeaving.IsEmpty() || !transition.crossDomainEntering.IsEmpty() {
				return true
			}
		}
	}
	return false
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
	logTransferCycleRound("drain_reclaim_to_primary", round)
	reclaimDrain, err := executor.executeDrainPhase(cpusetDomainReclaim, round.plan.drainReclaimToPrimary)
	if err != nil {
		return err
	}
	primaryDrainDone := false
	if !reclaimDrain.release.IsEmpty() {
		refreshed, err := p.nextRound(ctx)
		if err != nil {
			return err
		}
		actuallyReleased := p.gate.publishReleased(cpusetDomainReclaim, reclaimDrain.release, refreshed.snapshot)
		logTransferCyclePublish(refreshed, cpusetDomainReclaim, reclaimDrain.release, actuallyReleased)
		if !p.gate.pendingToReclaim.IsEmpty() {
			logTransferCycleRound("drain_primary_before_reclaim_expand", refreshed)
			primaryDrain, err := executor.executeDrainPhase(cpusetDomainPrimary, refreshed.plan.drainPrimaryToReclaim)
			if err != nil {
				return err
			}
			primaryDrainDone = true
			if !primaryDrain.release.IsEmpty() {
				refreshed, err = p.nextRound(ctx)
				if err != nil {
					return err
				}
				actuallyReleased := p.gate.publishReleased(cpusetDomainPrimary, primaryDrain.release, refreshed.snapshot)
				logTransferCyclePublish(refreshed, cpusetDomainPrimary, primaryDrain.release, actuallyReleased)
				logTransferCycleRound("expand_reclaim_after_primary_release", refreshed)
				if err := executor.executeExpandPhase(refreshed.plan.expandReclaim, p.gate); err != nil {
					return err
				}
			}
		}
		logTransferCycleRound("expand_primary_after_reclaim_release", refreshed)
		if err := executor.executeExpandPhase(refreshed.plan.expandPrimary, p.gate); err != nil {
			return err
		}
	}

	if primaryDrainDone {
		return nil
	}
	round, err = p.nextRound(ctx)
	if err != nil {
		return err
	}
	logTransferCycleRound("drain_primary_to_reclaim", round)
	primaryDrain, err := executor.executeDrainPhase(cpusetDomainPrimary, round.plan.drainPrimaryToReclaim)
	if err != nil {
		return err
	}
	if !primaryDrain.release.IsEmpty() {
		refreshed, err := p.nextRound(ctx)
		if err != nil {
			return err
		}
		actuallyReleased := p.gate.publishReleased(cpusetDomainPrimary, primaryDrain.release, refreshed.snapshot)
		logTransferCyclePublish(refreshed, cpusetDomainPrimary, primaryDrain.release, actuallyReleased)
		logTransferCycleRound("expand_reclaim_after_primary_release", refreshed)
		if err := executor.executeExpandPhase(refreshed.plan.expandReclaim, p.gate); err != nil {
			return err
		}
	}
	return nil
}
