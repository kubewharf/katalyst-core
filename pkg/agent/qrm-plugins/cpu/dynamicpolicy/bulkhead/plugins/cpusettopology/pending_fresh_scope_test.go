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
	"os/exec"
	"strings"
	"syscall"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type strictFreshPendingFetcher struct {
	metapod.PodFetcherStub
	containerErr  error
	containerErrs map[string]error
	containerIDs  map[string]string
	pods          map[string]*v1.Pod
	podErrs       map[string]error
	freshLookups  map[string]int
	listCalls     int
}

type strictSnapshotFetcher struct {
	metapod.PodFetcherStub
	containerIDs map[string]string
	pods         []*v1.Pod
	listErr      error
	refreshCalls int
	listCalls    int
	getCalls     int
}

type cacheThenFreshSnapshotFetcher struct {
	metapod.PodFetcherStub
	cachedPods       []*v1.Pod
	freshPods        []*v1.Pod
	cacheCalls       int
	freshCalls       int
	containerIDCalls int
}

func (f *cacheThenFreshSnapshotFetcher) GetPodListFromCache(
	_ context.Context, filter func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	f.cacheCalls++
	return copyFilteredPods(f.cachedPods, filter), nil
}

func (f *cacheThenFreshSnapshotFetcher) GetPodList(
	ctx context.Context, filter func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue ||
		ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod list lookup was not strict")
	}
	f.freshCalls++
	return copyFilteredPods(f.freshPods, filter), nil
}

func (f *cacheThenFreshSnapshotFetcher) GetContainerIDWithContext(
	context.Context, string, string,
) (string, error) {
	f.containerIDCalls++
	return "", errors.New("container ID lookup must use the cache-only snapshot")
}

func copyFilteredPods(pods []*v1.Pod, filter func(*v1.Pod) bool) []*v1.Pod {
	out := make([]*v1.Pod, 0, len(pods))
	for _, pod := range pods {
		if filter == nil || filter(pod) {
			out = append(out, pod.DeepCopy())
		}
	}
	return out
}

func (f *strictSnapshotFetcher) GetContainerIDWithContext(
	_ context.Context, podUID, containerName string,
) (string, error) {
	if id, ok := f.containerIDs[podUID+"/"+containerName]; ok {
		return id, nil
	}
	return "", metapod.ErrContainerNotFound
}

func (f *strictSnapshotFetcher) RefreshKubeletPodCache(context.Context) error {
	f.refreshCalls++
	return nil
}

func (f *strictSnapshotFetcher) GetPodList(
	ctx context.Context, podFilter func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	f.listCalls++
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod list did not bypass cache")
	}
	if ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod list was not strict")
	}
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*v1.Pod, 0, len(f.pods))
	for _, pod := range f.pods {
		if podFilter == nil || podFilter(pod) {
			out = append(out, pod.DeepCopy())
		}
	}
	return out, nil
}

func (f *strictSnapshotFetcher) GetPod(context.Context, string) (*v1.Pod, error) {
	f.getCalls++
	return nil, errors.New("per-pod lookup must not be used")
}

func (f *strictFreshPendingFetcher) GetContainerIDWithContext(
	_ context.Context, _, containerName string,
) (string, error) {
	if err, ok := f.containerErrs[containerName]; ok {
		return "", err
	}
	if id, ok := f.containerIDs[containerName]; ok {
		return id, nil
	}
	return "", f.containerErr
}

func (f *strictFreshPendingFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod lookup did not bypass cache")
	}
	if ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod lookup was not strict")
	}
	f.freshLookups[podUID]++
	if err := f.podErrs[podUID]; err != nil {
		return nil, err
	}
	if pod := f.pods[podUID]; pod != nil {
		return pod.DeepCopy(), nil
	}
	return nil, metapod.NewPodNotFoundError(podUID)
}

func (f *strictFreshPendingFetcher) GetPodList(
	ctx context.Context, podFilter func(*v1.Pod) bool,
) ([]*v1.Pod, error) {
	if ctx.Value(metapod.BypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod list did not bypass cache")
	}
	if ctx.Value(metapod.StrictBypassCacheKey) != metapod.BypassCacheTrue {
		return nil, errors.New("fresh pod list was not strict")
	}
	f.listCalls++
	out := make([]*v1.Pod, 0, len(f.pods))
	for podUID, pod := range f.pods {
		f.freshLookups[podUID]++
		if podFilter == nil || podFilter(pod) {
			out = append(out, pod.DeepCopy())
		}
	}
	return out, nil
}

func pendingScopeTestView(entries map[string]machine.CPUSet) *model.DesiredView {
	byPod := make(map[string]map[string]machine.CPUSet, len(entries))
	for podUID, cpus := range entries {
		byPod[podUID] = map[string]machine.CPUSet{"main": cpus}
	}
	return &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: byPod,
	}}
}

func pendingScopeTestContext(
	fetcher metapod.PodFetcher,
	view *model.DesiredView,
) bulkheadapi.HandlerContext {
	return bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{PodFetcher: fetcher},
			},
			Mode: cpusetutil.CPUSetAdjustmentModeAdmission,
		},
		DesiredView: view,
	}
}

func podScopeCandidates(podUID string) []string {
	return []string{
		"kubepods/pod" + podUID,
		"kubepods/besteffort/pod" + podUID,
		"kubepods/burstable/pod" + podUID,
	}
}

func absentCandidateErrors(podUID string) map[string]error {
	out := make(map[string]error, 3)
	for _, rel := range podScopeCandidates(podUID) {
		out[rel] = os.ErrNotExist
	}
	return out
}

