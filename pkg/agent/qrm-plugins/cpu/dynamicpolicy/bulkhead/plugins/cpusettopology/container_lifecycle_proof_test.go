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
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func registerRelativeCgroupPathHandlerForTest(t *testing.T, handler cgcommon.RelativeCgroupPathHandler) {
	t.Helper()
	t.Cleanup(cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(handler))
}

func TestValidateContainerLifecycleProofsEmptyUsesOneStrictSnapshotAndReturnsCanonicalSet(t *testing.T) {
	t.Parallel()
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "empty-proof-validation-must-not-resolve",
		Handler: func(_, _ string) (string, bool, error) {
			// Empty proof sets own no pod/container; skip foreign parallel tests so
			// this guard only backstops this test's own (pod-free) flow.
			return "", true, nil
		},
	})
	fetcher := &strictSnapshotFetcher{}
	validated, err := validateContainerLifecycleProofs(
		context.Background(),
		&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
		containerLifecycleProofSet{SourceSnapshotDigest: "non-canonical"},
	)
	if err != nil {
		t.Fatalf("validateContainerLifecycleProofs() error = %v", err)
	}
	if fetcher.listCalls != 1 {
		t.Fatalf("strict GetPodList calls = %d, want 1", fetcher.listCalls)
	}
	if fetcher.getCalls != 0 || fetcher.refreshCalls != 0 {
		t.Fatalf("per-pod/refresh calls = %d/%d, want 0/0", fetcher.getCalls, fetcher.refreshCalls)
	}
	canonicalDigest, err := lifecycleSourceSnapshotDigest(
		map[string]struct{}{}, map[string]*v1.Pod{})
	if err != nil {
		t.Fatalf("lifecycleSourceSnapshotDigest() error = %v", err)
	}
	if validated.SourceSnapshotDigest != canonicalDigest ||
		validated.ValidationDigest != canonicalDigest ||
		validated.OrderedProofs == nil ||
		len(validated.OrderedProofs) != 0 {
		t.Fatalf("validated empty proof set = %#v, want canonical digest %q and non-nil empty proofs",
			validated, canonicalDigest)
	}
}

func TestValidateContainerLifecycleProofsEmptyPreservesStrictSnapshotErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "transport", err: errors.New("strict snapshot transport failed")},
		{name: "decode", err: errors.New("strict snapshot decode failed")},
		{name: "cancel", err: context.Canceled},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fetcher := &strictSnapshotFetcher{listErr: tt.err}
			_, err := validateContainerLifecycleProofs(
				context.Background(),
				&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
				containerLifecycleProofSet{},
			)
			if err != tt.err {
				t.Fatalf("validation error = %v, want original error %v", err, tt.err)
			}
			if fetcher.listCalls != 1 {
				t.Fatalf("strict GetPodList calls = %d, want 1", fetcher.listCalls)
			}
			if fetcher.getCalls != 0 || fetcher.refreshCalls != 0 {
				t.Fatalf("per-pod/refresh calls = %d/%d, want 0/0", fetcher.getCalls, fetcher.refreshCalls)
			}
		})
	}
}

func TestValidateContainerLifecycleProofsUsesOneStrictSnapshot(t *testing.T) {
	t.Parallel()
	fetcher := &strictSnapshotFetcher{
		pods: []*v1.Pod{lifecycleProofPod(
			"pod-a", []string{"main"}, map[string]string{"main": "container-a"})},
	}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}
	frozen := lifecycleProofSetForValidation(t, []containerLifecycleProof{{
		PodUID:        "pod-a",
		ContainerName: "main",
		ContainerID:   "container-a",
		RelativePath:  "primary/container-a",
		DesiredCPUSet: machine.NewCPUSet(1),
		State:         containerLifecycleResolved,
	}}, fetcher.pods)

	validated, err := validateContainerLifecycleProofs(context.Background(), metaServer, frozen)
	if err != nil {
		t.Fatalf("validateContainerLifecycleProofs() error = %v", err)
	}
	if fetcher.listCalls != 1 {
		t.Fatalf("strict GetPodList calls = %d, want 1", fetcher.listCalls)
	}
	if fetcher.getCalls != 0 || fetcher.refreshCalls != 0 {
		t.Fatalf("per-pod/refresh calls = %d/%d, want 0/0", fetcher.getCalls, fetcher.refreshCalls)
	}
	if len(validated.OrderedProofs) != 1 || validated.ValidationDigest == "" {
		t.Fatalf("validated proof set = %#v, want one proof and validation digest", validated)
	}
}

func TestValidateContainerLifecycleProofsAcceptsStableResolvedProof(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
		RelativePath: "primary/container-a", DesiredCPUSet: machine.NewCPUSet(1),
		State: containerLifecycleResolved,
	}
	pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})
	validated := validateLifecycleProofSetForTest(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
	if !reflect.DeepEqual(validated.OrderedProofs, []containerLifecycleProof{proof}) {
		t.Fatalf("validated proofs = %#v, want %#v", validated.OrderedProofs, []containerLifecycleProof{proof})
	}
}

