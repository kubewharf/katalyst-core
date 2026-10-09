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

// Package cputypes owns the low-level, dependency-free type vocabulary for the
// per-scope reclaim constraint contract shared by the cpu advisor guard
// (package cpu) and the provision assembler.
//
// It is deliberately a leaf package: it imports neither the cpu advisor root
// nor the provision assembler, so both may depend on it without creating an
// import cycle. The dependency direction is:
//
//	cputypes  <-  provisionassembler  (assembler clamps to the ceilings)
//	cputypes  <-  cpu                 (guard decides the ceilings)
//
// Everything that identifies a scope or classifies a constraint lives here so
// the "type words" no longer have to be pulled in through the assembler. The
// clamping/recording behavior (ApplyReclaimConstraint /
// RecordReclaimConstraintTarget) stays in provisionassembler: it is assembler
// logic, not a type contract.
package cputypes

import "fmt"

// ReclaimConstraint classifies how the provision assembler must treat the
// reclaim pool size this cycle.
type ReclaimConstraint uint8

const (
	// ReclaimConstraintNone means no dynamic ceiling is published: the pool
	// keeps its steady (MaxRatio) size and nothing is clamped.
	ReclaimConstraintNone ReclaimConstraint = iota
	// ReclaimConstraintReservedFloor means the per-scope rate-limited ceilings
	// are active: every constrained scope may be clamped down toward its
	// reserved floor, but never below it.
	ReclaimConstraintReservedFloor
)

// ReclaimConstraintScope identifies an independently constrained reclaim
// target. The distinct type prevents callers from accidentally using unrelated
// strings as keys in per-scope ceiling maps.
//
// The string form is part of the contract (it is what gets recorded into
// InternalCPUCalculationResult.ReclaimConstraintTargets):
//
//	non-exclusive/<numaID>   a non-exclusive NUMA allocation; FakedNUMAID (-1)
//	                         is the global/non-binding aggregate scope.
//	exclusive/<regionName>   a disjoint dedicated NUMA-exclusive region.
//	legacy-exclusive/<name>  a legacy overlapping dedicated NUMA-exclusive region.
type ReclaimConstraintScope string

// NewNonExclusiveReclaimConstraintScope returns the stable scope key for a
// non-exclusive NUMA allocation. Faked NUMA IDs are preserved verbatim.
func NewNonExclusiveReclaimConstraintScope(numaID int) ReclaimConstraintScope {
	return ReclaimConstraintScope(fmt.Sprintf("non-exclusive/%d", numaID))
}

// NewExclusiveReclaimConstraintScope returns the stable scope key for a
// disjoint dedicated NUMA-exclusive region.
func NewExclusiveReclaimConstraintScope(regionName string) ReclaimConstraintScope {
	return ReclaimConstraintScope(fmt.Sprintf("exclusive/%s", regionName))
}

// NewLegacyExclusiveReclaimConstraintScope returns the stable scope key for a
// legacy overlapping dedicated NUMA-exclusive region.
func NewLegacyExclusiveReclaimConstraintScope(regionName string) ReclaimConstraintScope {
	return ReclaimConstraintScope(fmt.Sprintf("legacy-exclusive/%s", regionName))
}