func TestResolvedContainerFreshnessRequiresTypedLeafAbsence(t *testing.T) {
	t.Parallel()

	const containerName = "main"
	cpus := machine.NewCPUSet(4, 5)

	tests := []struct {
		name          string
		freshPod      func(string, string) *v1.Pod
		freshIDSame   bool
		oldStatErr    error
		oldExists     bool
		newLeafExists bool
		wantOld       bool
		wantNew       bool
		wantPending   bool
		wantState     containerLifecycleState
		wantProofID   string
		wantProofRel  string
		wantResolves  int
		wantErr       error
	}{
		{
			name:      "pod absent retires proof but keeps exact old leaf protection while it exists",
			oldExists: true,
			wantOld:   true,
			wantState: containerLifecycleRetired,
		},
		{
			name:       "pod absent keeps proof retired after typed old leaf absence",
			oldStatErr: os.ErrNotExist,
			wantState:  containerLifecycleRetired,
		},
		{
			name:       "old leaf operational error fails closed",
			oldStatErr: syscall.EIO,
			wantErr:    syscall.EIO,
		},
		{
			name: "same identity with cached path and typed leaf absence remains pending",
			freshPod: func(podUID, freshID string) *v1.Pod {
				return &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
					Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
						Name: containerName, ContainerID: "containerd://" + freshID,
					}}},
				}
			},
			freshIDSame:  true,
			oldStatErr:   os.ErrNotExist,
			wantPending:  true,
			wantState:    containerLifecyclePending,
			wantProofID:  "fresh",
			wantResolves: 2,
		},
		{
			name: "same identity resolves only when fresh leaf exists",
			freshPod: func(podUID, freshID string) *v1.Pod {
				return &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
					Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
						Name: containerName, ContainerID: "containerd://" + freshID,
					}}},
				}
			},
			freshIDSame:  true,
			oldExists:    true,
			wantOld:      true,
			wantNew:      true,
			wantState:    containerLifecycleResolved,
			wantProofID:  "fresh",
			wantProofRel: "fresh",
			wantResolves: 2,
		},
		{
			name: "same identity fresh leaf permission error fails closed",
			freshPod: func(podUID, freshID string) *v1.Pod {
				return &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
					Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
						Name: containerName, ContainerID: "containerd://" + freshID,
					}}},
				}
			},
			freshIDSame:  true,
			oldStatErr:   syscall.EACCES,
			wantResolves: 2,
			wantErr:      syscall.EACCES,
		},
		{
			name: "same identity fresh leaf io error fails closed",
			freshPod: func(podUID, freshID string) *v1.Pod {
				return &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
					Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
						Name: containerName, ContainerID: "containerd://" + freshID,
					}}},
				}
			},
			freshIDSame:  true,
			oldStatErr:   syscall.EIO,
			wantResolves: 2,
			wantErr:      syscall.EIO,
		},
		{
			name: "new identity protects old and resolved current generations",
			freshPod: func(podUID, newID string) *v1.Pod {
				return &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
					Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
						Name: containerName, ContainerID: "containerd://" + newID,
					}}},
				}
			},
			oldExists:     true,
			newLeafExists: true,
			wantOld:       true,
			wantNew:       true,
			wantState:     containerLifecycleResolved,
			wantProofID:   "fresh",
			wantProofRel:  "fresh",
		},
		{
			name: "new identity with absent leaf remains scoped pending",
			freshPod: func(podUID, newID string) *v1.Pod {
				return &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
					Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
					Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
						Name: containerName, ContainerID: "containerd://" + newID,
					}}},
				}
			},
			oldStatErr:  os.ErrNotExist,
			wantPending: true,
			wantState:   containerLifecyclePending,
			wantProofID: "fresh",
		},
		{
			name: "name absent keeps exact old leaf while it exists",
			freshPod: func(podUID, _ string) *v1.Pod {
				return &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)}}
			},
			oldExists: true,
			wantOld:   true,
			wantState: containerLifecycleRetired,
		},
		{
			name: "name absent retires after typed old leaf absence",
			freshPod: func(podUID, _ string) *v1.Pod {
				return &v1.Pod{ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)}}
			},
			oldStatErr: os.ErrNotExist,
			wantState:  containerLifecycleRetired,
		},
	}

	for i, tt := range tests {
		i := i
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podUID := fmt.Sprintf("resolved-freshness-%d", i)
			oldID := fmt.Sprintf("old-%d", i)
			newID := fmt.Sprintf("new-%d", i)
			freshID := newID
			if tt.freshIDSame {
				freshID = oldID
			}
			oldRel := "kubepods/pod" + podUID + "/" + oldID
			newRel := "kubepods/pod" + podUID + "/" + freshID
			resolveCalls := 0
			unregister := cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(cgcommon.RelativeCgroupPathHandler{
				Name: "resolved-freshness-" + podUID,
				Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
					if gotPodUID != podUID {
						return "", true, nil
					}
					switch gotContainerID {
					case oldID:
						resolveCalls++
						return "/" + oldRel, false, nil
					case freshID:
						resolveCalls++
						return "/" + newRel, false, nil
					default:
						return "", true, nil
					}
				},
			})
			t.Cleanup(unregister)

			var pods []*v1.Pod
			if tt.freshPod != nil {
				pods = append(pods, tt.freshPod(podUID, freshID))
			}
			fetcher := &strictSnapshotFetcher{
				containerIDs: map[string]string{podUID + "/" + containerName: oldID},
				pods:         pods,
			}
			cg := &fakeCgroupClient{
				existing:   map[string]bool{oldRel: tt.oldExists},
				statErrors: map[string]error{oldRel: tt.oldStatErr},
			}
			if newRel != oldRel {
				cg.existing[newRel] = tt.newLeafExists
			}
			res, err := (&CPUSetTopologyPlugin{
				cfg:    bulkheadConfigWithPrimary("kubepods"),
				cgroup: cg,
			}).buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher,
				pendingScopeTestView(map[string]machine.CPUSet{podUID: cpus}),
			))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if fetcher.listCalls != 1 {
				t.Fatalf("strict fresh pod list calls = %d, want 1", fetcher.listCalls)
			}
			if tt.wantResolves > 0 && resolveCalls != tt.wantResolves {
				t.Fatalf("container path resolve calls = %d, want %d", resolveCalls, tt.wantResolves)
			}
			if tt.wantErr != nil {
				return
			}
			if _, got := res.ExpectedByRel[oldRel]; got != tt.wantOld {
				t.Fatalf("old rel protected = %v, want %v; expected=%#v", got, tt.wantOld, res.ExpectedByRel)
			}
			if _, got := res.ExpectedByRel[newRel]; got != tt.wantNew {
				t.Fatalf("new rel protected = %v, want %v; expected=%#v", got, tt.wantNew, res.ExpectedByRel)
			}
			if got := len(res.PendingByPod) == 1; got != tt.wantPending {
				t.Fatalf("scoped pending = %v, want %v; pending=%#v", got, tt.wantPending, res.PendingByPod)
			}
			if tt.wantPending && tt.wantProofID == "fresh" && res.PendingByPod[0].ContainerID != freshID {
				t.Fatalf("pending physical container ID = %q, want fresh ID %q",
					res.PendingByPod[0].ContainerID, freshID)
			}
			if got := len(res.LifecycleProofs.OrderedProofs); got != 1 {
				t.Fatalf("lifecycle proof count = %d, want 1: %#v", got, res.LifecycleProofs.OrderedProofs)
			}
			if got := res.LifecycleProofs.OrderedProofs[0].State; got != tt.wantState {
				t.Fatalf("lifecycle state = %v, want %v", got, tt.wantState)
			}
			proof := res.LifecycleProofs.OrderedProofs[0]
			if tt.wantProofID == "fresh" && proof.ContainerID != freshID {
				t.Fatalf("lifecycle proof container ID = %q, want fresh ID %q", proof.ContainerID, freshID)
			}
			if tt.wantProofRel == "fresh" && proof.RelativePath != newRel {
				t.Fatalf("lifecycle proof relative path = %q, want fresh path %q", proof.RelativePath, newRel)
			}
			if tt.wantState == containerLifecyclePending && proof.RelativePath != "" {
				t.Fatalf("pending lifecycle proof relative path = %q, want empty", proof.RelativePath)
			}
			if tt.name == "name absent keeps exact old leaf while it exists" {
				if proof.ContainerID != oldID || proof.RelativePath != oldRel {
					t.Fatalf("retired proof identity = id %q rel %q, want old id %q rel %q",
						proof.ContainerID, proof.RelativePath, oldID, oldRel)
				}
				if !proof.DesiredCPUSet.Equals(cpus) {
					t.Fatalf("retired proof cpuset = %s, want %s", proof.DesiredCPUSet.String(), cpus.String())
				}
			}
		})
	}
}