func TestValidateContainerLifecycleProofsOmitsConfirmedRetiredProof(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "old", ContainerID: "container-old",
		DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecycleRetired,
	}
	pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})
	validated := validateLifecycleProofSetForTest(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
	got, err := containerCPUSetByPodFromFinalSnapshot(
		&topology.CompleteSnapshot{Entries: map[string]topology.EntryState{}},
		validated, nil)
	if err != nil {
		t.Fatalf("containerCPUSetByPodFromFinalSnapshot() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("retired proof published = %#v, want omitted", got)
	}
}

func TestValidateContainerLifecycleProofsKeepsStablePendingProofDeferred(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		frozenID   string
		currentIDs map[string]string
	}{
		{name: "stable id", frozenID: "container-a", currentIDs: map[string]string{"main": "container-a"}},
		{name: "stable absence", currentIDs: nil},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proof := containerLifecycleProof{
				PodUID: "pod-a", ContainerName: "main", ContainerID: tt.frozenID,
				DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecyclePending,
			}
			pod := lifecycleProofPod("pod-a", []string{"main"}, tt.currentIDs)
			validated := validateLifecycleProofSetForTest(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
			got, err := containerCPUSetByPodFromFinalSnapshot(
				&topology.CompleteSnapshot{Entries: map[string]topology.EntryState{}},
				validated, nil)
			if err != nil {
				t.Fatalf("containerCPUSetByPodFromFinalSnapshot() error = %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("pending proof published = %#v, want deferred", got)
			}
		})
	}
}

func TestValidateContainerLifecycleProofsRejectsContainerIDChange(t *testing.T) {
	t.Parallel()
	for _, state := range []containerLifecycleState{containerLifecycleResolved, containerLifecyclePending} {
		state := state
		t.Run(fmt.Sprintf("state-%d", state), func(t *testing.T) {
			t.Parallel()
			proof := containerLifecycleProof{
				PodUID: "pod-a", ContainerName: "main", ContainerID: "container-old",
				DesiredCPUSet: machine.NewCPUSet(1), State: state,
			}
			if state == containerLifecycleResolved {
				proof.RelativePath = "primary/container-old"
			}
			pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-new"})
			_, err := validateLifecycleProofSetWithPods([]containerLifecycleProof{proof}, []*v1.Pod{pod})
			var stale *containerLifecycleProofStaleError
			if !errors.As(err, &stale) || !errors.Is(err, topology.ErrCoordinatorPlanStale) {
				t.Fatalf("validation error = %v, want typed lifecycle stale wrapping coordinator stale", err)
			}
		})
	}
}

func TestValidateContainerLifecycleProofsRejectsRetiredOwnerReappearance(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "old", ContainerID: "container-old",
		DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecycleRetired,
	}
	pod := lifecycleProofPod("pod-a", []string{"old"}, map[string]string{"old": "container-new"})
	_, err := validateLifecycleProofSetWithPods([]containerLifecycleProof{proof}, []*v1.Pod{pod})
	var stale *containerLifecycleProofStaleError
	if !errors.As(err, &stale) || !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("validation error = %v, want typed lifecycle stale wrapping coordinator stale", err)
	}
}

func TestValidateContainerLifecycleProofsKeepsMaterializedPendingProofDeferred(t *testing.T) {
	t.Parallel()
	const unexpectedRel = "primary/container-a"
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "pending-finalization-must-not-resolve",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == "pod-a" && gotContainerID == "container-a" {
				t.Fatal("finalization resolved a pending cgroup path")
			}
			return "", true, nil
		},
	})
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
		DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecyclePending,
	}
	pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})
	validated := validateLifecycleProofSetForTest(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
	got, err := containerCPUSetByPodFromFinalSnapshot(
		&topology.CompleteSnapshot{Entries: map[string]topology.EntryState{
			unexpectedRel: {CPUs: machine.NewCPUSet(1)},
		}}, validated, nil)
	if err != nil {
		t.Fatalf("containerCPUSetByPodFromFinalSnapshot() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("materialized pending proof published = %#v, want deferred", got)
	}
}

func TestValidateContainerLifecycleProofsPreservesSnapshotTransportError(t *testing.T) {
	t.Parallel()
	transportErr := errors.New("strict snapshot transport failed")
	fetcher := &strictSnapshotFetcher{listErr: transportErr}
	_, err := validateContainerLifecycleProofs(
		context.Background(),
		&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
		containerLifecycleProofSet{OrderedProofs: []containerLifecycleProof{{
			PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
			RelativePath: "primary/container-a", DesiredCPUSet: machine.NewCPUSet(1),
			State: containerLifecycleResolved,
		}}},
	)
	if err != transportErr {
		t.Fatalf("validation error = %v, want original transport error %v", err, transportErr)
	}
	if fetcher.listCalls != 1 {
		t.Fatalf("strict GetPodList calls = %d, want 1", fetcher.listCalls)
	}
}

func TestContainerCPUSetByPodFromFinalSnapshotRejectsMissingResolvedLeafAsStale(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
		RelativePath: "primary/container-a", DesiredCPUSet: machine.NewCPUSet(1),
		State: containerLifecycleResolved,
	}
	pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})
	validated := validateLifecycleProofSetForTest(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
	_, err := containerCPUSetByPodFromFinalSnapshot(
		&topology.CompleteSnapshot{Entries: map[string]topology.EntryState{}},
		validated, nil)
	var stale *containerLifecycleProofStaleError
	if !errors.As(err, &stale) || !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("materialization error = %v, want typed lifecycle stale", err)
	}
}

