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

package cpusettopology

import (
	"context"
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// A same-Pod alias on one shared physical cgroup rel is safe only when the PodUID
// matches and the consensus CPUSet agrees; every other combination fails closed.
func TestContainerLifecycleProofsAllowSamePodCPUSetAliasAndBackfill(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"
	orders := [][]containerLifecycleProof{
		{
			{
				PodUID: "pod-a", ContainerName: "main", ContainerID: "container-main",
				RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
			},
			{
				PodUID: "pod-a", ContainerName: "sidecar", ContainerID: "container-sidecar",
				RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
			},
		},
		{
			{
				PodUID: "pod-a", ContainerName: "sidecar", ContainerID: "container-sidecar",
				RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
			},
			{
				PodUID: "pod-a", ContainerName: "main", ContainerID: "container-main",
				RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
			},
		},
	}
	pod := lifecycleProofPod("pod-a", []string{"main", "sidecar"}, map[string]string{
		"main": "container-main", "sidecar": "container-sidecar",
	})
	for _, proofs := range orders {
		frozen, err := freezeContainerLifecycleProofs(proofs, map[string]*v1.Pod{"pod-a": pod})
		if err != nil {
			t.Fatalf("freezeContainerLifecycleProofs() error = %v, want safe alias accepted", err)
		}
		validated, err := validateContainerLifecycleProofs(
			context.Background(),
			&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &strictSnapshotFetcher{pods: []*v1.Pod{pod}}}},
			frozen,
		)
		if err != nil {
			t.Fatalf("validateContainerLifecycleProofs() error = %v", err)
		}
		published, err := containerCPUSetByPodFromFinalSnapshot(
			&topology.CompleteSnapshot{Entries: map[string]topology.EntryState{
				rel: {CPUs: machine.NewCPUSet(1, 2)},
			}}, validated, nil)
		if err != nil {
			t.Fatalf("containerCPUSetByPodFromFinalSnapshot() error = %v", err)
		}
		// The physical observed CPUSet is backfilled to every logical alias member.
		for _, containerName := range []string{"main", "sidecar"} {
			got := published["pod-a"][containerName]
			if !got.Equals(machine.NewCPUSet(1, 2)) {
				t.Fatalf("backfilled cpuset for %s = %s, want 1-2", containerName, got.String())
			}
		}
	}
}

func TestContainerLifecycleProofsRejectUnsafeAliases(t *testing.T) {
	t.Parallel()
	const sharedRel = "kubepods/shared-leaf"
	tests := []struct {
		name   string
		proofs []containerLifecycleProof
		pods   map[string]*v1.Pod
	}{
		{
			name: "same pod different desired cpuset",
			proofs: []containerLifecycleProof{
				{
					PodUID: "pod-a", ContainerName: "main", ContainerID: "container-main",
					RelativePath: sharedRel, DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecycleResolved,
				},
				{
					PodUID: "pod-a", ContainerName: "sidecar", ContainerID: "container-sidecar",
					RelativePath: sharedRel, DesiredCPUSet: machine.NewCPUSet(2), State: containerLifecycleResolved,
				},
			},
			pods: map[string]*v1.Pod{"pod-a": lifecycleProofPod("pod-a", []string{"main", "sidecar"},
				map[string]string{"main": "container-main", "sidecar": "container-sidecar"})},
		},
		{
			name: "different pod same desired cpuset",
			proofs: []containerLifecycleProof{
				{
					PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
					RelativePath: sharedRel, DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecycleResolved,
				},
				{
					PodUID: "pod-b", ContainerName: "main", ContainerID: "container-b",
					RelativePath: sharedRel, DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecycleResolved,
				},
			},
			pods: map[string]*v1.Pod{
				"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"}),
				"pod-b": lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "container-b"}),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := freezeContainerLifecycleProofs(tt.proofs, tt.pods)
			if err == nil || !strings.Contains(err.Error(), "relative path") {
				t.Fatalf("freeze error = %v, want relative path ownership conflict", err)
			}
		})
	}
}