func TestPendingContainerErrorsUseStrictFreshPodAndFreshQoS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		wantPending bool
	}{
		{name: "pod not found", err: metapod.NewPodNotFoundError("cached"), wantPending: true},
		{name: "container not found", err: metapod.ErrContainerNotFound, wantPending: true},
		{name: "container not running", err: bulkheadutils.ErrContainerNotRunning, wantPending: true},
		{
			name:        "identity changed",
			err:         fmt.Errorf("%w: old=old-id current=new-id", bulkheadutils.ErrContainerIdentityChanged),
			wantPending: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podUID := "strict-fresh-" + strings.ReplaceAll(tt.name, " ", "-")
			fetcher := &strictFreshPendingFetcher{
				containerErr: tt.err,
				pods: map[string]*v1.Pod{
					podUID: {
						ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
						Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
					},
				},
				podErrs:      map[string]error{},
				freshLookups: map[string]int{},
			}
			p := &CPUSetTopologyPlugin{
				cfg:    bulkheadConfigWithPrimary("kubepods"),
				cgroup: &fakeCgroupClient{},
			}

			res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher,
				pendingScopeTestView(map[string]machine.CPUSet{podUID: machine.NewCPUSet(0, 1)}),
			))
			if err != nil {
				t.Fatalf("pending error for live pod must remain pending: %v", err)
			}
			if fetcher.freshLookups[podUID] != 1 {
				t.Fatalf("strict fresh lookups = %d, want 1", fetcher.freshLookups[podUID])
			}
			wantPendingCount := 0
			if tt.wantPending {
				wantPendingCount = 1
			}
			if len(res.PendingByPod) != wantPendingCount {
				t.Fatalf("pending entries = %#v, want %d", res.PendingByPod, wantPendingCount)
			}
			if !tt.wantPending {
				return
			}
			if got := res.PendingByPod[0].NativeQOSClass; got != v1.PodQOSBestEffort {
				t.Fatalf("pending qos = %q, want fresh pod qos %q", got, v1.PodQOSBestEffort)
			}
			if got := res.PendingByPod[0].ScopeRel; got != "kubepods/besteffort/pod"+podUID {
				t.Fatalf("pending scope = %q, want fresh best-effort scope", got)
			}
		})
	}
}

func TestLivePendingPodWithNoMaterializedCandidateIsNotStale(t *testing.T) {
	t.Parallel()
	const subprocessEnv = "KATALYST_TEST_LIVE_PENDING_MULTIPLE_CANDIDATES"
	if os.Getenv(subprocessEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLivePendingPodWithNoMaterializedCandidateIsNotStale$")
		cmd.Env = append(os.Environ(), subprocessEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated candidate-root test failed: %v\n%s", err, output)
		}
		return
	}

	const podUID = "live-multiple-candidates"
	cgcommon.InitKubernetesCGroupPath(cgcommon.CgroupTypeSystemd, []string{
		cgcommon.CgroupFsRootPath,
		cgcommon.CgroupFsRootPathBestEffort,
		cgcommon.CgroupFsRootPathBurstable,
	})
	statErrors := make(map[string]error)
	for _, rel := range relativePendingPodScopeCandidates(
		cgcommon.GetPodRelativeCgroupPathCandidatesForQOS(podUID, v1.PodQOSBestEffort)) {
		statErrors[rel] = os.ErrNotExist
	}
	fetcher := &strictFreshPendingFetcher{
		containerErr: metapod.ErrContainerNotFound,
		pods: map[string]*v1.Pod{
			podUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	p := &CPUSetTopologyPlugin{
		cgroup: &fakeCgroupClient{
			statErrors: statErrors,
		},
	}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
		fetcher,
		pendingScopeTestView(map[string]machine.CPUSet{podUID: machine.NewCPUSet(0, 1)}),
	))
	if err != nil {
		t.Fatalf("live pending pod must not fail when candidates are not materialized: %v", err)
	}
	if fetcher.freshLookups[podUID] != 1 {
		t.Fatalf("strict fresh lookups = %d, want 1", fetcher.freshLookups[podUID])
	}
	if len(res.PendingByPod) != 1 {
		t.Fatalf("pending entries = %#v, want one live allocation", res.PendingByPod)
	}
	if got := res.PendingCPUSetUnion(); !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("pending CPU union = %s, want 0-1", got.String())
	}
}

func TestPendingContainersResolveFreshScopeOncePerPodAndProtectUnion(t *testing.T) {
	t.Parallel()

	const podUID = "pending-container-union"
	fetcher := &strictFreshPendingFetcher{
		containerErr: metapod.ErrContainerNotFound,
		pods: map[string]*v1.Pod{
			podUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec: v1.PodSpec{Containers: []v1.Container{
					{Name: "main"},
					{Name: "sidecar"},
				}},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	p := &CPUSetTopologyPlugin{
		cfg:    bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {
				"main":    machine.NewCPUSet(0, 1),
				"sidecar": machine.NewCPUSet(2, 3),
			},
		},
	}}

	res, err := p.buildExpectedCPUSetByRel(
		context.Background(), pendingScopeTestContext(fetcher, view))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if fetcher.freshLookups[podUID] != 1 {
		t.Fatalf("strict fresh lookups = %d, want one pod-level lookup", fetcher.freshLookups[podUID])
	}
	if len(res.PendingByPod) != 2 {
		t.Fatalf("pending entries = %#v, want both containers", res.PendingByPod)
	}
	if got := res.PendingCPUSetUnion(); !got.Equals(machine.NewCPUSet(0, 1, 2, 3)) {
		t.Fatalf("pending CPU union = %s, want 0-3", got.String())
	}
}