func TestResolvedFinalLeafControllerUnavailableRemainsHardError(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
		RelativePath: "primary/container-a", DesiredCPUSet: machine.NewCPUSet(1),
		State: containerLifecycleResolved,
	}
	pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})
	validated := validateLifecycleProofSetForTest(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
	snapshot := &topology.CompleteSnapshot{
		Entries: map[string]topology.EntryState{},
		UnavailableChildren: map[string]topology.UnavailableChildEvidence{
			proof.RelativePath: {
				Identity: topology.CgroupIdentity{Device: 7, Inode: 11},
				Reason:   topology.UnavailableChildReasonControllerUnavailable,
			},
		},
	}

	_, err := containerCPUSetByPodFromFinalSnapshot(snapshot, validated, nil)
	var stale *containerLifecycleProofStaleError
	if err == nil || errors.As(err, &stale) || errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("materialization error = %v, want non-stale unavailable-leaf error", err)
	}
	if got := err.Error(); got != "resolved final leaf \"primary/container-a\" is unavailable: reason=controller-unavailable identity={7 11}" {
		t.Fatalf("materialization error = %q, want unavailable identity evidence", got)
	}
}

func TestAppliedViewFromFinalSnapshotRejectsMissingResolvedLeafAsStale(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID: "pod-a", ContainerName: "main", ContainerID: "container-a",
		RelativePath: "primary/container-a", DesiredCPUSet: machine.NewCPUSet(1),
		State: containerLifecycleResolved,
	}
	pod := lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})
	frozen := lifecycleProofSetForValidation(t, []containerLifecycleProof{proof}, []*v1.Pod{pod})
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
		PodFetcher: &strictSnapshotFetcher{pods: []*v1.Pod{pod}},
	}}
	dag, err := topology.BuildDAG([]topology.NodeSpec{{
		Rel: "primary", Role: topology.TopoNodeRolePrimary, Domain: topology.DomainPrimary,
		CPUs: machine.NewCPUSet(0, 1), ControlledRoot: true, TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	snapshot := &topology.CompleteSnapshot{Entries: map[string]topology.EntryState{
		"primary": {
			Rel: "primary", Identity: topology.CgroupIdentity{Device: 7, Inode: 10},
			CPUs: machine.NewCPUSet(0, 1), ConfiguredCPUs: machine.NewCPUSet(0, 1),
		},
	}}

	_, err = appliedViewFromFinalSnapshotWithDeferredCleanup(
		context.Background(), metaServer, model.NewDesiredView(), dag, snapshot, nil, frozen)
	var stale *containerLifecycleProofStaleError
	if !errors.As(err, &stale) || !errors.Is(err, topology.ErrCoordinatorPlanStale) {
		t.Fatalf("publication error = %v, want typed lifecycle stale", err)
	}
	if stale.PodUID != proof.PodUID || stale.ContainerName != proof.ContainerName ||
		stale.FrozenID != proof.ContainerID || stale.Reason != "resolved final leaf is missing" {
		t.Fatalf("publication stale = %#v, want missing resolved leaf identity", stale)
	}
}

func lifecycleProofSetForValidation(
	t *testing.T,
	proofs []containerLifecycleProof,
	pods []*v1.Pod,
) containerLifecycleProofSet {
	t.Helper()
	byUID := make(map[string]*v1.Pod, len(pods))
	for _, pod := range pods {
		byUID[string(pod.UID)] = pod
	}
	frozen, err := freezeContainerLifecycleProofs(proofs, byUID)
	if err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = %v", err)
	}
	return frozen
}

func validateLifecycleProofSetForTest(
	t *testing.T,
	proofs []containerLifecycleProof,
	pods []*v1.Pod,
) validatedContainerLifecycleProofSet {
	t.Helper()
	validated, err := validateLifecycleProofSetWithPods(proofs, pods)
	if err != nil {
		t.Fatalf("validateContainerLifecycleProofs() error = %v", err)
	}
	return validated
}

func validateLifecycleProofSetWithPods(
	proofs []containerLifecycleProof,
	pods []*v1.Pod,
) (validatedContainerLifecycleProofSet, error) {
	fetcher := &strictSnapshotFetcher{pods: pods}
	frozenByUID := make(map[string]*v1.Pod, len(pods))
	for _, pod := range pods {
		frozenByUID[string(pod.UID)] = pod
	}
	frozen, err := freezeContainerLifecycleProofs(proofs, frozenByUID)
	if err != nil {
		// Stale-case test fixtures intentionally freeze the old identity.
		for _, proof := range proofs {
			frozenByUID[proof.PodUID] = lifecycleProofPod(
				proof.PodUID, []string{proof.ContainerName},
				map[string]string{proof.ContainerName: proof.ContainerID})
			if proof.State == containerLifecycleRetired {
				frozenByUID[proof.PodUID] = lifecycleProofPod(proof.PodUID, nil, nil)
			}
		}
		frozen, err = freezeContainerLifecycleProofs(proofs, frozenByUID)
	}
	if err != nil {
		return validatedContainerLifecycleProofSet{}, err
	}
	return validateContainerLifecycleProofs(
		context.Background(),
		&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
		frozen,
	)
}

func TestBuildExpectedCPUSetByRelCarriesResolvedPendingAndRetiredProofs(t *testing.T) {
	t.Parallel()
	const podUID = "lifecycle-proof-pod"
	const resolvedRel = "kubepods/podlifecycle-proof-pod/resolved-id"

	unregister := cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(cgcommon.RelativeCgroupPathHandler{
		Name: "lifecycle-proof",
		Handler: func(gotPodUID, containerID string) (string, bool, error) {
			if gotPodUID != podUID {
				return "", true, nil
			}
			if containerID == "resolved-id" {
				return "/" + resolvedRel, false, nil
			}
			return "", false, os.ErrNotExist
		},
	})
	t.Cleanup(unregister)

	cached := lifecycleProofPod(podUID,
		[]string{"resolved", "pending", "retired"},
		map[string]string{
			"resolved": "resolved-id",
			"pending":  "pending-id",
			"retired":  "retired-id",
		})
	fresh := lifecycleProofPod(podUID,
		[]string{"resolved", "pending"},
		map[string]string{
			"resolved": "resolved-id",
			"pending":  "",
		})
	fetcher := &cacheThenFreshSnapshotFetcher{
		cachedPods: []*v1.Pod{cached},
		freshPods:  []*v1.Pod{fresh},
	}
	desired := map[string]map[string]machine.CPUSet{
		podUID: {
			"retired":  machine.NewCPUSet(4),
			"resolved": machine.NewCPUSet(2),
			"pending":  machine.NewCPUSet(3),
			"empty":    machine.NewCPUSet(),
		},
	}
	result, err := (&CPUSetTopologyPlugin{
		cfg:    bulkheadconfig.BulkheadConfiguration{BulkheadPrimaryRelPath: "kubepods"},
		cgroup: &fakeCgroupClient{existing: map[string]bool{resolvedRel: true}},
	}).buildExpectedCPUSetByRel(context.Background(), bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}},
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			ContainerCPUSetByPod: desired,
		}},
	})
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if fetcher.freshCalls != 1 {
		t.Fatalf("strict fresh snapshot calls = %d, want 1", fetcher.freshCalls)
	}
	if got := len(result.PendingByPod); got != 1 {
		t.Fatalf("pending entries = %d, want prefix-only fresh identity to remain pending: %#v",
			got, result.PendingByPod)
	}
	if got := len(result.LifecycleProofs.OrderedProofs); got != 3 {
		t.Fatalf("proof count = %d, want one per non-empty desired entry: %#v",
			got, result.LifecycleProofs.OrderedProofs)
	}
	gotStates := map[string]containerLifecycleState{}
	for _, proof := range result.LifecycleProofs.OrderedProofs {
		if _, duplicate := gotStates[proof.ContainerName]; duplicate {
			t.Fatalf("container %q classified more than once", proof.ContainerName)
		}
		gotStates[proof.ContainerName] = proof.State
	}
	wantStates := map[string]containerLifecycleState{
		"resolved": containerLifecycleResolved,
		"pending":  containerLifecyclePending,
		"retired":  containerLifecycleRetired,
	}
	if !reflect.DeepEqual(gotStates, wantStates) {
		t.Fatalf("lifecycle states = %#v, want %#v", gotStates, wantStates)
	}
}

