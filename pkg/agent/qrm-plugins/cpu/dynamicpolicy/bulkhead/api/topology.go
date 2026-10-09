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

package api

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ConvergenceLevel is the opaque result the topology owner returns to the
// manager. The manager only translates the level into orchestration decisions
// (publish vs. skip, gate vs. run dependent plugins) and never inspects the
// internal convergence booleans that produced it.
type ConvergenceLevel int32

const (
	// ConvergenceLevelNone means the topology did not reach a publishable state.
	// The manager must not publish an applied view and must not authorize any
	// dependent plugin.
	ConvergenceLevelNone ConvergenceLevel = iota
	// ConvergenceLevelPartial means a coarse-grained, parent-safe view is
	// publishable: the manager publishes the view but gates dependent plugins
	// that require exact leaf state.
	ConvergenceLevelPartial
	// ConvergenceLevelFull means the topology fully converged to a verified
	// final snapshot. The manager publishes the view and runs all dependent
	// plugins, authorizing periodical consumers.
	ConvergenceLevelFull
	// ConvergenceLevelDeferred means the topology owner deliberately held the
	// current state for this cycle (e.g. a read-only shadow projection decided
	// not to perturb non-empty reclaim cgroups during a pending admission). It is
	// a zero-write hold: the manager keeps the previous AppliedView, must not
	// publish a new view or checkpoint, must not authorize dependent plugins
	// (workqueue/system consumers) that would interpret an empty reclaim set,
	// and surfaces an identifiable retry/defer error to the admission caller.
	ConvergenceLevelDeferred
)

// TopologyOutcome is the sole handoff from the topology owner to the manager.
// It deliberately collapses the topology layer's internal convergence report
// into an opaque Level plus a small set of manager-owned fields. The manager
// must not introspect topology internals beyond this struct.
type TopologyOutcome struct {
	// Level is the manager-facing convergence classification.
	Level ConvergenceLevel
	// View is the candidate applied view the manager may publish. It is
	// non-nil exactly when Level is ConvergenceLevelPartial or
	// ConvergenceLevelFull, and nil for ConvergenceLevelNone and
	// ConvergenceLevelDeferred. The manager is the single writer of the
	// authoritative applied view; the plugin only proposes this candidate.
	View *model.AppliedView
	// Reclaim is the verified reclaim CPUSet associated with View.
	Reclaim machine.CPUSet
	// Note is a one-line diagnostic summary embedded in error/log text.
	Note string
}