func TestPendingContainersFilterAgainstOneStrictFreshMixedContainerSpec(t *testing.T) {
	t.Parallel()

	const podUID = "mixed-container-pod"
	identityChanged := fmt.Errorf("%w: previous=old current=new",
		bulkheadutils.ErrContainerIdentityChanged)
	containerNotRunning := fmt.Errorf("%w: transient runtime state",
		bulkheadutils.ErrContainerNotRunning)
	fetcher := &strictFreshPendingFetcher{
		containerErrs: map[string]error{
			"regular-container":   containerNotRunning,
			"init-container":      metapod.ErrContainerNotFound,
			"ephemeral-container": bulkheadutils.ErrContainerNotRunning,
			"removed-container":   metapod.ErrContainerNotFound,
			"restarted-container": identityChanged,
		},
		pods: map[string]*v1.Pod{
			podUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec: v1.PodSpec{
					Containers:     []v1.Container{{Name: "regular-container"}},
					InitContainers: []v1.Container{{Name: "init-container"}},
					EphemeralContainers: []v1.EphemeralContainer{{
						EphemeralContainerCommon: v1.EphemeralContainerCommon{Name: "ephemeral-container"},
					}},
				},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {
				"regular-container":   machine.NewCPUSet(0),
				"init-container":      machine.NewCPUSet(1),
				"ephemeral-container": machine.NewCPUSet(2),
				"removed-container":   machine.NewCPUSet(3),
				"restarted-container": machine.NewCPUSet(4),
			},
		},
	}}
	p := &CPUSetTopologyPlugin{
		cfg:    bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{},
	}

	res, err := p.buildExpectedCPUSetByRel(
		context.Background(), pendingScopeTestContext(fetcher, view))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if fetcher.freshLookups[podUID] != 1 {
		t.Fatalf("strict fresh lookups = %d, want exactly one", fetcher.freshLookups[podUID])
	}
	if len(res.PendingByPod) != 3 {
		t.Fatalf("pending entries = %#v, want regular, init, and ephemeral containers", res.PendingByPod)
	}
	got := make(map[string]pendingContainerCPUSet, len(res.PendingByPod))
	for _, pending := range res.PendingByPod {
		got[pending.ContainerName] = pending
	}
	for _, name := range []string{"regular-container", "init-container", "ephemeral-container"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("fresh spec container %q was not retained: %#v", name, res.PendingByPod)
		}
	}
	for _, name := range []string{"removed-container", "restarted-container"} {
		if _, ok := got[name]; ok {
			t.Fatalf("stale container %q was retained: %#v", name, res.PendingByPod)
		}
	}
	if !errors.Is(got["regular-container"].Cause, bulkheadutils.ErrContainerNotRunning) {
		t.Fatalf("pending cause = %v, want typed %v",
			got["regular-container"].Cause, bulkheadutils.ErrContainerNotRunning)
	}
	if got := res.PendingCPUSetUnion(); !got.Equals(machine.NewCPUSet(0, 1, 2)) {
		t.Fatalf("pending CPU union = %s, want 0-2", got.String())
	}
}

func TestBuildExpectedFiltersResolvedOnlyOutcomesAgainstOneStrictFreshSpec(t *testing.T) {
	t.Parallel()
	const (
		podUID      = "resolved-only-fresh-spec-pod"
		currentName = "current"
		removedName = "removed"
		currentID   = "resolved-only-current-id"
		removedID   = "resolved-only-removed-id"
		currentRel  = "kubepods/podresolved-only-fresh-spec-pod/resolved-only-current-id"
		removedRel  = "kubepods/podresolved-only-fresh-spec-pod/resolved-only-removed-id"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "resolved-only-fresh-spec-filter",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID != podUID {
				return "", true, nil
			}
			switch gotContainerID {
			case currentID:
				return "/" + currentRel, false, nil
			case removedID:
				return "/" + removedRel, false, nil
			default:
				return "", true, nil
			}
		},
	})

	fetcher := &strictFreshPendingFetcher{
		containerIDs: map[string]string{
			currentName: currentID,
			removedName: removedID,
		},
		pods: map[string]*v1.Pod{
			podUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: currentName}}},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name: currentName, ContainerID: "containerd://" + currentID,
				}}},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {
				currentName: machine.NewCPUSet(0),
				removedName: machine.NewCPUSet(1),
			},
		},
	}}
	cfg := bulkheadConfigWithPrimary("kubepods")
	cfg.EnableAdmissionLeafDefer = true
	p := &CPUSetTopologyPlugin{
		cfg: cfg,
		cgroup: &fakeCgroupClient{
			existing: map[string]bool{currentRel: true},
			cpus: map[string]machine.CPUSet{
				currentRel: machine.NewCPUSet(0),
				removedRel: machine.NewCPUSet(1, 2),
			},
		},
		pendingProtections: map[string]pendingPodProtection{},
	}

	res, err := p.buildExpectedCPUSetByRel(
		context.Background(), pendingScopeTestContext(fetcher, view))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if fetcher.freshLookups[podUID] != 1 {
		t.Fatalf("strict fresh lookups = %d, want exactly one", fetcher.freshLookups[podUID])
	}
	if len(res.ExpectedByRel) != 1 || !res.ExpectedByRel[currentRel].Equals(machine.NewCPUSet(0)) {
		t.Fatalf("expected leaves = %#v, want only current container", res.ExpectedByRel)
	}
	if _, ok := res.ExpectedByRel[removedRel]; ok {
		t.Fatalf("fresh-spec-absent container survived in ExpectedByRel: %#v", res.ExpectedByRel)
	}
	if _, ok := res.DeferredLeafByRel[removedRel]; ok {
		t.Fatalf("fresh-spec-absent container survived in DeferredLeafByRel: %#v", res.DeferredLeafByRel)
	}
	if len(res.PendingByPod) != 0 {
		t.Fatalf("resolved-only outcomes entered PendingByPod: %#v", res.PendingByPod)
	}

	protections, err := p.pendingProtectionScopes(context.Background(), nil, res.PendingByPod)
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	if len(protections) != 0 || len(p.pendingProtections) != 0 {
		t.Fatalf("removed container retained protection: scopes=%#v cache=%#v",
			protections, p.pendingProtections)
	}
}

