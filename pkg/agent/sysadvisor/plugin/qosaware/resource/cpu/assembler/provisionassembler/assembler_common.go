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

package provisionassembler

import (
	"fmt"
	"time"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// ProvisionAssemblerCommon holds only long-lived, immutable dependencies. Every
// per-cycle input arrives through ProvisionContext (including its embedded Snapshot)
// and nothing reachable from it is stored between AssembleProvision calls, so the
// assembler cannot leak one cycle's calculationContext into the next.
type ProvisionAssemblerCommon struct {
	conf       *config.Configuration
	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewProvisionAssemblerCommon(conf *config.Configuration, _ interface{},
	reader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionAssembler {
	return &ProvisionAssemblerCommon{
		conf:       conf,
		metaReader: reader,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

func (pa *ProvisionAssemblerCommon) assembleReserve(result *types.InternalCPUCalculationResult) {
	// fill in reserve pool entry
	reservePoolSize, _ := pa.metaReader.GetPoolSize(commonstate.PoolNameReserve)
	result.SetPoolEntry(commonstate.PoolNameReserve, commonstate.FakedNUMAID, reservePoolSize, -1)
}

// validateDefaultShareBackfillConfig rejects incompatible feature combinations:
// backfilling the default share pool with all residual non-reclaim CPUs only
// makes sense when neither shared nor dedicated cores overlap reclaimed cores,
// otherwise the residual accounting would double-count overlapped CPUs.
func (pa *ProvisionAssemblerCommon) validateDefaultShareBackfillConfig(ctx ProvisionContext) error {
	conf := ctx.DynamicConfiguration
	if !conf.FillDefaultSharePoolWithNonReclaimCPUs {
		return nil
	}
	if ctx.AllowSharedCoresOverlapReclaimedCores || !ctx.DisableDedicatedCoresOverlapReclaimedCores {
		return fmt.Errorf("fill default share pool requires shared and dedicated reclaim overlap disabled")
	}
	return nil
}

func (pa *ProvisionAssemblerCommon) AssembleProvision(ctx ProvisionContext) (types.InternalCPUCalculationResult, error) {
	if ctx.DynamicConfiguration == nil {
		return types.InternalCPUCalculationResult{}, fmt.Errorf("dynamic configuration is nil")
	}

	if err := pa.validateDefaultShareBackfillConfig(ctx); err != nil {
		general.Errorf("validateDefaultShareBackfillConfig failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries:                 make(map[string]map[int]types.CPUResource),
		PoolOverlapInfo:             map[string]map[int]map[string]int{},
		PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{},
		TimeStamp:                   time.Now(),
	}
	// mark the backfill enabled once so downstream finalize can decide whether to
	// override the default share pool quantity with the allocatable upper bound.
	calculationResult.DefaultShareBackfill.Enabled = ctx.DynamicConfiguration.FillDefaultSharePoolWithNonReclaimCPUs

	pa.assembleReserve(&calculationResult)

	regionHelper := NewRegionMapHelper(ctx.RegionMap)

	err := pa.assembleWithNUMABinding(ctx, regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleWithNUMABinding failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	err = pa.assembleWithoutNUMABinding(ctx, regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleWithoutNUMABinding failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	err = pa.assembleNUMABindingNUMAExclusive(ctx, regionHelper, &calculationResult)
	if err != nil {
		general.Errorf("assembleNUMABindingNUMAExclusive failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	// after every scope is assembled, override the default share pool quantity
	// with the canonical residual budget when the backfill is enabled.
	if err = pa.finalizeDefaultShareBackfill(ctx, regionHelper, &calculationResult); err != nil {
		general.Errorf("finalizeDefaultShareBackfill failed with error: %v", err)
		return types.InternalCPUCalculationResult{}, err
	}

	return calculationResult, nil
}

// assembleWithoutNUMABinding assembles the global, non-binding reclaim/scale-down
// scope. It shares the per-NUMA pool writer but always under FakedNUMAID (-1):
// non-binding share/isolation regions are keyed there in RegionMapHelper, so a
// non-binding region never lands under a real NUMA slot.
func (pa *ProvisionAssemblerCommon) assembleWithoutNUMABinding(ctx ProvisionContext, regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	return pa.assembleWithoutNUMAExclusivePool(ctx, regionHelper, commonstate.FakedNUMAID, result)
}

// assembleWithNUMABinding walks every backing NUMA and assembles the
// non-exclusive (share + isolation) pool for each. The iteration is over a Go
// map and so is intentionally order-independent: each call writes only disjoint
// (poolName, numaID) keys into result, and the per-scope reclaim overlaps are
// sorted by key before being summarized (see clampReclaimOverlapMetadata), so a
// nondeterministic visitation order can never change the published numbers.
// Non-binding NUMAs resolve to no regions here (their share regions live under
// FakedNUMAID) and are cheap no-ops; the real binding regions materialize here.
func (pa *ProvisionAssemblerCommon) assembleWithNUMABinding(ctx ProvisionContext, regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range ctx.NUMAAvailable {
		err := pa.assembleWithoutNUMAExclusivePool(ctx, regionHelper, numaID, result)
		if err != nil {
			return err
		}
	}

	return nil
}

// assembleNUMABindingNUMAExclusive assembles disjoint dedicated NUMA-exclusive
// regions. It runs AFTER the non-exclusive assembly on purpose: an exclusive
// region owns its whole CPUs and writes its own reclaim scope
// (exclusive/<regionName>), which must not be folded into the shared reclaim pool
// the previous two steps built. Like assembleWithNUMABinding the map walk is
// order-independent because each exclusive region writes its own scoped keys.
func (pa *ProvisionAssemblerCommon) assembleNUMABindingNUMAExclusive(ctx ProvisionContext, regionHelper *RegionMapHelper, result *types.InternalCPUCalculationResult) error {
	for numaID := range ctx.NUMAAvailable {
		dedicatedNUMAExclusiveRegions := regionHelper.GetRegions(numaID, configapi.QoSRegionTypeDedicated)
		for _, r := range dedicatedNUMAExclusiveRegions {
			if !r.IsNumaBinding() || !r.IsNumaExclusive() {
				continue
			}

			if err := pa.assembleDedicatedNUMAExclusiveRegion(ctx, r, result); err != nil {
				return fmt.Errorf("failed to assemble dedicatedNUMAExclusiveRegion: %v", err)
			}
		}
	}

	return nil
}
