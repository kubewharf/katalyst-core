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

package topology

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
)

// DAGApplyResult is the bulkhead-layer result produced by the topology owner.
// It was previously declared in the api package; it lives here now because it
// carries topology.ConvergenceReport, an internal convergence artifact that
// must not leak into the api contract. The api layer only sees the opaque
// api.TopologyOutcome derived from this result.
type DAGApplyResult struct {
	Attempted            int
	Applied              int
	Skipped              int
	Failed               int
	Deferred             int
	FullyConverged       bool
	ParentSafe           bool
	DeferredLeafCount    int
	DeferredCPUCount     int
	FinalSnapshotCurrent bool
	ConvergenceReport    ConvergenceReport
	AppliedView          *model.AppliedView
}