func TestBuildExpectedUsesOneStrictSnapshotForManyPods(t *testing.T) {
	t.Parallel()
	const podCount = 100
	fetcher := &strictSnapshotFetcher{
		containerIDs: make(map[string]string, podCount),
		pods:         make([]*v1.Pod, 0, podCount),
	}
	entries := make(map[string]machine.CPUSet, podCount)
	for i := 0; i < podCount; i++ {
		podUID := fmt.Sprintf("snapshot-pod-%d", i)
		containerID := fmt.Sprintf("snapshot-container-%d", i)
		fetcher.containerIDs[podUID+"/main"] = containerID
		fetcher.pods = append(fetcher.pods, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
			Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
				Name:        "main",
				ContainerID: "containerd://" + containerID,
			}}},
		})
		entries[podUID] = machine.NewCPUSet(i % 8)
	}
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "strict-snapshot-scaling",
		Handler: func(podUID, containerID string) (string, bool, error) {
			if strings.HasPrefix(podUID, "snapshot-pod-") && strings.HasPrefix(containerID, "snapshot-container-") {
				return "/kubepods/pod" + podUID + "/" + containerID, false, nil
			}
			return "", true, nil
		},
	})
	existing := make(map[string]bool, podCount)
	for podUID, containerID := range fetcher.containerIDs {
		podUID = strings.TrimSuffix(podUID, "/main")
		existing["kubepods/pod"+podUID+"/"+containerID] = true
	}
	p := &CPUSetTopologyPlugin{
		cfg:    bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{existing: existing},
	}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
		fetcher, pendingScopeTestView(entries)))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if len(res.ExpectedByRel) != podCount {
		t.Fatalf("resolved leaves = %d, want %d", len(res.ExpectedByRel), podCount)
	}
	if got := fetcher.refreshCalls + fetcher.listCalls; got != 1 {
		t.Fatalf("kubelet pod cache sync total = %d (RefreshKubeletPodCache=%d GetPodList=%d), want 1",
			got, fetcher.refreshCalls, fetcher.listCalls)
	}
	if fetcher.getCalls != 0 {
		t.Fatalf("per-pod GetPod calls = %d, want zero", fetcher.getCalls)
	}
}

func TestBuildExpectedUsesCacheOnlySnapshotBeforeStrictRefresh(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "cache-only-snapshot"
		containerName = "main"
		containerID   = "cached-id"
		containerRel  = "kubepods/podcache-only-snapshot/cached-id"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "cache-only-snapshot",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "/" + containerRel, false, nil
			}
			return "", true, nil
		},
	})
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
		Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name: containerName, ContainerID: "containerd://" + containerID,
		}}},
	}
	fetcher := &cacheThenFreshSnapshotFetcher{
		cachedPods: []*v1.Pod{pod},
		freshPods:  []*v1.Pod{pod},
	}
	p := &CPUSetTopologyPlugin{
		cfg:    bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{existing: map[string]bool{containerRel: true}},
	}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
		fetcher,
		pendingScopeTestView(map[string]machine.CPUSet{podUID: machine.NewCPUSet(0, 1)}),
	))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if fetcher.cacheCalls != 1 || fetcher.freshCalls != 1 || fetcher.containerIDCalls != 0 {
		t.Fatalf("cache/fresh/container-ID calls = %d/%d/%d, want 1/1/0",
			fetcher.cacheCalls, fetcher.freshCalls, fetcher.containerIDCalls)
	}
	if got := res.ExpectedByRel[containerRel]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("resolved cpuset = %s, want 0-1; result=%#v", got.String(), res)
	}
}

func TestBuildExpectedResolvesFreshIDMissingFromCachedSnapshot(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "fresh-id-resolution"
		containerName = "main"
		freshID       = "fresh-id"
		freshRel      = "kubepods/podfresh-id-resolution/fresh-id"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "fresh-id-resolution",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == freshID {
				return "/" + freshRel, false, nil
			}
			return "", true, nil
		},
	})
	cachedPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: containerName}}},
	}
	freshPod := cachedPod.DeepCopy()
	freshPod.Status.ContainerStatuses = []v1.ContainerStatus{{
		Name: containerName, ContainerID: "containerd://" + freshID,
	}}
	fetcher := &cacheThenFreshSnapshotFetcher{
		cachedPods: []*v1.Pod{cachedPod},
		freshPods:  []*v1.Pod{freshPod},
	}
	p := &CPUSetTopologyPlugin{
		cfg:    bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{existing: map[string]bool{freshRel: true}},
	}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
		fetcher,
		pendingScopeTestView(map[string]machine.CPUSet{podUID: machine.NewCPUSet(2, 3)}),
	))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if got := res.ExpectedByRel[freshRel]; !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("fresh-ID cpuset = %s, want 2-3; result=%#v", got.String(), res)
	}
	if len(res.PendingByPod) != 0 {
		t.Fatalf("resolved fresh ID remained pending: %#v", res.PendingByPod)
	}
}

func TestBuildExpectedProtectsCurrentGenerationForEveryStatusKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setStatus func(*v1.Pod, v1.ContainerStatus)
	}{
		{
			name: "regular",
			setStatus: func(pod *v1.Pod, status v1.ContainerStatus) {
				pod.Spec.Containers = []v1.Container{{Name: status.Name}}
				pod.Status.ContainerStatuses = []v1.ContainerStatus{status}
			},
		},
		{
			name: "init",
			setStatus: func(pod *v1.Pod, status v1.ContainerStatus) {
				pod.Spec.InitContainers = []v1.Container{{Name: status.Name}}
				pod.Status.InitContainerStatuses = []v1.ContainerStatus{status}
			},
		},
		{
			name: "ephemeral",
			setStatus: func(pod *v1.Pod, status v1.ContainerStatus) {
				pod.Spec.EphemeralContainers = []v1.EphemeralContainer{{
					EphemeralContainerCommon: v1.EphemeralContainerCommon{Name: status.Name},
				}}
				pod.Status.EphemeralContainerStatuses = []v1.ContainerStatus{status}
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podUID := "same-name-restart-" + tt.name
			containerName := "worker"
			oldID := "old-" + tt.name
			newID := "new-" + tt.name
			oldRel := "kubepods/pod" + podUID + "/" + oldID
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
			}
			tt.setStatus(pod, v1.ContainerStatus{
				Name:        containerName,
				ContainerID: "containerd://" + newID,
			})
			fetcher := &strictSnapshotFetcher{
				containerIDs: map[string]string{podUID + "/" + containerName: oldID},
				pods:         []*v1.Pod{pod},
			}
			registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
				Name: "resolved-old-id-" + tt.name,
				Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
					if gotPodUID == podUID && gotContainerID == oldID {
						return "", false, os.ErrNotExist
					}
					return "", true, nil
				},
			})
			p := &CPUSetTopologyPlugin{
				cfg:    bulkheadConfigWithPrimary("kubepods"),
				cgroup: &fakeCgroupClient{},
			}
			view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					podUID: {containerName: machine.NewCPUSet(0)},
				},
			}}

			res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher, view))
			if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if _, ok := res.ExpectedByRel[oldRel]; ok {
				t.Fatalf("old resolved cgroup survived same-name restart: %#v", res.ExpectedByRel)
			}
			if len(res.PendingByPod) != 1 {
				t.Fatalf("new generation without a leaf was not protected as pending: %#v", res.PendingByPod)
			}
			if got := fetcher.refreshCalls + fetcher.listCalls; got != 1 {
				t.Fatalf("kubelet pod cache sync total = %d (RefreshKubeletPodCache=%d GetPodList=%d), want 1",
					got, fetcher.refreshCalls, fetcher.listCalls)
			}
		})
	}
}

