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
	"context"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func verifyResetConvergence(ctx context.Context, cg cgroupclient.CgroupClient, dag *TopoDAG, targetByRel map[string]machine.CPUSet) ConvergenceReport {
	// This reports the post-reset state of statically controlled DAG targets only.
	// It does not prove convergence of dynamic descendants, which are not read.
	// It neither relies on fail-open behavior nor on a recovered live hierarchy.
	report := ConvergenceReport{}
	for _, node := range dag.Nodes() {
		target := targetByRel[node.Rel]
		observed, err := cg.ReadCPUSet(ctx, node.Rel)
		if err != nil {
			report.NonConvergedTargets = append(report.NonConvergedTargets, RelConvergence{
				Rel:    node.Rel,
				Target: target.Clone(),
				Reason: convergenceReasonReadError,
			})
			continue
		}
		if !observed.Equals(target) {
			report.NonConvergedTargets = append(report.NonConvergedTargets, RelConvergence{
				Rel:      node.Rel,
				Observed: observed.Clone(),
				Target:   target.Clone(),
				Reason:   convergenceReasonTargetMismatch,
			})
		}
	}
	report.FullyConverged = len(report.NonConvergedTargets) == 0
	return report
}
