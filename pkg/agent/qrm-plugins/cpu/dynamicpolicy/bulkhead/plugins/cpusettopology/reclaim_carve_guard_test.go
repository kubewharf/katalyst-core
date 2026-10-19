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

// runCarveCase executes the builder once and returns the ExpectedByRel target
// (or nil) plus the DeferredLeafByRel set for the leaf rel.
func runCarveCase(
	t *testing.T,
	podUID, containerName, containerID string,
	desired, current, reclaim machine.CPUSet,
) (expected machine.CPUSet, expectedPresent, deferred bool, resolvedRel string) {
	t.Helper()
	resolvedRel = "kubepods/pod" + podUID + "/" + containerID

	unregister := cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(cgcommon.RelativeCgroupPathHandler{
		Name: "reclaim-carve-guard",
		Handler: func(gotPodUID, id string) (string, bool, error) {
			if gotPodUID != podUID || id != containerID {
				return "", true, nil
			}
			return "/" + resolvedRel, false, nil
		},
	})
	t.Cleanup(unregister)

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
	if got, ok := result.ExpectedByRel[resolvedRel]; ok {
		expectedPresent = true
		expected = got.Clone()
	}
	_, deferred = result.DeferredLeafByRel[resolvedRel]
	return expected, expectedPresent, deferred, resolvedRel
}

// Scenario B: reclaim floor further expanded after the leaf was carved for a
// subset. The leaf is already reclaim-free; expected must stay the enforced
// cpuset (no grow demanded).
func TestCarveFloorExpandedFurther(t *testing.T) {
	t.Parallel()
	desired := machine.NewCPUSet(4, 5, 6, 7, 8, 9, 68, 69, 70, 71, 72)
	// leaf carved for old floor {6,70}; new floor also claims 30,94
	current := machine.NewCPUSet(4, 5, 7, 8, 9, 68, 69, 71, 72)
	reclaim := machine.NewCPUSet(6, 70, 30, 94, 46, 110)

	got, present, deferred, _ := runCarveCase(t, "pod-b", "demo", "id-b", desired, current, reclaim)
	if !present {
		t.Fatalf("carved leaf missing from ExpectedByRel")
	}
	if deferred {
		t.Fatalf("carved leaf must not be deferred")
	}
	if !got.Equals(current) {
		t.Fatalf("expected = %s, want enforced %s (must not demand growth into reclaim)", got.String(), current.String())
	}
}

// Scenario C: the leaf is narrower than desired but is missing a BUSINESS cpu
// that is NOT reclaim (partial physical loss). The carve branch must NOT fire;
// the expected target must remain the full stale desired allocation so the
// planner repairs the leaf.
func TestCarveMissingBusinessCPUFallsThrough(t *testing.T) {
	t.Parallel()
	desired := machine.NewCPUSet(4, 5, 6, 7, 8, 9)
	// missing 5 (business) and 6 (reclaim): difference is NOT a subset of reclaim.
	current := machine.NewCPUSet(4, 7, 8, 9)
	reclaim := machine.NewCPUSet(6, 70)

	got, present, deferred, _ := runCarveCase(t, "pod-c", "demo", "id-c", desired, current, reclaim)
	if !present {
		t.Fatalf("leaf must appear in ExpectedByRel via fall-through")
	}
	if deferred {
		t.Fatalf("leaf must not be deferred")
	}
	if !got.Equals(desired) {
		t.Fatalf("expected = %s, want full desired %s so planner repairs missing business cpu", got.String(), desired.String())
	}
}

// Scenario D: leaf narrower than desired, missing exactly reclaim, but the leaf
// STILL TOUCHES a reclaim cpu (incomplete carve). The carve branch must NOT
// fire (intersection guard); fall-through binds full desired.
func TestCarveIncompleteTouchingReclaimFallsThrough(t *testing.T) {
	t.Parallel()
	desired := machine.NewCPUSet(4, 5, 6, 7, 8, 9, 68, 69, 70, 71, 72)
	// carve removed only 6; 70 (reclaim) is still held by the leaf.
	current := machine.NewCPUSet(4, 5, 7, 8, 9, 68, 69, 70, 71, 72)
	reclaim := machine.NewCPUSet(6, 70)

	got, present, deferred, _ := runCarveCase(t, "pod-d", "demo", "id-d", desired, current, reclaim)
	if !present {
		t.Fatalf("leaf must appear in ExpectedByRel via fall-through")
	}
	if deferred {
		t.Fatalf("leaf must not be deferred")
	}
	if !got.Equals(desired) {
		t.Fatalf("expected = %s, want full desired %s", got.String(), desired.String())
	}
	// expected deliberately equals the stale full allocation here: the planner
	// policy refuses growth into reclaim, which keeps the round NonConverged so
	// the transient partial-carve state is retried next cycle. The carve branch
	// must NOT have bound the half-carved enforced cpuset.
	if got.Equals(current) {
		t.Fatalf("expected = %s must not be the half-carved enforced cpuset", got.String())
	}
}

