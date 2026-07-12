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
	apierrors "k8s.io/apimachinery/pkg/util/errors"
)

const CPUSetTopologyPluginName = "cpuset_topology"

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

func (p *CPUSetTopologyPlugin) Enable(conf *dynamicconfig.Configuration) bool {
	return enableBulkheadCpusetTopology(conf)
}

func (p *CPUSetTopologyPlugin) CPUSetAdjustmentHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	if in.View == nil {
		return nil
	}
	if in.View.ReclaimEffective.IsEmpty() {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "empty_reclaim")
		return nil
	}

	relExists := func(rel string) error {
		_, err := p.cgroup.StatDir(ctx, rel)
		return err
	}
	siblings := p.discoverBulkheadReclaimSiblings(ctx, in.View)
	specs := bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, in.View, siblings, relExists)
	dag, err := topology.BuildDAG(specs)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", 0, "dag_error")
		return fmt.Errorf("build bulkhead topology dag: %w", err)
	}

	expected := p.buildExpectedCPUSetByRel(in)
	_, err = topology.ApplyDAGDiff(ctx, topology.DAGApplyInputs{
		DAG:                 dag,
		Cgroup:              p.cgroup,
		ExpectedCPUSetByRel: expected,
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

func (p *CPUSetTopologyPlugin) PeriodicalHandler(
	ctx context.Context,
	in bulkheadapi.PeriodicalHandlerContext,
) error {
	if in.DynamicConf == nil || !enableBulkheadCpusetTopology(in.DynamicConf) {
		return nil
	}
	if p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV1 {
		if err := p.cgroup.ApplySchedLoadBalance(ctx, "", false); err != nil {
			return fmt.Errorf("apply root cpuset.sched_load_balance=0: %w", err)
		}
		return nil
	}

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
		if err := p.cgroup.ApplyCPUSetPartition(ctx, rel, cgcommon.CPUSetPartitionFlagRoot); err != nil {
			if errors.Is(err, cgcommon.ErrNotSupported) {
				general.InfofV(4, "bulkhead: cpuset partition not supported, skipping, rel=%q", rel)
				continue
			}
			errs = append(errs, fmt.Errorf("apply cpuset.cpus.partition=root @ %s: %w", rel, err))
			continue
		}
	}
	return apierrors.NewAggregate(errs)
}

func (p *CPUSetTopologyPlugin) buildExpectedCPUSetByRel(in bulkheadapi.HandlerContext) map[string]machine.CPUSet {
	if in.MetaServer == nil || in.View == nil || len(in.View.ContainerCPUSetByPod) == 0 {
		return nil
	}
	out := map[string]machine.CPUSet{}
	for podUID, containers := range in.View.ContainerCPUSetByPod {
		for containerName, cpus := range containers {
			rel, err := bulkheadutils.ResolveContainerRelPath(in.MetaServer, podUID, containerName)
			if err != nil {
				general.InfofV(5, "bulkhead: resolve container rel failed, skipping enforce, pod=%q container=%q err=%v",
					podUID, containerName, err)
				continue
			}
			if rel == "" {
				continue
			}
			out[rel] = cpus
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (p *CPUSetTopologyPlugin) discoverBulkheadReclaimSiblings(ctx context.Context, view *bulkheadutils.CPUSetPartitionView) []string {
	if !p.cfg.EnableBulkheadReclaimSiblings || p.cgroup.Version(ctx) != cgroupclient.CgroupVersionV1 {
		return nil
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
			general.ErrorS(err, "bulkhead: list reclaim sibling parent failed", "parentRel", parentRel)
			continue
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
	return out
}

func enableBulkheadCpusetTopology(conf *dynamicconfig.Configuration) bool {
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