func TestBuildExpectedCPUSetByRelProofStateComesOnlyFromFreshOwnerSnapshot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		freshOwners       []string
		cachedIDs         map[string]string
		desired           map[string]machine.CPUSet
		existingCandidate bool
		existingOldLeaf   bool
		existingOtherLeaf bool
		wantExpected      int
		wantPending       int
	}{
		{
			name:              "pod absent with materialized candidate scope",
			desired:           map[string]machine.CPUSet{"main": machine.NewCPUSet(1)},
			existingCandidate: true,
			wantPending:       1,
		},
		{
			name:              "pod absent with another resolved leaf",
			cachedIDs:         map[string]string{"sidecar": "sidecar-id"},
			desired:           map[string]machine.CPUSet{"main": machine.NewCPUSet(1), "sidecar": machine.NewCPUSet(2)},
			existingOtherLeaf: true,
			wantExpected:      1,
			wantPending:       1,
		},
		{
			name:              "name absent with materialized candidate scope",
			freshOwners:       []string{"sidecar"},
			desired:           map[string]machine.CPUSet{"main": machine.NewCPUSet(1)},
			existingCandidate: true,
		},
		{
			name:            "name absent with old resolved leaf",
			freshOwners:     []string{"sidecar"},
			cachedIDs:       map[string]string{"main": "old-id"},
			desired:         map[string]machine.CPUSet{"main": machine.NewCPUSet(1)},
			existingOldLeaf: true,
			wantExpected:    1,
		},
	}

	for i, tt := range tests {
		i := i
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podUID := fmt.Sprintf("proof-owner-snapshot-%d", i)
			oldRel := "kubepods/pod" + podUID + "/old-id"
			otherRel := "kubepods/pod" + podUID + "/sidecar-id"
			candidateRel := "kubepods/pod" + podUID
			unregister := cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(cgcommon.RelativeCgroupPathHandler{
				Name: "proof-owner-snapshot-" + podUID,
				Handler: func(gotPodUID, containerID string) (string, bool, error) {
					if gotPodUID == podUID && containerID == "old-id" {
						return "/" + oldRel, false, nil
					}
					if gotPodUID == podUID && containerID == "sidecar-id" {
						return "/" + otherRel, false, nil
					}
					return "", true, nil
				},
			})
			t.Cleanup(unregister)

			cachedOwners := make([]string, 0, len(tt.desired))
			for name := range tt.desired {
				cachedOwners = append(cachedOwners, name)
			}
			fetcher := &cacheThenFreshSnapshotFetcher{
				cachedPods: []*v1.Pod{lifecycleProofPod(podUID, cachedOwners, tt.cachedIDs)},
			}
			if tt.freshOwners != nil {
				fetcher.freshPods = []*v1.Pod{lifecycleProofPod(podUID, tt.freshOwners, nil)}
			}
			cg := &fakeCgroupClient{existing: map[string]bool{
				candidateRel: tt.existingCandidate,
				oldRel:       tt.existingOldLeaf,
				otherRel:     tt.existingOtherLeaf,
			}}
			result, err := (&CPUSetTopologyPlugin{
				cfg:    bulkheadConfigWithPrimary("kubepods"),
				cgroup: cg,
			}).buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher,
				&model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
					ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{podUID: tt.desired},
				}},
			))
			if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if got := len(result.ExpectedByRel); got != tt.wantExpected {
				t.Fatalf("physical expected leaves = %d, want %d: %#v", got, tt.wantExpected, result.ExpectedByRel)
			}
			if got := len(result.PendingByPod); got != tt.wantPending {
				t.Fatalf("physical pending entries = %d, want %d: %#v", got, tt.wantPending, result.PendingByPod)
			}
			if got, want := len(result.LifecycleProofs.OrderedProofs), len(tt.desired); got != want {
				t.Fatalf("proof count = %d, want exactly one per desired entry (%d): %#v",
					got, want, result.LifecycleProofs.OrderedProofs)
			}
			for _, proof := range result.LifecycleProofs.OrderedProofs {
				if proof.State != containerLifecycleRetired {
					t.Fatalf("proof %s/%s state = %v, want retired from strict fresh owner snapshot",
						proof.PodUID, proof.ContainerName, proof.State)
				}
			}
		})
	}
}

