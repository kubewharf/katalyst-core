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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// ReclaimConstraintScope identifies an independently constrained reclaim
// target. The type and its stable string contract now live in cputypes; the
// alias keeps this package's references source-compatible.
type ReclaimConstraintScope = cputypes.ReclaimConstraintScope

// The scope constructors are re-exported as thin wrapper functions so existing
// callers (and in-package tests) keep compiling while the contract lives in
// cputypes. Wrapping them as funcs rather than package-level var function values
// keeps the symbol a non-mutable reference: nothing in this package (or a test)
// can reassign these to a different implementation at runtime.
func NewNonExclusiveReclaimConstraintScope(numaID int) ReclaimConstraintScope {
	return cputypes.NewNonExclusiveReclaimConstraintScope(numaID)
}

func NewExclusiveReclaimConstraintScope(regionName string) ReclaimConstraintScope {
	return cputypes.NewExclusiveReclaimConstraintScope(regionName)
}

func NewLegacyExclusiveReclaimConstraintScope(regionName string) ReclaimConstraintScope {
	return cputypes.NewLegacyExclusiveReclaimConstraintScope(regionName)
}

// ApplyReclaimConstraint clamps size and a non-negative quota limit to the
// optional, rate-limited per-scope ceiling. Only scopes listed in activeScopes
// are constrained; every other scope passes through untouched so a ramp-up on
// one NUMA cannot compress the reclaim pool of an unrelated (dedicated) NUMA.
//
// P4 boundary contract (sentinel / empty / non-negative):
//   - ceiling is *int: a nil (or absent) ceiling is the "unconstrained"
//     SENTINEL, not zero. The pool keeps its steady (MaxRatio) size -- an
//     unknown or unavailable target must never be treated as 0 nor floored to an
//     arbitrary value.
//   - a nil ceilings or activeScopes map is the EMPTY configured input and is
//     valid: it behaves as if no dynamic ceiling / no active scope were set.
//   - limit is treated as a quota only when NON-NEGATIVE (limit >= 0); a
//     negative limit means "no quota" and passes through, so -1 (the package's
//     unlimited sentinel) never floors the pool.
//
// reservedForReclaim is the pure steady reservation floor; once size is
// already at/below it the ceiling is a no-op and excess is zero.
func ApplyReclaimConstraint(scope ReclaimConstraintScope, size int, limit float64, reservedForReclaim int,
	constraint cputypes.ReclaimConstraint, ceilings map[ReclaimConstraintScope]*int,
	activeScopes map[ReclaimConstraintScope]bool,
) (int, float64, int) {
	if constraint != cputypes.ReclaimConstraintReservedFloor || !activeScopes[scope] || size <= reservedForReclaim {
		return size, limit, 0
	}

	excess := general.Max(size-reservedForReclaim, 0)
	if configured, ok := ceilings[scope]; ok && configured != nil {
		upper := *configured
		if upper < reservedForReclaim {
			upper = reservedForReclaim
		}
		if size > upper {
			size = upper
		}
	}
	if limit >= 0 && limit > float64(size) {
		limit = float64(size)
	}
	return size, limit, excess
}

// RecordReclaimConstraintTarget records the unconstrained desired target, the
// steady reservation floor, the steady upper bound, the optional rate-limited
// ceiling and the member NUMA set for one scope, and retains the maximum excess
// across all scopes. It initializes a nil target map and safely ignores a nil
// result. The recorded target is the descriptive contract consumed by the
// constraint guard commit and by diagnostic metrics; it never feeds back into
// this cycle's quantity decision (the assembler is the only quantity boundary).
func RecordReclaimConstraintTarget(result *types.InternalCPUCalculationResult, constraint cputypes.ReclaimConstraint,
	scope ReclaimConstraintScope, target types.ReclaimConstraintTarget, excess int,
) {
	if result == nil || constraint != cputypes.ReclaimConstraintReservedFloor {
		return
	}
	if result.ReclaimConstraintTargets == nil {
		result.ReclaimConstraintTargets = make(map[string]types.ReclaimConstraintTarget)
	}
	result.ReclaimConstraintTargets[string(scope)] = target
	result.ReclaimConstraintExcess = general.Max(result.ReclaimConstraintExcess, excess)
}
