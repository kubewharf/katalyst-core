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

// Package deadlock holds the result types and error surface of the v1
// non-empty drain deadlock probe. It depends only on topology/model and the
// machine package so that it cannot import the parent topology package. The
// planner-coupled probe entry point (analyzeV1Deadlock) remains in the parent
// package because it needs the live snapshot, budget and planner relation
// helpers; this package owns the immutable outcome that crosses back into the
// parent package and its callers.
package deadlock

import (
	"errors"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ErrIncompleteRequiredCoreReleaseWitness records that a required-core release
// could not be witnessed before the probe budget was exhausted.
var ErrIncompleteRequiredCoreReleaseWitness = errors.New(
	"incomplete required core release witness")

// DefaultDeadlockProbeBudget bounds canonical drain-atom projections when no
// explicit budget is supplied and no tracker is attached.
const DefaultDeadlockProbeBudget = 4096

// ProbeCompleteness reports whether the deadlock probe finished exhaustively or
// was stopped early by its budget.
type ProbeCompleteness string

const (
	ProbeComplete      ProbeCompleteness = "complete"
	ProbeIndeterminate ProbeCompleteness = "indeterminate"
)

// DrainAtom is one ordered, atomic CPU transfer between ownership domains.
type DrainAtom struct {
	Source      model.DomainID
	Destination model.DomainID
	CPUs        machine.CPUSet
}

// DrainAtomClass labels why a drain atom exists.
type DrainAtomClass string

const (
	DrainAtomClassV1Empty    DrainAtomClass = "v1_empty"
	DrainAtomClassProtected  DrainAtomClass = "protected"
	DrainAtomClassReleasable DrainAtomClass = "releasable"
	DrainAtomClassHeld       DrainAtomClass = "held"
)

// DeadlockAnalysis is the immutable outcome of a v1 drain deadlock probe.
type DeadlockAnalysis struct {
	Completeness   ProbeCompleteness
	Atoms          []DrainAtom
	AtomClasses    []DrainAtomClass
	SafeSeed       *DrainAtom
	SafeGrowAnchor machine.CPUSet
	EmptyBlockers  map[string]machine.CPUSet
	Protected      machine.CPUSet
	ProbeStats     DeadlockProbeStats
}

// DeadlockProbeStats records probe accounting for logging and budget diagnosis.
type DeadlockProbeStats struct {
	Atoms                      int
	AtomIndex                  int
	AtomSource                 model.DomainID
	AtomDestination            model.DomainID
	SnapshotEntries            int
	SnapshotChildEdges         int
	ProtectedRels              int
	ProtectedPendingCPUs       int
	ProbeOperations            int
	ProbeLimit                 int
	AutoBudget                 bool
	ContextOperations          int
	ContextPhase               string
	BaseOperations             int
	ProtectedOperations        int
	RelIndexOperations         int
	ChildIndexOps              int
	ChildMembershipsScanned    int
	FrontierIndexOps           int
	FrontierMembershipsScanned int
	AncestorClosureOps         int
	AtomOperations             int
	SnapshotID                 model.SnapshotID
}

// StructuralV1NonEmptyDeadlock is returned to the coordinator when the probe
// found a structural v1 non-empty deadlock that cannot be drained safely.
type StructuralV1NonEmptyDeadlock struct {
	Analysis DeadlockAnalysis
}

// Error implements error.
func (e *StructuralV1NonEmptyDeadlock) Error() string {
	return fmt.Sprintf("structural cgroup v1 non-empty deadlock: atoms=%d blockers=%d",
		len(e.Analysis.Atoms), len(e.Analysis.EmptyBlockers))
}
