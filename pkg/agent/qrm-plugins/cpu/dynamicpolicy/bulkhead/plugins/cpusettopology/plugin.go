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

package cpusettopology

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const CPUSetTopologyPluginName = "cpuset_topology"

var _ bulkheadapi.Plugin = (*CPUSetTopologyPlugin)(nil)

type CPUSetTopologyPlugin struct {
	cfg    bulkheadconfig.BulkheadConfiguration
	cgroup cgroupclient.CgroupClient
}

func NewCPUSetTopologyPlugin(conf *config.Configuration) bulkheadapi.Plugin {
	var cfg bulkheadconfig.BulkheadConfiguration
	if conf != nil && conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg = *conf.CPUQRMPluginConfig.BulkheadConfiguration
	}
	return &CPUSetTopologyPlugin{
		cfg:    cfg,
		cgroup: cgroupclient.NewCgroupClient(),
	}
}

func (p *CPUSetTopologyPlugin) Name() string { return CPUSetTopologyPluginName }

func (p *CPUSetTopologyPlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableBulkheadCpusetTopology(in)
}

func (p *CPUSetTopologyPlugin) CPUSetAdjustmentHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	if in.View == nil {
		return nil
	}
	relExists := func(rel string) error {
		_, err := p.cgroup.StatDir(ctx, rel)
		return err
	}
	siblings, err := p.discoverBulkheadReclaimSiblings(ctx, in.View)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "discover_error")
		return fmt.Errorf("discover bulkhead reclaim siblings: %w", err)
	}
	specs := bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, in.View, siblings, relExists)
	dag, err := topology.BuildDAG(specs)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "dag_error")
		return fmt.Errorf("build bulkhead topology dag: %w", err)
	}

	expectedRes, err := p.buildExpectedCPUSetByRel(ctx, in)
	if err != nil {
		// Only non-pending resolve failures (illegal rel, cgroup/metaserver
		// internal error) reach here. Pending containers (admit window, no
		// container id yet) are classified as protected-pending and do NOT
		// produce an error, so a normal new-pod admit is never rejected.
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "container_error")
		return fmt.Errorf("build expected container cpuset: %w", err)
	}
	general.InfofV(5, "cpuset_topology: apply start specs=%d siblings=%d expected_leaf_count=%d pending_count=%d protected_pending=%s",
		len(specs), len(siblings), len(expectedRes.ExpectedByRel), len(expectedRes.PendingByPod),
		expectedRes.PendingCPUSetUnion().String())
	_, err = topology.ApplyDAGDiff(ctx, topology.DAGApplyInputs{
		DAG:                    dag,
		Cgroup:                 p.cgroup,
		ExpectedCPUSetByRel:    expectedRes.ExpectedByRel,
		KubeManagedRelPrefix:   p.cfg.BulkheadPrimaryRelPath,
		ProtectedPendingCPUSet: expectedRes.PendingCPUSetUnion(),
	})
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "dag_error")
		return fmt.Errorf("apply bulkhead topology dag: %w", err)
	}

	activeRels := bulkheadutils.CollectActiveRels(p.cfg, in.View, in.MetaServer, siblings, relExists)
	p.cgroup.Prune(activeRels)
	emitBulkheadPruneResult(in.Emitter, "success", len(activeRels), "")
	return nil
}

func (p *CPUSetTopologyPlugin) CPUSetAdjustmentDisabledHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	return p.resetCPUSetTopology(ctx, in)
}

func (p *CPUSetTopologyPlugin) disabledResetCPUSet(ctx context.Context, in bulkheadapi.HandlerContext) (machine.CPUSet, error) {
	if p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV2 {
		return machine.NewCPUSet(), nil
	}
	if in.Topology == nil {
		return machine.CPUSet{}, fmt.Errorf("nil topology for v1 disabled cpuset reset")
	}
	target := in.Topology.CPUDetails.CPUs()
	if target.IsEmpty() {
		return machine.CPUSet{}, fmt.Errorf("empty machine cpuset for v1 disabled cpuset reset")
	}
	return target, nil
}