func TestBuildExpectedCPUSetByRelOrdersLifecycleProofsDeterministically(t *testing.T) {
	t.Parallel()
	proofs := []containerLifecycleProof{
		{PodUID: "pod-b", ContainerName: "z", ContainerID: "2", State: containerLifecyclePending, DesiredCPUSet: machine.NewCPUSet(3)},
		{PodUID: "pod-a", ContainerName: "z", ContainerID: "1", RelativePath: "r/1", State: containerLifecycleResolved, DesiredCPUSet: machine.NewCPUSet(2)},
		{PodUID: "pod-a", ContainerName: "a", ContainerID: "3", RelativePath: "r/3", State: containerLifecycleRetired, DesiredCPUSet: machine.NewCPUSet(1)},
	}
	frozen, err := freezeContainerLifecycleProofs(proofs, map[string]*v1.Pod{
		"pod-b": lifecycleProofPod("pod-b", []string{"z"}, map[string]string{"z": "2"}),
		"pod-a": lifecycleProofPod("pod-a", []string{"z"}, map[string]string{"z": "1"}),
	})
	if err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = %v", err)
	}
	got := make([]string, 0, len(frozen.OrderedProofs))
	for _, proof := range frozen.OrderedProofs {
		got = append(got, proof.PodUID+"/"+proof.ContainerName)
	}
	want := []string{"pod-a/a", "pod-a/z", "pod-b/z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered proofs = %v, want %v", got, want)
	}
}