func TestBuildExpectedKeepsResolvedContainerIDForEveryStatusKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setStatus func(*v1.Pod, v1.ContainerStatus)
	}{
		{
			name: "regular",
			setStatus: func(pod *v1.Pod, status v1.ContainerStatus) {
				pod.Spec.Containers = []v1.Container{{Name: status.Name}}
				pod.Status.ContainerStatuses = []v1.ContainerStatus{status}
			},
		},
		{
			name: "init",
			setStatus: func(pod *v1.Pod, status v1.ContainerStatus) {
				pod.Spec.InitContainers = []v1.Container{{Name: status.Name}}
				pod.Status.InitContainerStatuses = []v1.ContainerStatus{status}
			},
		},
		{
			name: "ephemeral",
			setStatus: func(pod *v1.Pod, status v1.ContainerStatus) {
				pod.Spec.EphemeralContainers = []v1.EphemeralContainer{{
					EphemeralContainerCommon: v1.EphemeralContainerCommon{Name: status.Name},
				}}
				pod.Status.EphemeralContainerStatuses = []v1.ContainerStatus{status}
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podUID := "current-status-" + tt.name
			containerName := "worker"
			containerID := "current-" + tt.name
			rel := "kubepods/pod" + podUID + "/" + containerID
			pod := &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
			}
			tt.setStatus(pod, v1.ContainerStatus{
				Name:        containerName,
				ContainerID: "containerd://" + containerID,
			})
			fetcher := &strictSnapshotFetcher{
				containerIDs: map[string]string{podUID + "/" + containerName: containerID},
				pods:         []*v1.Pod{pod},
			}
			registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
				Name: "resolved-current-id-" + tt.name,
				Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
					if gotPodUID == podUID && gotContainerID == containerID {
						return "/" + rel, false, nil
					}
					return "", true, nil
				},
			})
			p := &CPUSetTopologyPlugin{
				cfg:    bulkheadConfigWithPrimary("kubepods"),
				cgroup: &fakeCgroupClient{existing: map[string]bool{rel: true}},
			}
			view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
				ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
					podUID: {containerName: machine.NewCPUSet(0)},
				},
			}}

			res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher, view))
			if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if got, ok := res.ExpectedByRel[rel]; !ok || !got.Equals(machine.NewCPUSet(0)) {
				t.Fatalf("resolved current cgroup = %s, ok=%v, want CPU 0", got.String(), ok)
			}
		})
	}
}

func TestBuildExpectedPreservesStrictSnapshotErrors(t *testing.T) {
	t.Parallel()
	transportErr := errors.New("snapshot transport failed")
	for _, wantErr := range []error{context.Canceled, context.DeadlineExceeded, transportErr} {
		wantErr := wantErr
		t.Run(wantErr.Error(), func(t *testing.T) {
			t.Parallel()
			fetcher := &strictSnapshotFetcher{
				containerIDs: map[string]string{"snapshot-error/main": "container"},
				listErr:      wantErr,
			}
			registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
				Name: "strict-snapshot-error-" + strings.ReplaceAll(wantErr.Error(), " ", "-"),
				Handler: func(podUID, containerID string) (string, bool, error) {
					if podUID == "snapshot-error" && containerID == "container" {
						return "/kubepods/podsnapshot-error/container", false, nil
					}
					return "", true, nil
				},
			})
			p := &CPUSetTopologyPlugin{cfg: bulkheadConfigWithPrimary("kubepods")}

			_, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher,
				pendingScopeTestView(map[string]machine.CPUSet{"snapshot-error": machine.NewCPUSet(0)}),
			))
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want errors.Is(_, %v)", err, wantErr)
			}
			if fetcher.listCalls != 1 || fetcher.getCalls != 0 {
				t.Fatalf("list calls=%d get calls=%d, want 1/0", fetcher.listCalls, fetcher.getCalls)
			}
		})
	}
}