// Distinct rels must stay independent: two containers on different paths never
// collide even within one Pod.
func TestContainerLifecycleProofsIndependentRels(t *testing.T) {
	t.Parallel()
	proofs := []containerLifecycleProof{
		{
			PodUID: "pod-a", ContainerName: "main", ContainerID: "container-main",
			RelativePath: "kubepods/pod-a/leaf-a", DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecycleResolved,
		},
		{
			PodUID: "pod-a", ContainerName: "sidecar", ContainerID: "container-sidecar",
			RelativePath: "kubepods/pod-a/leaf-b", DesiredCPUSet: machine.NewCPUSet(2), State: containerLifecycleResolved,
		},
	}
	pods := map[string]*v1.Pod{"pod-a": lifecycleProofPod("pod-a", []string{"main", "sidecar"},
		map[string]string{"main": "container-main", "sidecar": "container-sidecar"})}
	if _, err := freezeContainerLifecycleProofs(proofs, pods); err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = %v, want independent rels accepted", err)
	}
}

func TestExpectedCPUSetOwnerBinderAllowsSamePodAlias(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"
	binder := expectedCPUSetOwnerBinder{ownerByRel: make(map[string]expectedCPUSetOwner)}
	target := make(map[string]machine.CPUSet)
	for _, containerName := range []string{"main", "sidecar"} {
		err := binder.bind(target, resolvedContainerCPUSet{
			PodUID: "pod-a", ContainerName: containerName, Rel: rel, CPUs: machine.NewCPUSet(1, 2),
		})
		if err != nil {
			t.Fatalf("bind alias %s error = %v", containerName, err)
		}
	}
	if got := target[rel]; !got.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("bound cpuset = %s, want 1-2", got.String())
	}
}

// The binder persists the consensus CPUSet it recorded, so an alias bound into the
// deferred map still agrees with an owner bound into the expected map and vice
// versa; the decision must not read CPUSet from whichever target map is current.
func TestExpectedCPUSetOwnerBinderCrossMapAlias(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"

	cases := []struct {
		name          string
		firstDeferred bool
	}{
		{name: "owner deferred, alias expected", firstDeferred: true},
		{name: "owner expected, alias deferred", firstDeferred: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			binder := expectedCPUSetOwnerBinder{ownerByRel: make(map[string]expectedCPUSetOwner)}
			deferred := make(map[string]machine.CPUSet)
			expected := make(map[string]machine.CPUSet)
			first, second := expected, deferred
			if tc.firstDeferred {
				first, second = deferred, expected
			}
			if err := binder.bind(first, resolvedContainerCPUSet{
				PodUID: "pod-a", ContainerName: "main", Rel: rel, CPUs: machine.NewCPUSet(1, 2),
			}); err != nil {
				t.Fatalf("first bind error = %v", err)
			}
			if err := binder.bind(second, resolvedContainerCPUSet{
				PodUID: "pod-a", ContainerName: "sidecar", Rel: rel, CPUs: machine.NewCPUSet(1, 2),
			}); err != nil {
				t.Fatalf("cross-map alias bind error = %v, want accepted", err)
			}
		})
	}
}

func TestExpectedCPUSetOwnerBinderRejectsUnsafeAliases(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/shared-leaf"
	unsafe := []resolvedContainerCPUSet{
		{PodUID: "pod-a", ContainerName: "sidecar", Rel: rel, CPUs: machine.NewCPUSet(2)},
		{PodUID: "pod-b", ContainerName: "main", Rel: rel, CPUs: machine.NewCPUSet(1)},
	}
	for _, alias := range unsafe {
		binder := expectedCPUSetOwnerBinder{ownerByRel: make(map[string]expectedCPUSetOwner)}
		target := make(map[string]machine.CPUSet)
		if err := binder.bind(target, resolvedContainerCPUSet{
			PodUID: "pod-a", ContainerName: "main", Rel: rel, CPUs: machine.NewCPUSet(1),
		}); err != nil {
			t.Fatalf("bind initial owner error = %v", err)
		}
		if err := binder.bind(target, alias); err == nil {
			t.Fatalf("bind unsafe alias %#v succeeded, want ownership conflict", alias)
		}
	}
}