func TestFreezeContainerLifecycleProofsRejectsInvalidProofs(t *testing.T) {
	t.Parallel()
	valid := containerLifecycleProof{
		PodUID:        "pod-a",
		ContainerName: "main",
		ContainerID:   "container-a",
		RelativePath:  "kubepods/pod-a/container-a",
		DesiredCPUSet: machine.NewCPUSet(1),
		State:         containerLifecycleResolved,
	}
	tests := []struct {
		name   string
		mutate func(*containerLifecycleProof)
	}{
		{
			name: "empty pod UID",
			mutate: func(proof *containerLifecycleProof) {
				proof.PodUID = ""
			},
		},
		{
			name: "empty container name",
			mutate: func(proof *containerLifecycleProof) {
				proof.ContainerName = ""
			},
		},
		{
			name: "invalid state",
			mutate: func(proof *containerLifecycleProof) {
				proof.State = containerLifecycleState(255)
			},
		},
		{
			name: "resolved without container ID",
			mutate: func(proof *containerLifecycleProof) {
				proof.ContainerID = ""
			},
		},
		{
			name: "resolved without relative path",
			mutate: func(proof *containerLifecycleProof) {
				proof.RelativePath = ""
			},
		},
		{
			name: "pending with relative path",
			mutate: func(proof *containerLifecycleProof) {
				proof.State = containerLifecyclePending
			},
		},
		{
			name: "pending with stale identity",
			mutate: func(proof *containerLifecycleProof) {
				proof.State = containerLifecyclePending
				proof.ContainerID = "stale-container"
				proof.RelativePath = ""
			},
		},
		{
			name: "retired while fresh owner exists",
			mutate: func(proof *containerLifecycleProof) {
				proof.State = containerLifecycleRetired
				proof.ContainerID = ""
				proof.RelativePath = ""
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proof := valid.clone()
			tt.mutate(&proof)
			if _, err := freezeContainerLifecycleProofs(
				[]containerLifecycleProof{proof},
				map[string]*v1.Pod{"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})},
			); err == nil {
				t.Fatalf("freezeContainerLifecycleProofs() error = nil, want invalid proof rejection")
			}
		})
	}
}

func TestFreezeContainerLifecycleProofsRejectsDuplicateLogicalKey(t *testing.T) {
	t.Parallel()
	proof := containerLifecycleProof{
		PodUID:        "pod-a",
		ContainerName: "main",
		ContainerID:   "container-a",
		RelativePath:  "kubepods/pod-a/container-a",
		DesiredCPUSet: machine.NewCPUSet(1),
		State:         containerLifecycleResolved,
	}
	if _, err := freezeContainerLifecycleProofs(
		[]containerLifecycleProof{proof, proof.clone()},
		map[string]*v1.Pod{"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "container-a"})},
	); err == nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = nil, want duplicate logical key rejection")
	}
}

func TestFreezeContainerLifecycleProofsRejectsPhysicalIdentityAliasesInEitherOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		proofs []containerLifecycleProof
		pods   map[string]*v1.Pod
	}{
		{
			name: "resolved relative path",
			proofs: []containerLifecycleProof{
				{
					PodUID: "pod-a", ContainerName: "main", ContainerID: "id-a",
					RelativePath: "kubepods/shared-leaf", DesiredCPUSet: machine.NewCPUSet(1),
					State: containerLifecycleResolved,
				},
				{
					PodUID: "pod-b", ContainerName: "main", ContainerID: "id-b",
					RelativePath: "kubepods/shared-leaf", DesiredCPUSet: machine.NewCPUSet(2),
					State: containerLifecycleResolved,
				},
			},
			pods: map[string]*v1.Pod{
				"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "id-a"}),
				"pod-b": lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "id-b"}),
			},
		},
		{
			name: "fresh container ID",
			proofs: []containerLifecycleProof{
				{
					PodUID: "pod-a", ContainerName: "main", ContainerID: "shared-id",
					RelativePath: "kubepods/pod-a/shared-id", DesiredCPUSet: machine.NewCPUSet(1),
					State: containerLifecycleResolved,
				},
				{
					PodUID: "pod-b", ContainerName: "main", ContainerID: "shared-id",
					RelativePath: "kubepods/pod-b/shared-id", DesiredCPUSet: machine.NewCPUSet(2),
					State: containerLifecycleResolved,
				},
			},
			pods: map[string]*v1.Pod{
				"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "shared-id"}),
				"pod-b": lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "shared-id"}),
			},
		},
		{
			name: "pending fresh container ID",
			proofs: []containerLifecycleProof{
				{
					PodUID: "pod-a", ContainerName: "main", ContainerID: "shared-id",
					DesiredCPUSet: machine.NewCPUSet(1), State: containerLifecyclePending,
				},
				{
					PodUID: "pod-b", ContainerName: "main", ContainerID: "shared-id",
					DesiredCPUSet: machine.NewCPUSet(2), State: containerLifecyclePending,
				},
			},
			pods: map[string]*v1.Pod{
				"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "shared-id"}),
				"pod-b": lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "shared-id"}),
			},
		},
		{
			name: "active and retired physical generations share container ID",
			proofs: []containerLifecycleProof{
				{
					PodUID: "pod-a", ContainerName: "main", ContainerID: "shared-generation-id",
					RelativePath: "kubepods/pod-a/shared-generation-id", DesiredCPUSet: machine.NewCPUSet(1),
					State: containerLifecycleResolved,
				},
				{
					PodUID: "pod-b", ContainerName: "removed", ContainerID: "shared-generation-id",
					RelativePath: "kubepods/pod-b/shared-generation-id", DesiredCPUSet: machine.NewCPUSet(2),
					State: containerLifecycleRetired,
				},
			},
			pods: map[string]*v1.Pod{
				"pod-a": lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "shared-generation-id"}),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orders := [][]containerLifecycleProof{
				tt.proofs,
				{tt.proofs[1], tt.proofs[0]},
			}
			for i, proofs := range orders {
				if _, err := freezeContainerLifecycleProofs(proofs, tt.pods); err == nil {
					t.Fatalf("order %d freezeContainerLifecycleProofs() error = nil, want physical identity alias rejection", i)
				}
			}
		})
	}
}

func TestFreezeContainerLifecycleProofsAllowsPendingOwnersToSharePodScope(t *testing.T) {
	t.Parallel()
	const podUID = "pending-shared-scope"
	proofs := []containerLifecycleProof{
		{
			PodUID: podUID, ContainerName: "main", DesiredCPUSet: machine.NewCPUSet(1),
			State: containerLifecyclePending,
		},
		{
			PodUID: podUID, ContainerName: "sidecar", DesiredCPUSet: machine.NewCPUSet(2),
			State: containerLifecyclePending,
		},
	}
	if _, err := freezeContainerLifecycleProofs(
		proofs,
		map[string]*v1.Pod{podUID: lifecycleProofPod(podUID, []string{"main", "sidecar"}, nil)},
	); err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() rejected pending owners sharing pod scope: %v", err)
	}
}