func (p *CPUSetTopologyPlugin) buildDisabledResetDAG(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	target machine.CPUSet,
) (*topology.TopoDAG, error) {
	relExists := func(rel string) error {
		_, err := p.cgroup.StatDir(ctx, rel)
		return err
	}

	siblings, err := p.discoverBulkheadReclaimSiblings(ctx, in.View)
	if err != nil {
		return nil, fmt.Errorf("discover bulkhead reclaim siblings: %w", err)
	}

	specs := bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, in.View, siblings, relExists)
	if len(specs) == 0 {
		return nil, nil
	}
	for i := range specs {
		specs[i].CPUs = target
		specs[i].Mems = ""
	}

	dag, err := topology.BuildDAG(specs)
	if err != nil {
		return nil, fmt.Errorf("build disabled reset topology dag: %w", err)
	}
	return dag, nil
}

func (p *CPUSetTopologyPlugin) resetCPUSetTopology(ctx context.Context, in bulkheadapi.HandlerContext) error {
	target, err := p.disabledResetCPUSet(ctx, in)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "reset_target_error")
		return err
	}

	// Reset (disabled transition) widens leaves back towards the machine/root
	// cpuset. It must stay fail-open: if a live container rel cannot be resolved
	// we still want to relax the (possibly polluted) leaf, otherwise a leaf stuck
	// on a stale transient-pool cpuset can never recover. For the same reason we
	// do NOT protect kube pod leaves here - widening a leaf is always safe.
	// Any classification error is intentionally ignored: reset must never be
	// blocked by a transient resolve failure.
	expectedRes, _ := p.buildExpectedCPUSetByRel(ctx, in)
	var expected map[string]machine.CPUSet
	if expectedRes != nil {
		expected = expectedRes.ExpectedByRel
	}

	dag, err := p.buildDisabledResetDAG(ctx, in, target)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "dag_error")
		return err
	}
	if dag == nil {
		emitBulkheadPruneResult(in.Emitter, "success", 0, "")
		return nil
	}

	res, err := topology.ApplyDAGDiff(ctx, topology.DAGApplyInputs{
		DAG:                 dag,
		Cgroup:              p.cgroup,
		SkipObservedRead:    true,
		ExpectedCPUSetByRel: expected,
	})
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", res.Applied, "dag_error")
		return fmt.Errorf("apply disabled reset topology dag: %w", err)
	}

	emitBulkheadPruneResult(in.Emitter, "success", res.Applied, "")
	return nil
}

func (p *CPUSetTopologyPlugin) PeriodicalHandler(
	ctx context.Context,
	in bulkheadapi.PeriodicalHandlerContext,
) error {
	enabled := enableBulkheadCpusetTopologyByDynamicConf(in.DynamicConf)
	if p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV1 {
		schedLoadBalance := !enabled
		if err := p.cgroup.ApplySchedLoadBalance(ctx, "", schedLoadBalance); err != nil {
			return fmt.Errorf("apply root cpuset.sched_load_balance=%t: %w", schedLoadBalance, err)
		}
		return nil
	}

	flag := cgcommon.CPUSetPartitionFlagMember
	if enabled {
		flag = cgcommon.CPUSetPartitionFlagRoot
	}
	return p.applyBulkheadPartitionFlag(ctx, flag)
}

func (p *CPUSetTopologyPlugin) applyBulkheadPartitionFlag(ctx context.Context, flag cgcommon.CPUSetPartitionFlag) error {
	var errs []error
	for _, rel := range p.cfg.BulkheadPartitionRelPaths {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			continue
		}
		if _, err := p.cgroup.StatDir(ctx, rel); err != nil {
			general.InfofV(4, "bulkhead: partition rel path does not exist, skipping, rel=%q err=%v", rel, err)
			continue
		}
		if err := p.cgroup.ApplyCPUSetPartition(ctx, rel, flag); err != nil {
			if errors.Is(err, cgcommon.ErrNotSupported) {
				general.InfofV(4, "bulkhead: cpuset partition not supported, skipping, rel=%q", rel)
				continue
			}
			errs = append(errs, fmt.Errorf("apply cpuset.cpus.partition=%s @ %s: %w", flag, rel, err))
			continue
		}
	}
	return apierrors.NewAggregate(errs)
}