// A third safe alias still binds; a conflicting owner after safe aliases fails closed.
func TestExpectedCPUSetOwnerBinderThirdAliasAndConflict(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"
	binder := expectedCPUSetOwnerBinder{ownerByRel: make(map[string]expectedCPUSetOwner)}
	target := make(map[string]machine.CPUSet)
	for _, containerName := range []string{"main", "sidecar", "third"} {
		if err := binder.bind(target, resolvedContainerCPUSet{
			PodUID: "pod-a", ContainerName: containerName, Rel: rel, CPUs: machine.NewCPUSet(1, 2),
		}); err != nil {
			t.Fatalf("bind safe alias %s error = %v", containerName, err)
		}
	}
	if err := binder.bind(target, resolvedContainerCPUSet{
		PodUID: "pod-b", ContainerName: "main", Rel: rel, CPUs: machine.NewCPUSet(1),
	}); err == nil {
		t.Fatal("bind cross-pod conflict succeeded, want ownership conflict")
	}
}

// A missing resolved alias leaf surfaces as a stale error rather than publishing
// a fabricated CPUSet.
func TestAliasMissingResolvedLeafStale(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"
	proofs := []containerLifecycleProof{
		{
			PodUID: "pod-a", ContainerName: "main", ContainerID: "container-main",
			RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
		},
		{
			PodUID: "pod-a", ContainerName: "sidecar", ContainerID: "container-sidecar",
			RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
		},
	}
	pod := lifecycleProofPod("pod-a", []string{"main", "sidecar"}, map[string]string{
		"main": "container-main", "sidecar": "container-sidecar",
	})
	frozen, err := freezeContainerLifecycleProofs(proofs, map[string]*v1.Pod{"pod-a": pod})
	if err != nil {
		t.Fatalf("freeze error = %v", err)
	}
	validated, err := validateContainerLifecycleProofs(
		context.Background(),
		&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &strictSnapshotFetcher{pods: []*v1.Pod{pod}}}},
		frozen,
	)
	if err != nil {
		t.Fatalf("validate error = %v", err)
	}
	if _, err := containerCPUSetByPodFromFinalSnapshot(
		&topology.CompleteSnapshot{}, validated, nil); err == nil {
		t.Fatal("missing resolved leaf published, want stale error")
	}
}

// Restart re-validates frozen alias proofs against a fresh strict snapshot with no
// persisted witness; stable resolved identities still pass.
func TestAliasRestartRevalidation(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"
	proofs := []containerLifecycleProof{
		{
			PodUID: "pod-a", ContainerName: "main", ContainerID: "container-main",
			RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
		},
		{
			PodUID: "pod-a", ContainerName: "sidecar", ContainerID: "container-sidecar",
			RelativePath: rel, DesiredCPUSet: machine.NewCPUSet(1, 2), State: containerLifecycleResolved,
		},
	}
	pod := lifecycleProofPod("pod-a", []string{"main", "sidecar"}, map[string]string{
		"main": "container-main", "sidecar": "container-sidecar",
	})
	frozen, err := freezeContainerLifecycleProofs(proofs, map[string]*v1.Pod{"pod-a": pod})
	if err != nil {
		t.Fatalf("freeze error = %v", err)
	}
	// A fresh strict snapshot on a restart must still validate every alias proof.
	validated, err := validateContainerLifecycleProofs(
		context.Background(),
		&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: &strictSnapshotFetcher{pods: []*v1.Pod{pod.DeepCopy()}}}},
		frozen,
	)
	if err != nil {
		t.Fatalf("restart revalidation error = %v", err)
	}
	if len(validated.OrderedProofs) != 2 {
		t.Fatalf("validated proofs = %d, want 2", len(validated.OrderedProofs))
	}
}

// The disabled reset must protect the shared alias leaf from Expected, Deferred,
// and proof rels alike; it never writes a protected leaf.
func TestDisabledResetProtectsSharedAliasLeaf(t *testing.T) {
	t.Parallel()
	const rel = "kubepods/pod-a/kata-sandbox"
	res := &expectedCPUSetBuildResult{
		ExpectedByRel:     map[string]machine.CPUSet{rel: machine.NewCPUSet(1, 2)},
		DeferredLeafByRel: map[string]machine.CPUSet{},
	}
	protected := protectedLeafRels(res)
	if _, ok := protected[rel]; !ok {
		t.Fatalf("shared alias leaf %q missing from protected leaves", rel)
	}
}
