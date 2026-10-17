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

package topology

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

// RequiredCPUSetByRelFromNodeSpecs freezes canonical reclaim NUMA floors from
// the same immutable node-spec projection used to build the attempt's DAG.
func RequiredCPUSetByRelFromNodeSpecs(specs []NodeSpec) map[string]machine.CPUSet {
	required := make(map[string]machine.CPUSet)
	for _, spec := range specs {
		if spec.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		required[spec.Rel] = spec.CPUs.Clone()
	}
	return required
}