func TestBuildExpectedFiltersAllPodOutcomesAgainstOneStrictFreshMixedContainerSpec(t *testing.T) {
	t.Parallel()
	const (
		podUID      = "mixed-resolved-and-pending-pod"
		regularName = "regular-resolved"
		initName    = "init-pending"
		ephemeral   = "ephemeral-pending"
		staleName   = "resolved-stale"
		changedName = "identity-changed"
		regularID   = "mixed-regular-id"
		staleID     = "mixed-stale-id"
		regularRel  = "kubepods/podmixed-resolved-and-pending-pod/mixed-regular-id"
		staleRel    = "kubepods/podmixed-resolved-and-pending-pod/mixed-stale-id"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "mixed-resolved-and-pending-fresh-filter",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID != podUID {
				return "", true, nil
			}
			switch gotContainerID {
			case regularID:
				return "/" + regularRel, false, nil
			case staleID:
				return "/" + staleRel, false, nil
			default:
				return "", true, nil
			}
		},
	})

	fetcher := &strictFreshPendingFetcher{
		containerErrs: map[string]error{
			initName:    metapod.ErrContainerNotFound,
			ephemeral:   bulkheadutils.ErrContainerNotRunning,
			changedName: fmt.Errorf("%w: previous=old current=new", bulkheadutils.ErrContainerIdentityChanged),
		},
		containerIDs: map[string]string{
			regularName: regularID,
			staleName:   staleID,
		},
		pods: map[string]*v1.Pod{
			podUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec: v1.PodSpec{
					Containers:     []v1.Container{{Name: regularName}},
					InitContainers: []v1.Container{{Name: initName}},
					EphemeralContainers: []v1.EphemeralContainer{{
						EphemeralContainerCommon: v1.EphemeralContainerCommon{Name: ephemeral},
					}},
				},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name: regularName, ContainerID: "containerd://" + regularID,
				}}},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	view := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
			podUID: {
				regularName: machine.NewCPUSet(0),
				initName:    machine.NewCPUSet(1),
				ephemeral:   machine.NewCPUSet(2),
				staleName:   machine.NewCPUSet(3),
				changedName: machine.NewCPUSet(4),
			},
		},
	}}
	p := &CPUSetTopologyPlugin{
		cfg:                bulkheadConfigWithPrimary("kubepods"),
		cgroup:             &fakeCgroupClient{existing: map[string]bool{regularRel: true}},
		pendingProtections: map[string]pendingPodProtection{},
	}

	res, err := p.buildExpectedCPUSetByRel(
		context.Background(), pendingScopeTestContext(fetcher, view))
	if err != nil {
		t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
	}
	if fetcher.freshLookups[podUID] != 1 {
		t.Fatalf("strict fresh lookups = %d, want exactly one", fetcher.freshLookups[podUID])
	}
	if len(res.ExpectedByRel) != 1 || !res.ExpectedByRel[regularRel].Equals(machine.NewCPUSet(0)) {
		t.Fatalf("expected leaves = %#v, want only live regular container", res.ExpectedByRel)
	}
	if _, ok := res.ExpectedByRel[staleRel]; ok {
		t.Fatalf("fresh-spec-absent CPUs survived in ExpectedByRel: %#v", res.ExpectedByRel)
	}
	if len(res.PendingByPod) != 2 {
		t.Fatalf("pending entries = %#v, want only init and ephemeral containers", res.PendingByPod)
	}
	pendingNames := make(map[string]struct{}, len(res.PendingByPod))
	for _, pending := range res.PendingByPod {
		pendingNames[pending.ContainerName] = struct{}{}
	}
	for _, name := range []string{initName, ephemeral} {
		if _, ok := pendingNames[name]; !ok {
			t.Fatalf("live pending container %q was dropped: %#v", name, res.PendingByPod)
		}
	}
	for _, name := range []string{staleName, changedName} {
		if _, ok := pendingNames[name]; ok {
			t.Fatalf("stale container %q survived in PendingByPod: %#v", name, res.PendingByPod)
		}
	}
	if got := res.PendingCPUSetUnion(); !got.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("pending CPU union = %s, want 1-2 without stale CPUs 3-4", got.String())
	}

	dag, err := topology.BuildDAG([]topology.NodeSpec{{
		Rel:            "kubepods",
		Role:           topology.TopoNodeRolePrimary,
		Domain:         topology.DomainPrimary,
		ControlledRoot: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	protections, err := p.pendingProtectionScopes(context.Background(), dag, res.PendingByPod)
	if err != nil {
		t.Fatalf("pendingProtectionScopes() error = %v", err)
	}
	protected := machine.NewCPUSet()
	for _, protection := range protections {
		protected = protected.Union(protection.CPUs)
	}
	if !protected.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("protected CPU union = %s, want 1-2 without stale CPUs 3-4", protected.String())
	}
}

func TestFreshAbsentPendingPodCandidateCardinality(t *testing.T) {
	t.Parallel()

	const podUID = "fresh-absent"
	tests := []struct {
		name       string
		statErrors map[string]error
		existing   map[string]bool
		wantScope  string
		wantSkip   bool
		wantErr    error
	}{
		{
			name:       "zero candidates skips stale checkpoint entry",
			statErrors: absentCandidateErrors(podUID),
			wantSkip:   true,
		},
		{
			name:       "one candidate retains concrete scope",
			statErrors: absentCandidateErrors(podUID),
			existing: map[string]bool{
				"kubepods/burstable/pod" + podUID: true,
			},
			wantScope: "kubepods/burstable/pod" + podUID,
		},
		{
			name:       "multiple candidates fail closed",
			statErrors: absentCandidateErrors(podUID),
			existing: map[string]bool{
				"kubepods/pod" + podUID:           true,
				"kubepods/burstable/pod" + podUID: true,
			},
			wantErr: errPendingPodScopeAmbiguous,
		},
		{
			name: "non ENOENT is preserved",
			statErrors: func() map[string]error {
				errs := absentCandidateErrors(podUID)
				errs["kubepods/burstable/pod"+podUID] = os.ErrPermission
				return errs
			}(),
			wantErr: os.ErrPermission,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			statErrors := make(map[string]error, len(tt.statErrors))
			for rel, statErr := range tt.statErrors {
				statErrors[rel] = statErr
			}
			for rel := range tt.existing {
				delete(statErrors, rel)
			}
			fetcher := &strictFreshPendingFetcher{
				containerErr: metapod.ErrContainerNotFound,
				pods:         map[string]*v1.Pod{},
				podErrs:      map[string]error{},
				freshLookups: map[string]int{},
			}
			p := &CPUSetTopologyPlugin{
				cfg: bulkheadConfigWithPrimary("kubepods"),
				cgroup: &fakeCgroupClient{
					existing:   tt.existing,
					statErrors: statErrors,
				},
			}

			res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher,
				pendingScopeTestView(map[string]machine.CPUSet{podUID: machine.NewCPUSet(2, 3)}),
			))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if fetcher.listCalls != 1 {
				t.Fatalf("strict fresh pod list calls = %d, want 1", fetcher.listCalls)
			}
			if tt.wantSkip {
				if len(res.PendingByPod) != 0 {
					t.Fatalf("stale pod entered pending protection: %#v", res.PendingByPod)
				}
				return
			}
			if len(res.PendingByPod) != 1 || res.PendingByPod[0].ScopeRel != tt.wantScope {
				t.Fatalf("pending entries = %#v, want concrete scope %q", res.PendingByPod, tt.wantScope)
			}
		})
	}
}

