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

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Reduced lifecycle fixture. It includes five controlled specs and a live leaf;
// CPU masks are from the recorded Mixed transition. It is not an atomic node dump.
func dormantTransferFixture(t *testing.T) (*admissionTraceFixture, *CompleteSnapshot) {
	f := newAdmissionTraceFixture(t)
	f.cpuDetails = machine.CPUDetails{}
	for c := 0; c < 96; c++ {
		n := 0
		if c%48 >= 24 {
			n = 1
		}
		f.cpuDetails[c] = machine.CPUTopoInfo{NUMANodeID: n, CoreID: c % 48}
	}
	f.round.cpuDetails = f.cpuDetails
	f.round.allowEmptyTarget = false
	f.addPrimary("primary", "1-23,25-47,49-71,73-95", "0-1")
	f.addReclaim("reclaim", "0,24,48,72", "0-1")
	f.addReclaim("reclaim/bucket-0", "0,48", "0")
	f.addReclaim("reclaim/bucket-1", "24,72", "1")
	f.addReclaim("system", "0,24,48,72", "0-1")
	f.driver.markInactive("reclaim/bucket-1")
	f.driver.add("primary/pod", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "1-23,25-47,49-71,73-95", "1")
	f.driver.add("primary/pod/leaf", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "27-47,75-95", "1")
	f.dynamicByRel["primary/pod/leaf"] = machine.MustParse("24")
	for i := range f.specs {
		s := &f.specs[i]
		switch s.Rel {
		case "primary":
			s.CPUs = machine.MustParse("1,3-47,49,51-95")
		case "system":
			s.Role = TopoNodeRoleReclaimSibling
			s.CPUs = machine.MustParse("0,2,48,50")
		case "reclaim":
			s.CPUs = machine.MustParse("0,2,48,50")
		case "reclaim/bucket-0":
			s.Role = TopoNodeRoleReclaimNUMABucket
			s.CPUs = machine.MustParse("0,2,48,50")
			s.Constraint = TopologyConstraint{CPUUpperBound: machine.MustParse("0-23,48-71"), Scope: TopologyScopeNUMANode}
			s.Metadata = map[string]string{"numa": "0"}
		case "reclaim/bucket-1":
			s.Role = TopoNodeRoleReclaimNUMABucket
			s.CPUs = machine.NewCPUSet()
			s.Constraint = TopologyConstraint{CPUUpperBound: machine.MustParse("24-47,72-95"), Scope: TopologyScopeNUMANode}
			s.Metadata = map[string]string{"numa": "1"}
		}
		f.targetByRel[s.Rel] = s.CPUs.Clone()
	}
	f.requiredByRel = RequiredCPUSetByRelFromNodeSpecs(f.specs)
	f.snapshot()
	base, err := newDormantCompleteSnapshotSource(f.driver, f.round.dag, f.budget)(context.Background())
	require.NoError(t, err)
	f.round.semanticTargetByRel = desiredTargets(f.round.dag)
	base, err = f.round.preparePlanningSnapshot(context.Background(), base)
	require.NoError(t, err)
	return f, base
}
