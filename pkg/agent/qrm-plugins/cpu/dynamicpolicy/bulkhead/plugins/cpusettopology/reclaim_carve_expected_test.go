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
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestBuildExpectedTreatsReclaimCarvedProtectedLeafAsConverged is the regression
// for the high-churn fixed-point "no progress" hard failure.
//
// Scenario (reproduced from the failing 4-NUMA / 128-CU node): an already
// placed protected leaf is admitted again while the per-NUMA reclaim floor has
// expanded to carve exactly two CPUs out of its original allocation. The
// enforced cgroup cpuset is therefore the allocation MINUS the reclaim-effective
// set (correct, reclaim-disjoint), but DesiredView still carries the stale full
// allocation as the container's desired cpuset. Before the fix the coordinator
// bound the stale full allocation as the expected physical target, so the
// convergence proof saw a permanent dynamic_target_mismatch, the planner
// correctly refused to grow the leaf back into reclaim, and the fixed-point
// compiler deadlocked with a bare ErrNoProgress.
func TestBuildExpectedTreatsReclaimCarvedProtectedLeafAsConverged(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "reclaim-carve-pod"
		containerID   = "carve-id"
		containerName = "demo"
	)
	resolvedRel := "kubepods/pod" + podUID + "/" + containerID

	unregister := cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(cgcommon.RelativeCgroupPathHandler{
		Name: "reclaim-carve",
		Handler: func(gotPodUID, id string) (string, bool, error) {
			if gotPodUID != podUID || id != containerID {
				return "", true, nil
			}
			return "/" + resolvedRel, false, nil
		},
	})
	t.Cleanup(unregister)

	// desired (stale): the protected leaf was allocated its full range.
	desired := machine.NewCPUSet(4, 5, 6, 7, 8, 9, 68, 69, 70, 71, 72)
	// enforced current: reclaim already carved exactly 6 and 70 out of the leaf.
	current := machine.NewCPUSet(4, 5, 7, 8, 9, 68, 69, 71, 72)
	// reclaim-effective (per-NUMA floor), a superset of the carved-out CPUs.
	reclaim := machine.NewCPUSet(6, 70, 30, 94, 46, 110, 48, 112)

	if !desired.Difference(current).IsSubsetOf(reclaim) {
		t.Fatalf("test fixture broken: desired-current=%s is not subset of reclaim=%s",
			desired.Difference(current).String(), reclaim.String())
	}
	if !current.Intersection(reclaim).IsEmpty() {
		t.Fatalf("test fixture broken: current=%s already touches reclaim", current.String())
	}

	pod := lifecycleProofPod(podUID, []string{containerName}, map[string]string{containerName: containerID})
	fetcher := &cacheThenFreshSnapshotFetcher{
		cachedPods: []*v1.Pod{pod},
		freshPods:  []*v1.Pod{pod},
	}

	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {containerName: desired},
		},
	}}
	view.DesiredReclaimEffective = reclaim

	result, err := (&CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:   "kubepods",
			EnableAdmissionLeafDefer: true,
		},
		cgroup: &fakeCgroupClient{
			existing: map[string]bool{resolvedRel: true},
			cpus:     map[string]machine.CPUSet{resolvedRel: current},
		},
	}).buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
			Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: view,
	})
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}

	// The expected physical target must be the reclaim-free enforced cpuset, so
	// the convergence proof sees the leaf as already converged instead of
	// demanding it grow back into reclaim CPUs.
	got, ok := result.ExpectedByRel[resolvedRel]
	if !ok {
		t.Fatalf("expected leaf %q missing from ExpectedByRel=%#v", resolvedRel, result.ExpectedByRel)
	}
	if !got.Equals(current) {
		t.Fatalf("expected target = %s, want the reclaim-free enforced cpuset %s (stale desired %s includes reclaim)",
			got.String(), current.String(), desired.String())
	}
	if got.Intersection(reclaim).IsEmpty() == false {
		t.Fatalf("expected target %s still intersects reclaim %s", got.String(), reclaim.String())
	}
	if len(result.DeferredLeafByRel) != 0 {
		t.Fatalf("reclaim-carved leaf should not be deferred, got %#v", result.DeferredLeafByRel)
	}
}
