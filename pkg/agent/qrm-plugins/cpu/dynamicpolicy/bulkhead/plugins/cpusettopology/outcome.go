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
	"fmt"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
)

// outcomeControl selects which acceptance gate the owner applies to a result.
type outcomeControl struct {
	// requireReclaimOnly is set on the disabled-topology reconcile path. It
	// mirrors the gate that previously lived in the manager: a disabled
	// reconcile is accepted only when it fully converged to a ReclaimOnly
	// view. The normal apply path instead accepts either a full or a
	// parent-safe result.
	requireReclaimOnly bool
}

// toOutcome classifies a topology DAGApplyResult into the opaque manager-facing
// TopologyOutcome. The classification is a verbatim migration of the gates the
// manager used to perform inline:
//
//   - normal apply path: manager.go "successfulTopology := FC || PS; reject if
//     !successfulTopology || !FinalSnapshotCurrent || AppliedView==nil; Partial
//     when ParentSafe, Full otherwise".
//   - disabled-reconcile path: manager.go reject if !FullyConverged ||
//     !FinalSnapshotCurrent || AppliedView==nil || AppliedView.Level !=
//     ReclaimOnly.
//
// The manager now only switches on outcome.Level and never inspects the
// underlying convergence booleans.
func toOutcome(res topology.DAGApplyResult, ctl outcomeControl) bulkheadapi.TopologyOutcome {
	if ctl.requireReclaimOnly {
		if !res.FullyConverged || !res.FinalSnapshotCurrent || res.AppliedView == nil ||
			res.AppliedView.Level != model.AppliedViewLevelReclaimOnly {
			return bulkheadapi.TopologyOutcome{
				Level: bulkheadapi.ConvergenceLevelNone,
				Note:  diagNote(res),
			}
		}
		return bulkheadapi.TopologyOutcome{
			Level:   bulkheadapi.ConvergenceLevelFull,
			View:    res.AppliedView,
			Reclaim: res.AppliedView.ReclaimEffective.Clone(),
			Note:    diagNote(res),
		}
	}
	successfulTopology := res.FullyConverged || res.ParentSafe
	if !successfulTopology || !res.FinalSnapshotCurrent || res.AppliedView == nil {
		return bulkheadapi.TopologyOutcome{
			Level: bulkheadapi.ConvergenceLevelNone,
			Note:  diagNote(res),
		}
	}
	level := bulkheadapi.ConvergenceLevelFull
	// FullyConverged is the strong, fully-trust case. ParentSafe only downgrades
	// to Partial when full convergence was NOT achieved (the coordinator sets
	// Converged=true and ParentSafe=true mutually exclusively, so both flags is
	// unreachable; FC wins if it ever occurs).
	if res.ParentSafe && !res.FullyConverged {
		level = bulkheadapi.ConvergenceLevelPartial
	}
	return bulkheadapi.TopologyOutcome{
		Level:   level,
		View:    res.AppliedView,
		Reclaim: res.AppliedView.ReclaimEffective.Clone(),
		Note:    diagNote(res),
	}
}

// diagNote renders the one-line diagnostic the manager embeds in its error
// text. It preserves the shape of the old NonConvergedError message.
func diagNote(res topology.DAGApplyResult) string {
	return fmt.Sprintf("current=%t deferred=%d report=%+v",
		res.FinalSnapshotCurrent, res.Deferred, res.ConvergenceReport)
}