// pendingContainerCPUSet records a container whose allocation already exists in
// QRM state but whose cgroup rel cannot be resolved yet (typically the pod
// admit window before kubelet/containerd creates the container). Its cpuset
// must still be treated as a protected descendant so the parent effective
// target never shrinks below it, but its leaf must NOT be written.
type pendingContainerCPUSet struct {
	PodUID        string
	ContainerName string
	CPUs          machine.CPUSet
	Reason        string
}

// expectedCPUSetBuildResult separates resolvable container leaves (ExpectedByRel,
// written precisely) from admit-pending containers (PendingByPod, protected but
// not written).
type expectedCPUSetBuildResult struct {
	ExpectedByRel map[string]machine.CPUSet
	PendingByPod  []pendingContainerCPUSet
}

// PendingCPUSetUnion returns the union of all pending container allocations. The
// writer folds this into the primary effective target so the parent cgroup never
// shrinks below an allocation whose leaf has not been created yet.
func (r *expectedCPUSetBuildResult) PendingCPUSetUnion() machine.CPUSet {
	out := machine.NewCPUSet()
	if r == nil {
		return out
	}
	for _, p := range r.PendingByPod {
		out = out.Union(p.CPUs)
	}
	return out
}

// isContainerNotCreatedErr reports whether a ResolveContainerRelPath error means
// the container simply has not been created yet (admit-safe pending), as opposed
// to a real internal error. A failure while resolving the container ID means
// kubelet/containerd has not created the container yet - the normal pod admit
// window - so it must NOT fail the round. A failure after the ID is known (for
// example resolving the relative cgroup path) is a real problem and is surfaced.
func isContainerNotCreatedErr(err error) bool {
	if err == nil {
		return false
	}
	var resolveErr *bulkheadutils.ContainerRelPathResolveError
	if errors.As(err, &resolveErr) {
		return resolveErr.Stage == bulkheadutils.ContainerRelPathResolveStageContainerID
	}
	// Backward-compatible fallback for callers/tests that may still wrap errors
	// with the old text-only context. Keep this intentionally narrow: a generic
	// "not found" from cgroup path resolution must stay fail-closed.
	return strings.Contains(err.Error(), "resolve container id:")
}

func (p *CPUSetTopologyPlugin) buildExpectedCPUSetByRel(_ context.Context, in bulkheadapi.HandlerContext) (*expectedCPUSetBuildResult, error) {
	if in.MetaServer == nil || in.View == nil || len(in.View.ContainerCPUSetByPod) == 0 {
		return &expectedCPUSetBuildResult{}, nil
	}
	out := &expectedCPUSetBuildResult{ExpectedByRel: map[string]machine.CPUSet{}}
	var errs []error
	for podUID, containers := range in.View.ContainerCPUSetByPod {
		for containerName, cpus := range containers {
			if cpus.IsEmpty() {
				continue
			}
			// Reuse ResolveContainerRelPath so that the rel-key format stays in sync
			// with everywhere else in bulkhead (BulkheadPrimaryRelPath,
			// BulkheadReclaimRelPaths, CollectActiveRels, controlledRels, and the
			// childRel constructed by writer.ApplyDAGDiff via filepath.Join(parent, name)).
			// ResolveContainerRelPath does the GetContainerID + GetContainerRelativeCgroupPath
			// lookup and, crucially, trims the leading "/" that
			// GetKubernetesAnyExistRelativeCgroupPath prepends. Without this trim, the
			// expected map key would never match the childRel that expandDescendants
			// produces during recursion, causing per-container cpuset enforcement to
			// silently degrade to inheriting the parent pool target.
			rel, err := bulkheadutils.ResolveContainerRelPath(in.MetaServer, podUID, containerName)
			if err != nil {
				if isContainerNotCreatedErr(err) {
					// admit-safe pending: state has the allocation but the container
					// cgroup does not exist yet. Do NOT fail (that would reject pod
					// admit); record it so the writer keeps the parent a superset.
					general.InfofV(5, "bulkhead: container rel pending, protecting allocation, pod=%q container=%q cpuset=%s err=%v",
						podUID, containerName, cpus.String(), err)
					out.PendingByPod = append(out.PendingByPod, pendingContainerCPUSet{
						PodUID: podUID, ContainerName: containerName, CPUs: cpus, Reason: err.Error(),
					})
					continue
				}
				// A real internal error (illegal rel, cgroup/metaserver failure):
				// block this round rather than apply a partial/wrong topology.
				errs = append(errs, fmt.Errorf("pod=%s container=%s cpuset=%s: %w",
					podUID, containerName, cpus.String(), err))
				continue
			}
			if rel == "" {
				errs = append(errs, fmt.Errorf("pod=%s container=%s cpuset=%s: empty relative cgroup path",
					podUID, containerName, cpus.String()))
				continue
			}
			out.ExpectedByRel[rel] = cpus
		}
	}
	if len(errs) > 0 {
		return nil, apierrors.NewAggregate(errs)
	}
	return out, nil
}