func TestBuildExpectedCPUSetByRelRejectsResolvedRelativePathAlias(t *testing.T) {
	t.Parallel()
	const sharedRel = "kubepods/shared-leaf"
	unregister := cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(cgcommon.RelativeCgroupPathHandler{
		Name: "lifecycle-proof-relative-path-alias",
		Handler: func(podUID, containerID string) (string, bool, error) {
			if (podUID == "pod-a" || podUID == "pod-b") &&
				(containerID == "id-a" || containerID == "id-b") {
				return "/" + sharedRel, false, nil
			}
			return "", true, nil
		},
	})
	t.Cleanup(unregister)
	fetcher := &cacheThenFreshSnapshotFetcher{
		cachedPods: []*v1.Pod{
			lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "id-a"}),
			lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "id-b"}),
		},
		freshPods: []*v1.Pod{
			lifecycleProofPod("pod-a", []string{"main"}, map[string]string{"main": "id-a"}),
			lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "id-b"}),
		},
	}
	_, err := (&CPUSetTopologyPlugin{
		cfg:    bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{existing: map[string]bool{sharedRel: true}},
	}).buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
		fetcher,
		&model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				"pod-a": {"main": machine.NewCPUSet(1)},
				"pod-b": {"main": machine.NewCPUSet(2)},
			},
		}},
	))
	if err == nil {
		t.Fatal("buildExpectedCPUSetByRel() error = nil, want shared physical rel rejection")
	}
}

func TestBuildExpectedCPUSetByRelCanonicalizesEmptyProofDigest(t *testing.T) {
	t.Parallel()
	const emptyFramedDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	tests := []struct {
		name string
		in   bulkheadapi.HandlerContext
	}{
		{name: "nil desired view"},
		{
			name: "empty desired map",
			in: bulkheadapi.HandlerContext{
				DesiredView: &model.DesiredView{},
			},
		},
		{
			name: "all empty cpusets",
			in: pendingScopeTestContext(
				&cacheThenFreshSnapshotFetcher{},
				&model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
					ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
						"pod-a": {"main": machine.NewCPUSet()},
					},
				}},
			),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := (&CPUSetTopologyPlugin{}).buildExpectedCPUSetByRel(context.Background(), tt.in)
			if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if got := result.LifecycleProofs.SourceSnapshotDigest; got != emptyFramedDigest {
				t.Fatalf("empty proof digest = %q, want SHA256(empty framed input) %q", got, emptyFramedDigest)
			}
		})
	}
}

func TestBuildExpectedCPUSetByRelRejectsMissingMetaServerForNonEmptyDesired(t *testing.T) {
	t.Parallel()
	_, err := (&CPUSetTopologyPlugin{}).buildExpectedCPUSetByRel(
		context.Background(),
		bulkheadapi.HandlerContext{
			DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					"pod-a": {"main": machine.NewCPUSet(1)},
				},
			}},
		},
	)
	if err == nil {
		t.Fatal("buildExpectedCPUSetByRel() error = nil, want missing MetaServer to fail closed")
	}
}

func TestValidateLifecycleProofCoverageRejectsMutations(t *testing.T) {
	t.Parallel()
	desired := map[string]map[string]machine.CPUSet{
		"pod-a": {
			"main":  machine.NewCPUSet(1),
			"empty": machine.NewCPUSet(),
		},
	}
	valid := containerLifecycleProof{
		PodUID:        "pod-a",
		ContainerName: "main",
		ContainerID:   "container-a",
		RelativePath:  "kubepods/pod-a/container-a",
		DesiredCPUSet: machine.NewCPUSet(1),
		State:         containerLifecycleResolved,
	}
	tests := []struct {
		name   string
		proofs []containerLifecycleProof
	}{
		{name: "missing", proofs: nil},
		{name: "extra", proofs: []containerLifecycleProof{valid, {
			PodUID: "pod-extra", ContainerName: "main", State: containerLifecycleRetired,
			DesiredCPUSet: machine.NewCPUSet(2),
		}}},
		{name: "duplicate", proofs: []containerLifecycleProof{valid, valid.clone()}},
		{name: "mutated desired cpuset", proofs: []containerLifecycleProof{{
			PodUID: valid.PodUID, ContainerName: valid.ContainerName,
			ContainerID: valid.ContainerID, RelativePath: valid.RelativePath,
			DesiredCPUSet: machine.NewCPUSet(2), State: valid.State,
		}}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := validateLifecycleProofCoverage(desired, tt.proofs); err == nil {
				t.Fatalf("validateLifecycleProofCoverage() error = nil, want mutation rejection")
			}
		})
	}
	if err := validateLifecycleProofCoverage(desired, []containerLifecycleProof{valid}); err != nil {
		t.Fatalf("validateLifecycleProofCoverage() valid error = %v", err)
	}
}

func TestLifecycleProofDigestIgnoresInputMapOrder(t *testing.T) {
	t.Parallel()
	proofs := []containerLifecycleProof{
		{PodUID: "pod-a", ContainerName: "main", ContainerID: "id-a", State: containerLifecyclePending, DesiredCPUSet: machine.NewCPUSet(1)},
		{PodUID: "pod-b", ContainerName: "main", ContainerID: "id-b", State: containerLifecyclePending, DesiredCPUSet: machine.NewCPUSet(2)},
	}
	podA := lifecycleProofPod("pod-a", []string{"sidecar", "main"}, map[string]string{"main": "id-a"})
	podB := lifecycleProofPod("pod-b", []string{"main"}, map[string]string{"main": "id-b"})
	first, err := freezeContainerLifecycleProofs(proofs, map[string]*v1.Pod{
		"pod-a": podA,
		"pod-b": podB,
		"noise": lifecycleProofPod("noise", []string{"ignored"}, map[string]string{"ignored": "x"}),
	})
	if err != nil {
		t.Fatalf("first freeze error = %v", err)
	}
	second, err := freezeContainerLifecycleProofs([]containerLifecycleProof{proofs[1], proofs[0]}, map[string]*v1.Pod{
		"pod-b":       podB.DeepCopy(),
		"pod-a":       podA.DeepCopy(),
		"other-noise": lifecycleProofPod("other-noise", []string{"ignored"}, map[string]string{"ignored": "y"}),
	})
	if err != nil {
		t.Fatalf("second freeze error = %v", err)
	}
	if first.SourceSnapshotDigest != second.SourceSnapshotDigest {
		t.Fatalf("digest depends on map/proof order or uninvolved pods: %q != %q",
			first.SourceSnapshotDigest, second.SourceSnapshotDigest)
	}
}

