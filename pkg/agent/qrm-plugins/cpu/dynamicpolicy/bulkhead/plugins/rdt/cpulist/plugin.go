/*
Copyright 2026 The Katalyst Authors.

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

package cpulist

import (
	"context"
	"errors"
	"fmt"
	"sort"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	qrmresctrlmanager "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
)

const CPUListPluginName = "rdt_cpulist"

var (
	_ bulkheadapi.Plugin            = (*CPUListPlugin)(nil)
	_ bulkheadapi.AdjustmentCapable = (*CPUListPlugin)(nil)
)

// Clos identifies a currently existing CLOS directory. Epoch changes when the
// directory identity changes, so an applied value for a deleted CLOS is never
// reused for a same-named replacement.
type Clos = qrmresctrlmanager.CPUListClos

// CPUListManager exposes only the read/write operations needed by this plugin.
// CLOS deletion stays outside this plugin; ApplyCPUList may create a missing
// CLOS only when applying a non-empty target.
type CPUListManager = qrmresctrlmanager.CPUListManager

type cpuListObserver interface {
	CPUListMatches(context.Context, string, string) (bool, error)
}

type appliedTarget struct {
	cpuSet string
}

type CPUListPlugin struct {
	config             *qrmresctrl.ResctrlConfig
	manager            CPUListManager
	capabilityProvider rdt.CATCapabilityProvider
	applied            map[Clos]appliedTarget
}

func NewCPUListPlugin(conf *config.Configuration) bulkheadapi.Plugin {
	var resctrlConfig *qrmresctrl.ResctrlConfig
	if conf != nil && conf.QRMPluginsConfiguration != nil {
		resctrlConfig = conf.QRMPluginsConfiguration.ResctrlConfig
	}
	return NewCPUListPluginWithManager(
		resctrlConfig,
		qrmresctrlmanager.NewCPUListManager(),
		rdt.NewCATCapabilityProvider(),
	)
}

func NewCPUListPluginWithManager(config *qrmresctrl.ResctrlConfig, manager CPUListManager, providers ...rdt.CATCapabilityProvider) *CPUListPlugin {
	if config == nil {
		config = qrmresctrl.NewResctrlConfig()
	}
	capabilityProvider := rdt.NewCATCapabilityProvider()
	if len(providers) > 0 {
		capabilityProvider = providers[0]
	}
	return &CPUListPlugin{
		config:             config,
		manager:            manager,
		capabilityProvider: capabilityProvider,
		applied:            make(map[Clos]appliedTarget),
	}
}

func (p *CPUListPlugin) Name() string { return CPUListPluginName }

func (p *CPUListPlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableCPUList(in.DynamicConf)
}

func (p *CPUListPlugin) CPUSetAdjustmentHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	if !catSupported(p.capabilityProvider) {
		return nil
	}
	if p.manager == nil {
		return nil
	}
	clos, err := p.manager.ListManagedClos(ctx)
	if err != nil {
		return fmt.Errorf("list managed CLOS: %w", err)
	}
	targetCPUSets := p.buildTargetCPUSets(in.View)
	if err := validateDisjointCPUListTargets(targetCPUSets); err != nil {
		return err
	}
	targets := formatCPUListTargets(targetCPUSets)
	active := make(map[Clos]struct{}, len(clos))
	existing := make(map[string]struct{}, len(clos))
	for _, current := range clos {
		if !p.isManaged(current.ID) {
			continue
		}
		existing[current.ID] = struct{}{}
		active[current] = struct{}{}
		if err := p.apply(ctx, current, targets[current.ID]); err != nil {
			return err
		}
	}
	for _, closID := range sortedTargetClosIDs(targets) {
		if !p.isManaged(closID) {
			continue
		}
		if _, ok := existing[closID]; ok {
			continue
		}
		current := Clos{ID: closID}
		active[current] = struct{}{}
		if err := p.apply(ctx, current, targets[closID]); err != nil {
			return err
		}
	}
	p.pruneApplied(active)
	return nil
}

func (p *CPUListPlugin) CPUSetAdjustmentDisabledHandler(ctx context.Context, _ bulkheadapi.HandlerContext) error {
	if !catSupported(p.capabilityProvider) {
		return nil
	}
	if p.manager == nil {
		return nil
	}
	clos, err := p.manager.ListManagedClos(ctx)
	if err != nil {
		return fmt.Errorf("list CLOS for disabled reset: %w", err)
	}
	active := make(map[Clos]struct{}, len(clos))
	for _, current := range clos {
		if !p.isManaged(current.ID) {
			continue
		}
		active[current] = struct{}{}
		if err := p.apply(ctx, current, ""); err != nil {
			return err
		}
	}
	p.pruneApplied(active)
	return nil
}

func (p *CPUListPlugin) buildTargetCPUSets(view *model.CPUSetPartitionView) map[string]machine.CPUSet {
	cpuSets := make(map[string]machine.CPUSet)
	if view == nil {
		return cpuSets
	}
	for pool, cpus := range view.SharePoolMap {
		if cpus.IsEmpty() {
			continue
		}
		closID := resctrlutil.ResolveSharedPoolClosID(pool, p.config)
		cpuSets[closID] = cpuSets[closID].Union(cpus)
	}
	if !view.Dedicated.IsEmpty() {
		cpuSets[consts.ResctrlGroupDedicated] = view.Dedicated
	}
	return cpuSets
}

func validateDisjointCPUListTargets(targets map[string]machine.CPUSet) error {
	closIDs := make([]string, 0, len(targets))
	for closID, cpus := range targets {
		if !cpus.IsEmpty() {
			closIDs = append(closIDs, closID)
		}
	}
	sort.Strings(closIDs)
	// Validate after pools have been grouped by CLOS. Overlap within one CLOS is
	// an intentional union, while overlap across final CLOS targets violates the
	// expected CPU ownership invariant.
	for i, leftID := range closIDs {
		for _, rightID := range closIDs[i+1:] {
			overlap := targets[leftID].Intersection(targets[rightID])
			if !overlap.IsEmpty() {
				return fmt.Errorf("cpu list targets for clos %q and %q overlap on cpus %q",
					leftID, rightID, overlap.String())
			}
		}
	}
	return nil
}

func formatCPUListTargets(cpuSets map[string]machine.CPUSet) map[string]string {
	targets := make(map[string]string, len(cpuSets))
	for closID, cpus := range cpuSets {
		targets[closID] = cpus.String()
	}
	return targets
}

func sortedTargetClosIDs(targets map[string]string) []string {
	closIDs := make([]string, 0, len(targets))
	for closID := range targets {
		closIDs = append(closIDs, closID)
	}
	sort.Strings(closIDs)
	return closIDs
}

func (p *CPUListPlugin) isManaged(closID string) bool {
	return resctrlutil.IsCPUListManagedClosID(closID, p.config)
}

func (p *CPUListPlugin) apply(ctx context.Context, clos Clos, target string) error {
	if previous, ok := p.applied[clos]; ok && previous.cpuSet == target {
		if observer, ok := p.manager.(cpuListObserver); ok {
			matches, err := observer.CPUListMatches(ctx, clos.ID, target)
			if err != nil {
				return fmt.Errorf("observe cpu_list for CLOS %q: %w", clos.ID, err)
			}
			if matches {
				return nil
			}
		} else {
			return nil
		}
	}
	if err := p.manager.ApplyCPUList(ctx, clos.ID, target); err != nil {
		return fmt.Errorf("apply cpu_list=%q for CLOS %q: %w", target, clos.ID, err)
	}
	p.applied[clos] = appliedTarget{cpuSet: target}
	return nil
}

func (p *CPUListPlugin) pruneApplied(active map[Clos]struct{}) {
	for cacheKey := range p.applied {
		if _, ok := active[cacheKey]; !ok {
			delete(p.applied, cacheKey)
		}
	}
}

func enableCPUList(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	if conf.AdminQoSConfiguration.QRMPluginConfiguration != nil &&
		conf.AdminQoSConfiguration.QRMPluginConfiguration.RDTConfig.DisableRDT {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCPUList
}

func catSupported(capabilityProvider rdt.CATCapabilityProvider) bool {
	if capabilityProvider == nil {
		return false
	}
	capabilities, err := capabilityProvider.GetCATCapabilities()
	if err != nil {
		return !errors.Is(err, rdt.ErrCATUnsupported)
	}
	return len(capabilities) > 0
}