func (p *CPUSetTopologyPlugin) discoverBulkheadReclaimSiblings(ctx context.Context, view *bulkheadutils.CPUSetPartitionView) ([]string, error) {
	if !p.cfg.EnableBulkheadReclaimSiblings || p.cgroup.Version(ctx) != cgroupclient.CgroupVersionV1 {
		return nil, nil
	}

	excluded := map[string]struct{}{}
	addExcluded := func(rel string) {
		rel = strings.Trim(rel, "/")
		if rel != "" {
			excluded[rel] = struct{}{}
		}
	}
	addExcluded(p.cfg.BulkheadPrimaryRelPath)
	for _, rel := range p.cfg.BulkheadReclaimRelPaths {
		addExcluded(rel)
	}
	for _, rel := range p.cfg.BulkheadPartitionRelPaths {
		addExcluded(rel)
	}
	if view != nil {
		for reclaimIdx := range p.cfg.BulkheadReclaimRelPaths {
			for numaID := range view.ReclaimEffectivePerNUMA {
				addExcluded(p.cfg.ReclaimPerNUMA(reclaimIdx, numaID))
			}
		}
	}

	seen := map[string]struct{}{}
	var out []string
	for _, reclaimRel := range p.cfg.BulkheadReclaimRelPaths {
		reclaimRel = strings.Trim(reclaimRel, "/")
		if reclaimRel == "" {
			continue
		}
		parentRel := path.Dir(reclaimRel)
		if parentRel == "." {
			parentRel = ""
		}
		children, err := p.cgroup.ListChildren(ctx, parentRel)
		if err != nil {
			return nil, fmt.Errorf("list reclaim sibling parent %q: %w", parentRel, err)
		}
		for _, child := range children {
			rel := strings.Trim(path.Join(parentRel, child), "/")
			if rel == "" {
				continue
			}
			if _, skip := excluded[rel]; skip {
				continue
			}
			if p.isConfiguredReclaimNUMARel(rel) {
				continue
			}
			if _, ok := seen[rel]; ok {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

func enableBulkheadCpusetTopology(in bulkheadapi.HandlerContext) bool {
	if in.State != nil && in.State.GetAllowSharedCoresOverlapReclaimedCores() {
		return false
	}
	return enableBulkheadCpusetTopologyByDynamicConf(in.DynamicConf)
}

func enableBulkheadCpusetTopologyByDynamicConf(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology
}

func (p *CPUSetTopologyPlugin) isConfiguredReclaimNUMARel(rel string) bool {
	rel = strings.Trim(rel, "/")
	for _, prefix := range p.cfg.BulkheadReclaimNumaPrefixes {
		prefix = strings.Trim(prefix, "/")
		if prefix == "" || !strings.HasPrefix(rel, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(rel, prefix)
		if suffix == "" {
			continue
		}
		if _, err := strconv.Atoi(suffix); err == nil {
			return true
		}
	}
	return false
}

const metricBulkheadPruneResult = "bulkhead_prune_result"

func emitBulkheadPruneResult(emitter metrics.MetricEmitter, status string, activeRelsCount int, reason string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadPruneResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "active_rels_count", Val: strconv.Itoa(activeRelsCount)},
		metrics.MetricTag{Key: "reason", Val: reason},
	)
}
