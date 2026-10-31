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
	"math"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// cpuCountInNUMAs returns the total number of CPUs within the given NUMA set.
// It returns 0 when metaServer or its CPUDetails is not ready, so callers can
// skip clamping instead of mistakenly capping reclaim to 0.
func (pa *ProvisionAssemblerCommon) cpuCountInNUMAs(numas machine.CPUSet) int {
	if pa.metaServer == nil || pa.metaServer.CPUDetails == nil {
		return 0
	}
	return pa.metaServer.CPUDetails.CPUsInNUMANodes(numas.ToSliceInt()...).Size()
}

func (pa *ProvisionAssemblerCommon) reclaimRatioCPUsPerCore(ctx ProvisionContext, numaID int) (int, error) {
	// Physical-core ratio rounding only applies to the specific NUMA that is under
	// an active ramp-up hard partition; other NUMAs keep the historical logical-CPU
	// rounding.
	if !pa.rampUpHardPartitionActive(ctx, numaID) {
		return 1, nil
	}
	if pa.metaServer == nil || pa.metaServer.CPUTopology == nil {
		return 0, fmt.Errorf("cpu topology is unavailable for hard reclaim ratio cap")
	}
	cpusPerCore := pa.metaServer.CPUTopology.CPUsPerCore()
	if cpusPerCore <= 0 {
		return 0, fmt.Errorf("invalid cpus per core %d for hard reclaim ratio cap", cpusPerCore)
	}
	return cpusPerCore, nil
}

// clampByReclaimedCPUMaxRatio caps the reclaim pool size (and quota when it is
// not the -1 sentinel) by ratio*cpuCount. When ratio<=0 it disables clamping and
// returns the inputs unchanged; a negative limit (e.g. the -1 "no quota limit"
// sentinel) is preserved as-is.
//
// In legacy mode the cap is rounded down to an even logical CPU count. In hard
// partition mode the ratio is applied to physical cores, rounded down to a
// complete physical core count, and converted back to logical CPUs. The reclaim
// reservation remains a floor in both modes.
func clampByReclaimedCPUMaxRatio(
	size int,
	limit float64,
	ratio float64,
	cpuCount int,
	reservedForReclaim int,
	cpusPerCore int,
) (int, float64, error) {
	if ratio <= 0 {
		return size, limit, nil
	}
	capCPUs, err := calculateReclaimRatioCap(cpuCount, ratio, reservedForReclaim, cpusPerCore)
	if err != nil {
		return 0, 0, err
	}
	if size > capCPUs {
		size = capCPUs
	}
	if limit >= 0 && limit > float64(capCPUs) {
		limit = float64(capCPUs)
	}
	return size, limit, nil
}

func calculateReclaimRatioCap(cpuCount int, ratio float64, reservedForReclaim, cpusPerCore int) (int, error) {
	if cpusPerCore <= 1 {
		capCPUs := int(math.Floor(ratio * float64(cpuCount)))
		capCPUs -= capCPUs % 2
		return general.Max(capCPUs, reservedForReclaim), nil
	}
	return machine.CalculatePerNUMAHardReclaimTarget(cpuCount, ratio, 0, reservedForReclaim, cpusPerCore)
}

// reclaimClampResult carries the structured diagnostics of a single reclaim
// max-ratio clamp: the raw size before clamping, the final size and limit after
// clamping, and the amount of reclaim cores released by the clamp. It exposes
// the released residual so callers can later backfill the default share pool.
type reclaimClampResult struct {
	RawSize      int
	FinalSize    int
	ReleasedSize int
	FinalLimit   float64
}

// clampByReclaimedCPUMaxRatioWithDiagnostics wraps clampByReclaimedCPUMaxRatio
// without changing its clamp semantics; it only augments the return value with
// diagnostics (raw size and released cores). It does not write into
// InternalCPUCalculationResult and does not perform any accumulation.
func clampByReclaimedCPUMaxRatioWithDiagnostics(size int, limit float64, ratio float64,
	cpuCount int, reservedForReclaim int, cpusPerCore int,
) (reclaimClampResult, error) {
	finalSize, finalLimit, err := clampByReclaimedCPUMaxRatio(
		size, limit, ratio, cpuCount, reservedForReclaim, cpusPerCore)
	if err != nil {
		return reclaimClampResult{}, err
	}
	return reclaimClampResult{
		RawSize:      size,
		FinalSize:    finalSize,
		ReleasedSize: general.Max(0, size-finalSize),
		FinalLimit:   finalLimit,
	}, nil
}