func TestIdentityChangedMissingPodCandidateCardinality(t *testing.T) {
	t.Parallel()

	const podUID = "identity-changed-missing"
	tests := []struct {
		name      string
		existing  map[string]bool
		wantScope string
		wantSkip  bool
		wantErr   error
	}{
		{
			name:     "zero candidates skips stale checkpoint entry",
			wantSkip: true,
		},
		{
			name: "one candidate protects identity-changed allocation",
			existing: map[string]bool{
				"kubepods/burstable/pod" + podUID: true,
			},
			wantScope: "kubepods/burstable/pod" + podUID,
		},
		{
			name: "multiple candidates fail closed",
			existing: map[string]bool{
				"kubepods/pod" + podUID:           true,
				"kubepods/burstable/pod" + podUID: true,
			},
			wantErr: errPendingPodScopeAmbiguous,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			statErrors := absentCandidateErrors(podUID)
			for rel := range tt.existing {
				delete(statErrors, rel)
			}
			fetcher := &strictFreshPendingFetcher{
				containerErr: fmt.Errorf("%w: old=old-id current=new-id", bulkheadutils.ErrContainerIdentityChanged),
				pods:         map[string]*v1.Pod{},
				podErrs:      map[string]error{},
				freshLookups: map[string]int{},
			}
			p := &CPUSetTopologyPlugin{
				cfg: bulkheadConfigWithPrimary("kubepods"),
				cgroup: &fakeCgroupClient{
					existing:   tt.existing,
					statErrors: statErrors,
				},
			}

			res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
				fetcher,
				pendingScopeTestView(map[string]machine.CPUSet{podUID: machine.NewCPUSet(4, 5)}),
			))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildExpectedCPUSetByRel() error = %v", err)
			}
			if fetcher.listCalls != 1 {
				t.Fatalf("strict fresh pod list calls = %d, want 1", fetcher.listCalls)
			}
			if tt.wantSkip {
				if len(res.PendingByPod) != 0 {
					t.Fatalf("stale pod entered pending protection: %#v", res.PendingByPod)
				}
				return
			}
			if len(res.PendingByPod) != 1 || res.PendingByPod[0].ScopeRel != tt.wantScope {
				t.Fatalf("pending entries = %#v, want concrete scope %q", res.PendingByPod, tt.wantScope)
			}
		})
	}
}

func TestSelectConcretePendingPodScope(t *testing.T) {
	t.Parallel()

	t.Run("rejects unsafe candidates before lookup", func(t *testing.T) {
		t.Parallel()
		tests := []string{
			"",
			".",
			"..",
			"../escape",
			"kubepods/../escape",
			"unmanaged/podunsafe",
			"/kubepods/podunsafe",
		}
		for _, candidate := range tests {
			candidate := candidate
			t.Run(fmt.Sprintf("%q", candidate), func(t *testing.T) {
				t.Parallel()
				cg := &fakeCgroupClient{}
				p := &CPUSetTopologyPlugin{
					cfg:    bulkheadConfigWithPrimary("kubepods"),
					cgroup: cg,
				}

				_, _, err := p.selectConcretePendingPodScope(
					context.Background(), "unsafe", []string{candidate})
				if err == nil {
					t.Fatalf("candidate %q succeeded, want safety error", candidate)
				}
				if len(cg.statCalls) != 0 {
					t.Fatalf("unsafe candidate reached StatDir: %#v", cg.statCalls)
				}
			})
		}
	})

	t.Run("deduplicates normalized candidates before cardinality", func(t *testing.T) {
		t.Parallel()
		const (
			podUID = "duplicate"
			want   = "kubepods/podduplicate"
		)
		cg := &fakeCgroupClient{existing: map[string]bool{want: true}}
		p := &CPUSetTopologyPlugin{
			cfg:    bulkheadConfigWithPrimary("kubepods"),
			cgroup: cg,
		}

		scope, stale, err := p.selectConcretePendingPodScope(context.Background(), podUID, []string{
			want,
			"kubepods//podduplicate",
			"kubepods/podduplicate/",
		})
		if err != nil {
			t.Fatalf("selectConcretePendingPodScope() error = %v", err)
		}
		if stale || scope != want {
			t.Fatalf("scope=%q stale=%v, want scope=%q stale=false", scope, stale, want)
		}
		if len(cg.statCalls) != 1 || cg.statCalls[0] != want {
			t.Fatalf("StatDir calls = %#v, want one canonical lookup for %q", cg.statCalls, want)
		}
	})

	t.Run("preserves EIO from StatDir", func(t *testing.T) {
		t.Parallel()
		const candidate = "kubepods/podio"
		eio := fmt.Errorf("stat candidate: %w", syscall.EIO)
		cg := &fakeCgroupClient{statErrors: map[string]error{candidate: eio}}
		p := &CPUSetTopologyPlugin{
			cfg:    bulkheadConfigWithPrimary("kubepods"),
			cgroup: cg,
		}

		_, _, err := p.selectConcretePendingPodScope(
			context.Background(), "io", []string{candidate})
		if err != eio {
			t.Fatalf("error = %v, want original EIO %v", err, eio)
		}
		if !errors.Is(err, syscall.EIO) {
			t.Fatalf("error = %v, want errors.Is(_, EIO)", err)
		}
	})
}

func TestRemovePodIgnoresUnrelatedStalePendingEntry(t *testing.T) {
	t.Parallel()

	const (
		liveUID  = "remove-pod-live"
		staleUID = "remove-pod-unrelated-stale"
	)
	fetcher := &strictFreshPendingFetcher{
		containerErr: metapod.ErrContainerNotFound,
		pods: map[string]*v1.Pod{
			liveUID: {
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(liveUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
			},
		},
		podErrs:      map[string]error{},
		freshLookups: map[string]int{},
	}
	statErrors := absentCandidateErrors(staleUID)
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadConfigWithPrimary("kubepods"),
		cgroup: &fakeCgroupClient{
			statErrors: statErrors,
		},
	}

	res, err := p.buildExpectedCPUSetByRel(context.Background(), pendingScopeTestContext(
		fetcher,
		pendingScopeTestView(map[string]machine.CPUSet{
			liveUID:  machine.NewCPUSet(0, 1),
			staleUID: machine.NewCPUSet(2, 3),
		}),
	))
	if err != nil {
		t.Fatalf("RemovePod-shaped round must ignore unrelated stale entry: %v", err)
	}
	if len(res.PendingByPod) != 1 || res.PendingByPod[0].PodUID != liveUID {
		t.Fatalf("pending entries = %#v, want only live pod", res.PendingByPod)
	}
	if fetcher.listCalls != 1 {
		t.Fatalf("strict fresh pod list calls = %d, want one", fetcher.listCalls)
	}
}

func bulkheadConfigWithPrimary(primary string) bulkheadconfig.BulkheadConfiguration {
	return bulkheadconfig.BulkheadConfiguration{BulkheadPrimaryRelPath: primary}
}