func TestPodStatusContainerIDKeepsProofStateAndDigestOnNormalizedIdentity(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "normalized-container-id"
		containerName = "main"
	)
	tests := []struct {
		name        string
		containerID string
		wantID      string
		wantFresh   bool
		wantState   containerLifecycleState
	}{
		{name: "containerd prefix only", containerID: "containerd://", wantState: containerLifecyclePending},
		{name: "docker prefix only", containerID: "docker://", wantState: containerLifecyclePending},
		{name: "blank", containerID: "", wantState: containerLifecyclePending},
		{
			name:        "valid containerd identity",
			containerID: "containerd://containerd-id",
			wantID:      "containerd-id",
			wantFresh:   true,
			wantState:   containerLifecycleResolved,
		},
		{
			name:        "valid docker identity",
			containerID: "docker://docker-id",
			wantID:      "docker-id",
			wantFresh:   true,
			wantState:   containerLifecycleResolved,
		},
	}

	digests := make(map[string]string, len(tests))
	for _, tt := range tests {
		tt := tt
		pod := lifecycleProofPodWithRawContainerID(podUID, containerName, tt.containerID)
		gotID, gotFresh := podStatusContainerID(pod, containerName)
		if gotID != tt.wantID || gotFresh != tt.wantFresh {
			t.Fatalf("podStatusContainerID() = (%q, %t), want (%q, %t)",
				gotID, gotFresh, tt.wantID, tt.wantFresh)
		}

		classification := podLifecycleClassification{}
		if tt.wantState == containerLifecycleResolved {
			classification.Resolved = []resolvedContainerCPUSet{{
				PodUID:        podUID,
				ContainerName: containerName,
				ContainerID:   tt.wantID,
				Rel:           "kubepods/pod" + podUID + "/" + tt.wantID,
				CPUs:          machine.NewCPUSet(1),
			}}
		}
		proofs := lifecycleProofsForDesiredPod(
			podUID,
			map[string]machine.CPUSet{containerName: machine.NewCPUSet(1)},
			pod,
			true,
			classification,
		)
		if len(proofs) != 1 {
			t.Fatalf("proof count = %d, want 1: %#v", len(proofs), proofs)
		}
		if proofs[0].ContainerID != tt.wantID || proofs[0].State != tt.wantState {
			t.Fatalf("proof source identity/state = (%q, %v), want (%q, %v)",
				proofs[0].ContainerID, proofs[0].State, tt.wantID, tt.wantState)
		}

		frozen, err := freezeContainerLifecycleProofs(
			proofs, map[string]*v1.Pod{podUID: pod})
		if err != nil {
			t.Fatalf("freezeContainerLifecycleProofs() error = %v", err)
		}
		digests[tt.name] = frozen.SourceSnapshotDigest
	}

	blankDigest := digests["blank"]
	for _, name := range []string{"containerd prefix only", "docker prefix only"} {
		if digests[name] != blankDigest {
			t.Fatalf("%s digest = %q, want normalized-empty digest %q",
				name, digests[name], blankDigest)
		}
	}
	for _, name := range []string{"valid containerd identity", "valid docker identity"} {
		if digests[name] == blankDigest {
			t.Fatalf("%s digest unexpectedly matches normalized-empty digest %q",
				name, blankDigest)
		}
	}
}

func TestLifecycleProofsCloneDesiredCPUSet(t *testing.T) {
	t.Parallel()
	desired := machine.NewCPUSet(1, 2)
	proofs := []containerLifecycleProof{{
		PodUID: "pod-a", ContainerName: "main", State: containerLifecyclePending, DesiredCPUSet: desired,
	}}
	frozen, err := freezeContainerLifecycleProofs(proofs, map[string]*v1.Pod{
		"pod-a": lifecycleProofPod("pod-a", []string{"main"}, nil),
	})
	if err != nil {
		t.Fatalf("freezeContainerLifecycleProofs() error = %v", err)
	}

	proofs[0].DesiredCPUSet.Add(9)
	if got := frozen.OrderedProofs[0].DesiredCPUSet.String(); got != "1-2" {
		t.Fatalf("frozen cpuset aliased caller storage: %q", got)
	}
}

func lifecycleProofPod(uid string, owners []string, ids map[string]string) *v1.Pod {
	pod := &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(uid)}}
	for _, name := range owners {
		pod.Spec.Containers = append(pod.Spec.Containers, v1.Container{Name: name})
	}
	for name, id := range ids {
		pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, v1.ContainerStatus{
			Name: name, ContainerID: "containerd://" + id,
		})
	}
	return pod
}

func lifecycleProofPodWithRawContainerID(uid, name, containerID string) *v1.Pod {
	pod := lifecycleProofPod(uid, []string{name}, nil)
	pod.Status.ContainerStatuses = []v1.ContainerStatus{{
		Name: name, ContainerID: containerID,
	}}
	return pod
}
