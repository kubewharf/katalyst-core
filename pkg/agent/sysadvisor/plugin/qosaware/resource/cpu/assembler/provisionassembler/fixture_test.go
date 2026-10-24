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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// provisionTestFixture bundles a provision assembler with the mutable per-cycle
// bookkeeping the assembler used to capture by pointer at construction. The
// assembler is now stateless across cycles: tests mutate the exposed maps and call
// ctx() to obtain a fresh, deep-copied ProvisionContext for the next cycle. This
// mirrors the production wiring, where the cpu root package builds a Snapshot
// from its own bookkeeping on every AssembleProvision call.
//
// The override fields below mirror the per-cycle inputs the advisor packs into a
// ProvisionContext (ramp-up domains, reclaim constraints/ceilings, live reclaim).
// They are applied on top of the snapshot every time ctx() is built, so a test
// that used to set pa.calculationContext.X now sets f.X and keeps calling ctx().
type provisionTestFixture struct {
	*ProvisionAssemblerCommon
	conf                   *config.Configuration
	dynamicConf            *dynamic.Configuration
	regionMap              map[string]region.QoSRegion
	reservedForReclaim     map[int]int
	rampUpReclaimCPUSetCap map[int]int
	numaAvailable          map[int]int
	nonBindingNumas        machine.CPUSet
	allowShared            bool
	disableDedicated       bool

	// per-cycle ctx overrides (mirror advisor-built ProvisionContext inputs).
	RampUpDomains        []int
	ReclaimConstraint    cputypes.ReclaimConstraint
	ReclaimActiveScopes  map[ReclaimConstraintScope]bool
	ReclaimCeilings      map[ReclaimConstraintScope]*int
	ReclaimDomainTargets map[ReclaimConstraintScope]types.ReclaimConstraintTarget
	LiveReclaimByNUMA    map[int]int
}

// snapshot builds the immutable per-cycle Snapshot from the fixture's current
// bookkeeping maps. It delegates to NewSnapshot so the test fixture faithfully
// reproduces the production deep-copy contract: the returned Snapshot owns its own
// maps and CPUSet, and later mutations of the fixture's bookkeeping do not disturb
// an already-built Snapshot.
func (f *provisionTestFixture) snapshot() Snapshot {
	return NewSnapshot(
		f.regionMap,
		f.reservedForReclaim,
		f.rampUpReclaimCPUSetCap,
		f.numaAvailable,
		f.nonBindingNumas,
		f.allowShared,
		f.disableDedicated,
		f.RampUpDomains,
		f.ReclaimDomainTargets,
	)
}

// ctx returns a fresh ProvisionContext carrying a snapshot of the current
// bookkeeping plus any per-cycle overrides set on the fixture. Callers mutate the
// fixture maps/overrides first, then call ctx() again to pick the changes up for
// the next cycle; nothing is retained between calls. RampUpDomains travels inside
// the embedded Snapshot (snapshot()), not as a direct ctx field.
func (f *provisionTestFixture) ctx() ProvisionContext {
	return ProvisionContext{
		DynamicConfiguration: f.dynamicConf,
		ReclaimConstraint:    f.ReclaimConstraint,
		ReclaimActiveScopes:  f.ReclaimActiveScopes,
		ReclaimCeilings:      f.ReclaimCeilings,
		LiveReclaimByNUMA:    f.LiveReclaimByNUMA,
		Snapshot:             f.snapshot(),
	}
}