// Scenario E: reclaim floor SHRANK. The leaf was carved for {6,70}, but 70 is
// no longer reclaim. desired-current = {6,70} is NOT a subset of the new reclaim
// {6}; the carve branch must not fire, and expected must be the full desired so
// the planner can safely grow the leaf back onto 70.
func TestCarveReclaimShrankFallsThrough(t *testing.T) {
	t.Parallel()
	desired := machine.NewCPUSet(4, 5, 6, 7, 8, 9, 68, 69, 70, 71, 72)
	current := machine.NewCPUSet(4, 5, 7, 8, 9, 68, 69, 71, 72)
	// 70 returned to business: reclaim is now only {6, 30, 94}
	reclaim := machine.NewCPUSet(6, 30, 94)

	got, present, deferred, _ := runCarveCase(t, "pod-e", "demo", "id-e", desired, current, reclaim)
	if !present {
		t.Fatalf("leaf must appear in ExpectedByRel via fall-through")
	}
	if deferred {
		t.Fatalf("leaf must not be deferred")
	}
	if !got.Equals(desired) {
		t.Fatalf("expected = %s, want full desired %s so leaf may grow back onto 70", got.String(), desired.String())
	}
}

// Scenario F: leaf WIDER than desired but the extra cpus touch reclaim. Neither
// the defer branch (requires current disjoint from reclaim) nor the carve branch
// may fire; expected must be the full desired allocation.
func TestWiderLeafTouchingReclaimFallsThrough(t *testing.T) {
	t.Parallel()
	desired := machine.NewCPUSet(4, 5, 7, 8, 9)
	// leaf still inherits an envelope that includes reclaim 6 and extra 100.
	current := machine.NewCPUSet(4, 5, 6, 7, 8, 9, 100)
	reclaim := machine.NewCPUSet(6, 70)

	got, present, deferred, _ := runCarveCase(t, "pod-f", "demo", "id-f", desired, current, reclaim)
	if !present {
		t.Fatalf("leaf must appear in ExpectedByRel via fall-through")
	}
	if deferred {
		t.Fatalf("leaf touching reclaim must not be deferred")
	}
	if !got.Equals(desired) {
		t.Fatalf("expected = %s, want desired %s", got.String(), desired.String())
	}
}

func TestReclassifyAdmissionDeferredLeavesScopesPrimaryCleanup(t *testing.T) {
	t.Parallel()

	const rel = "kubepods/pod-a/container-a"
	for _, tc := range []struct {
		name         string
		current      machine.CPUSet
		target       machine.CPUSet
		reclaim      machine.CPUSet
		wantDeferred bool
	}{
		{
			name:         "primary internal relocation",
			current:      machine.NewCPUSet(7, 11, 55, 59),
			target:       machine.NewCPUSet(0, 4, 48, 52),
			reclaim:      machine.NewCPUSet(1, 2, 3, 5, 6, 10),
			wantDeferred: true,
		},
		{
			name:         "missing desired business cpu",
			current:      machine.NewCPUSet(0, 4),
			target:       machine.NewCPUSet(0, 4, 48, 52),
			reclaim:      machine.NewCPUSet(1, 2, 3, 5, 6, 10),
			wantDeferred: false,
		},
		{
			name:         "current touches reclaim",
			current:      machine.NewCPUSet(1, 7, 11, 55),
			target:       machine.NewCPUSet(0, 4, 48, 52),
			reclaim:      machine.NewCPUSet(1, 2, 3, 5, 6, 10),
			wantDeferred: false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := &expectedCPUSetBuildResult{
				ExpectedByRel:     map[string]machine.CPUSet{rel: tc.target.Clone()},
				DeferredLeafByRel: map[string]machine.CPUSet{},
			}
			plugin := &CPUSetTopologyPlugin{cgroup: &fakeCgroupClient{
				cpus: map[string]machine.CPUSet{rel: tc.current.Clone()},
			}}

			plugin.reclassifyAdmissionDeferredLeaves(context.Background(), tc.reclaim, result)

			deferred, isDeferred := result.DeferredLeafByRel[rel]
			if isDeferred != tc.wantDeferred {
				t.Fatalf("deferred=%v, want %v; expected=%v deferred_map=%v",
					isDeferred, tc.wantDeferred, result.ExpectedByRel, result.DeferredLeafByRel)
			}
			if tc.wantDeferred {
				if !deferred.Equals(tc.target) {
					t.Fatalf("deferred target=%s, want %s", deferred.String(), tc.target.String())
				}
				if _, ok := result.ExpectedByRel[rel]; ok {
					t.Fatalf("deferred relocation remained synchronously required: %v", result.ExpectedByRel)
				}
				return
			}
			if expected, ok := result.ExpectedByRel[rel]; !ok || !expected.Equals(tc.target) {
				t.Fatalf("synchronous expected target=%v, want %s", result.ExpectedByRel, tc.target.String())
			}
		})
	}
}
