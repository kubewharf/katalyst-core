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

package utils

import (
	"context"
	"errors"
	"os"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func registerRelativeCgroupPathHandlerForTest(t *testing.T, handler cgcommon.RelativeCgroupPathHandler) {
	t.Helper()
	t.Cleanup(cgcommon.RegisterRelativeCgroupPathHandlerWithUnregister(handler))
}

type contextAwareContainerIDFetcher struct {
	metapod.PodFetcherStub
	observed context.Context
}

type containerIDResult struct {
	id  string
	err error
}

type sequenceContainerIDFetcher struct {
	metapod.PodFetcherStub
	results        []containerIDResult
	calls          int
	runtimeRunning bool
	runtimeErr     error
}

type cacheAwareContainerIDFetcher struct {
	metapod.PodFetcherStub
	cachedID     string
	currentID    string
	calls        int
	refreshed    bool
	refreshCalls int
}

func (f *cacheAwareContainerIDFetcher) GetContainerIDWithContext(
	ctx context.Context, _, _ string,
) (string, error) {
	f.calls++
	if f.refreshed || ctx.Value(metapod.BypassCacheKey) == metapod.BypassCacheTrue {
		return f.currentID, nil
	}
	return f.cachedID, nil
}

func (f *cacheAwareContainerIDFetcher) RefreshKubeletPodCache(context.Context) error {
	f.refreshCalls++
	f.refreshed = true
	return nil
}

func (f *sequenceContainerIDFetcher) GetContainerIDWithContext(
	context.Context, string, string,
) (string, error) {
	result := f.results[f.calls]
	f.calls++
	return result.id, result.err
}

func (f *sequenceContainerIDFetcher) IsContainerRunningInRuntime(
	context.Context, string, string, string,
) (bool, error) {
	return f.runtimeRunning, f.runtimeErr
}

func (f *contextAwareContainerIDFetcher) GetContainerIDWithContext(
	ctx context.Context, _, _ string,
) (string, error) {
	f.observed = ctx
	return "", ctx.Err()
}

func TestResolveContainerRelPathWithContextPropagatesCancellation(t *testing.T) {
	t.Parallel()

	fetcher := &contextAwareContainerIDFetcher{}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ResolveContainerRelPathWithContext(ctx, metaServer, "pod", "container")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want context canceled", err)
	}
	if fetcher.observed != ctx {
		t.Fatal("ResolveContainerRelPathWithContext() did not pass the caller context to GetContainerIDWithContext")
	}
}

func TestResolveContainerRelPathAndIDWithContextReturnsResolvedIdentity(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "pod-resolved-identity"
		containerName = "main"
		containerID   = "resolved-identity"
		wantRel       = "kubepods/podpod-resolved-identity/resolved-identity"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "return-resolved-identity",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "/" + wantRel, false, nil
			}
			return "", true, nil
		},
	})
	fetcher := &sequenceContainerIDFetcher{results: []containerIDResult{{id: containerID}}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	rel, resolvedID, err := ResolveContainerRelPathAndIDWithContext(
		context.Background(), metaServer, podUID, containerName)
	if err != nil {
		t.Fatalf("ResolveContainerRelPathAndIDWithContext() error = %v", err)
	}
	if rel != wantRel || resolvedID != containerID {
		t.Fatalf("resolved rel/id = %q/%q, want %q/%q", rel, resolvedID, wantRel, containerID)
	}
}

func TestResolveContainerRelPathAndIDCacheOnlyWithContextReturnsUsedIdentityOnPathError(t *testing.T) {
	t.Parallel()
	const (
		podUID        = "pod-cache-only-path-error"
		containerName = "main"
		containerID   = "cached-identity"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "cache-only-path-error",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &cacheAwareContainerIDFetcher{
		cachedID:  containerID,
		currentID: "new-identity",
	}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	rel, usedID, err := ResolveContainerRelPathAndIDCacheOnlyWithContext(
		context.Background(), metaServer, podUID, containerName)
	if rel != "" {
		t.Fatalf("resolved rel = %q, want empty", rel)
	}
	if usedID != containerID {
		t.Fatalf("used container ID = %q, want %q", usedID, containerID)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want errors.Is(_, os.ErrNotExist)", err)
	}
	if fetcher.refreshCalls != 0 {
		t.Fatalf("RefreshKubeletPodCache calls = %d, want 0", fetcher.refreshCalls)
	}
	if fetcher.calls != 1 {
		t.Fatalf("container ID lookup calls = %d, want 1", fetcher.calls)
	}
}

func TestResolveContainerRelPathWithContextDetectsContainerIdentityChange(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-container-identity-change"
		oldContainer  = "container-old"
		newContainer  = "container-new"
		containerName = "main"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "container-identity-change",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == oldContainer {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &cacheAwareContainerIDFetcher{
		cachedID:  oldContainer,
		currentID: newContainer,
	}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	_, err := ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
	if !errors.Is(err, ErrContainerIdentityChanged) {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want %v", err, ErrContainerIdentityChanged)
	}
	if fetcher.calls != 2 {
		t.Fatalf("container ID lookup calls = %d, want 2", fetcher.calls)
	}
}

func TestContainerIdentityRefreshScopeRefreshesOnlyOnce(t *testing.T) {
	t.Parallel()

	fetcher := &cacheAwareContainerIDFetcher{
		cachedID:  "container-old",
		currentID: "container-new",
	}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}
	ctx := WithContainerIdentityRefreshScope(context.Background())

	if err := RefreshContainerIdentityCache(ctx, metaServer); err != nil {
		t.Fatalf("first RefreshContainerIdentityCache() error = %v", err)
	}
	if err := RefreshContainerIdentityCache(ctx, metaServer); err != nil {
		t.Fatalf("second RefreshContainerIdentityCache() error = %v", err)
	}
	if fetcher.refreshCalls != 1 {
		t.Fatalf("identity cache refresh calls = %d, want 1", fetcher.refreshCalls)
	}
}

func TestResolveContainerRelPathWithContextFailsClosedForStableMissingCgroup(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-stable-missing-cgroup"
		containerID   = "container-stable"
		containerName = "main"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "stable-missing-cgroup",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &sequenceContainerIDFetcher{results: []containerIDResult{
		{id: containerID},
		{id: containerID},
	}, runtimeRunning: true}
	fetcher.PodList = []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:        containerName,
			ContainerID: "containerd://" + containerID,
			State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}}},
	}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	_, err := ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
	if err == nil {
		t.Fatal("ResolveContainerRelPathWithContext() error = nil, want fail-closed cgroup path error")
	}
	if errors.Is(err, ErrContainerIdentityChanged) {
		t.Fatalf("stable container identity was misclassified as changed: %v", err)
	}
	var resolveErr *ContainerRelPathResolveError
	if !errors.As(err, &resolveErr) || resolveErr.Stage != ContainerRelPathResolveStageCgroupPath {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want cgroup_path stage", err)
	}
	if fetcher.calls != 2 {
		t.Fatalf("container ID lookup calls = %d, want 2", fetcher.calls)
	}
}

func TestResolveContainerRelPathWithContextTreatsKubeletRunningRuntimeExitedAsNotRunning(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-runtime-exited-container"
		containerID   = "container-runtime-exited"
		containerName = "main"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "runtime-exited-container",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &sequenceContainerIDFetcher{results: []containerIDResult{
		{id: containerID},
		{id: containerID},
	}}
	fetcher.PodList = []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:        containerName,
			ContainerID: "containerd://" + containerID,
			State:       v1.ContainerState{Running: &v1.ContainerStateRunning{}},
		}}},
	}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	_, err := ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
	if !errors.Is(err, ErrContainerNotRunning) {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want %v", err, ErrContainerNotRunning)
	}
}

func TestResolveContainerRelPathWithContextTreatsTerminatedContainerAsNotRunning(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-terminated-container"
		containerID   = "container-terminated"
		containerName = "main"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "terminated-container",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &sequenceContainerIDFetcher{results: []containerIDResult{
		{id: containerID},
		{id: containerID},
	}}
	fetcher.PodList = []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
		Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
			Name:        containerName,
			ContainerID: "containerd://" + containerID,
			State:       v1.ContainerState{Terminated: &v1.ContainerStateTerminated{}},
		}}},
	}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	_, err := ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
	if !errors.Is(err, ErrContainerNotRunning) {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want %v", err, ErrContainerNotRunning)
	}
}

func TestResolveContainerRelPathWithContextDetectsContainerDisappearance(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-container-disappeared"
		containerID   = "container-disappeared"
		containerName = "main"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "container-disappeared",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &sequenceContainerIDFetcher{results: []containerIDResult{
		{id: containerID},
		{err: metapod.ErrContainerNotFound},
	}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	_, err := ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
	if !errors.Is(err, metapod.ErrContainerNotFound) {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want %v", err, metapod.ErrContainerNotFound)
	}
	if fetcher.calls != 2 {
		t.Fatalf("container ID lookup calls = %d, want 2", fetcher.calls)
	}
}

func TestResolveContainerRelPathWithContextFailsClosedWhenIdentityConfirmationFails(t *testing.T) {
	t.Parallel()

	const (
		podUID        = "pod-identity-confirmation-failed"
		containerID   = "container-confirmation-failed"
		containerName = "main"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommon.RelativeCgroupPathHandler{
		Name: "identity-confirmation-failed",
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return "", false, os.ErrNotExist
			}
			return "", true, nil
		},
	})
	fetcher := &sequenceContainerIDFetcher{results: []containerIDResult{
		{id: containerID},
		{err: context.DeadlineExceeded},
	}}
	metaServer := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: fetcher}}

	_, err := ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveContainerRelPathWithContext() error = %v, want %v", err, context.DeadlineExceeded)
	}
	if errors.Is(err, ErrContainerIdentityChanged) {
		t.Fatalf("identity confirmation failure was misclassified as changed: %v", err)
	}
	if fetcher.calls != 2 {
		t.Fatalf("container ID lookup calls = %d, want 2", fetcher.calls)
	}
}

func TestCollectActiveRelsIncludesRootPartitionsSiblingsAndPerNUMA(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-"},
		BulkheadPartitionRelPaths:   []string{"kubepods", "burstable"},
	}
	view := &model.CPUSetPartitionView{
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			1: machine.NewCPUSet(4, 5),
		},
	}

	got := CollectActiveRels(cfg, view, nil, []string{"besteffort"}, nil)
	for _, rel := range []string{"", "kubepods", "reclaimed", "reclaimed/reclaimed-1", "burstable", "besteffort"} {
		if _, ok := got[rel]; !ok {
			t.Fatalf("expected active rel %q in %#v", rel, got)
		}
	}
}

func TestBuildTopologyNodeSpecsFromViewUsesBulkheadConfig(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(0, 1),
		ReclaimEffective:        machine.NewCPUSet(2, 3),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(2)},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 0},
		2: {NUMANodeID: 0},
		3: {NUMANodeID: 0},
	}

	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, []string{"sibling"}, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	rels := map[string]struct{}{}
	for _, spec := range specs {
		rels[spec.Rel] = struct{}{}
	}
	for _, rel := range []string{"kubepods", "reclaimed", "reclaimed/reclaimed-0", "sibling"} {
		if _, ok := rels[rel]; !ok {
			t.Fatalf("expected rel %q in specs %#v", rel, specs)
		}
	}
	for _, spec := range specs {
		if spec.Rel != "sibling" {
			continue
		}
		if spec.Role != topology.TopoNodeRoleReclaimSibling {
			t.Fatalf("sibling role = %q, want %q", spec.Role, topology.TopoNodeRoleReclaimSibling)
		}
		if spec.Domain != topology.DomainReclaim {
			t.Fatalf("sibling domain = %q, want %q", spec.Domain, topology.DomainReclaim)
		}
		if !spec.ControlledRoot || !spec.TrustAnchor {
			t.Fatalf("sibling control flags = controlled_root=%t trust_anchor=%t, want both true", spec.ControlledRoot, spec.TrustAnchor)
		}
		if !spec.CPUs.Equals(view.ReclaimEffective) {
			t.Fatalf("sibling cpuset = %s, want %s", spec.CPUs.String(), view.ReclaimEffective.String())
		}
		return
	}
	t.Fatalf("expected sibling spec in %#v", specs)
}

func TestBuildTopologyNodeSpecsFromViewRetainsEmptyPhysicalNUMABucket(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0, 1, 3),
		ReclaimEffective: machine.NewCPUSet(2),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2),
			1: machine.NewCPUSet(),
		},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 0}, 3: {NUMANodeID: 1},
	}

	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, nil, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	byRel := make(map[string]topology.NodeSpec, len(specs))
	for _, spec := range specs {
		byRel[spec.Rel] = spec
	}
	emptyBucket, ok := byRel["reclaimed/reclaimed-1"]
	if !ok {
		t.Fatalf("empty physical NUMA bucket was omitted from topology specs: %#v", specs)
	}
	if !emptyBucket.CPUs.IsEmpty() {
		t.Fatalf("empty physical NUMA bucket CPUs = %s, want empty", emptyBucket.CPUs.String())
	}
	if !emptyBucket.Constraint.CPUUpperBound.Equals(machine.NewCPUSet(3)) {
		t.Fatalf("empty physical NUMA bucket upper bound = %s, want 3", emptyBucket.Constraint.CPUUpperBound.String())
	}
}

func TestBuildTopologyNodeSpecsFromViewBackfillsMissingEmptyPhysicalNUMABucket(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"custom-reclaim-root"},
		BulkheadReclaimNumaPrefixes: []string{"custom-reclaim-root/reclaimed-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0, 1, 2, 3, 4, 5),
		ReclaimEffective: machine.NewCPUSet(0, 2, 4),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(0),
			1: machine.NewCPUSet(2),
			2: machine.NewCPUSet(4),
		},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
		4: {NUMANodeID: 2}, 5: {NUMANodeID: 2},
		6: {NUMANodeID: 3}, 7: {NUMANodeID: 3},
	}

	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, nil, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	byRel := make(map[string]topology.NodeSpec, len(specs))
	for _, spec := range specs {
		byRel[spec.Rel] = spec
	}
	emptyBucket, ok := byRel["custom-reclaim-root/reclaimed-3"]
	if !ok {
		t.Fatalf("missing empty physical NUMA bucket was omitted from topology specs: %#v", specs)
	}
	if !emptyBucket.CPUs.IsEmpty() {
		t.Fatalf("missing empty physical NUMA bucket CPUs = %s, want empty", emptyBucket.CPUs.String())
	}
	if !emptyBucket.Constraint.CPUUpperBound.Equals(machine.NewCPUSet(6, 7)) {
		t.Fatalf("missing empty physical NUMA bucket upper bound = %s, want 6-7", emptyBucket.Constraint.CPUUpperBound.String())
	}
}

func TestBuildTopologyNodeSpecsFromViewRetainsConfiguredMissingRels(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed", "system"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-", "system/numa-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(0, 1),
		ReclaimEffective:        machine.NewCPUSet(2, 3),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(2, 3)},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 0}, 3: {NUMANodeID: 0},
	}

	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, nil, func(string) error {
		return os.ErrNotExist
	})
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	byRel := make(map[string]topology.NodeSpec, len(specs))
	for _, spec := range specs {
		byRel[spec.Rel] = spec
	}
	for _, rel := range []string{"reclaimed", "system", "reclaimed/reclaimed-0", "system/numa-0"} {
		if _, ok := byRel[rel]; !ok {
			t.Fatalf("configured missing rel %q was removed from topology boundary: %#v", rel, specs)
		}
	}
}

func TestBuildTopologyNodeSpecsFromViewBuildsNestedReclaimParentHierarchyWithoutMems(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "primary",
		BulkheadReclaimRelPaths:     []string{"reclaim", "reclaim/nested"},
		BulkheadReclaimNumaPrefixes: []string{"reclaim/direct-", "reclaim/nested/deep/bucket-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0, 1),
		ReclaimEffective: machine.NewCPUSet(2, 3),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(2),
			1: machine.NewCPUSet(3),
		},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
		2: {NUMANodeID: 0}, 3: {NUMANodeID: 1},
	}
	existing := map[string]struct{}{
		"primary": {}, "reclaim": {}, "reclaim/direct-0": {},
		"reclaim/nested": {}, "reclaim/nested/deep/bucket-1": {},
	}
	relExists := func(rel string) error {
		if _, ok := existing[rel]; ok {
			return nil
		}
		return os.ErrNotExist
	}

	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, nil, relExists)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	byRel := make(map[string]topology.NodeSpec, len(specs))
	for _, spec := range specs {
		byRel[spec.Rel] = spec
	}
	for rel, want := range map[string]struct {
		parent string
	}{
		"reclaim":                      {parent: ""},
		"reclaim/direct-0":             {parent: "reclaim"},
		"reclaim/nested":               {parent: "reclaim"},
		"reclaim/nested/deep/bucket-1": {parent: "reclaim/nested"},
	} {
		got, ok := byRel[rel]
		if !ok {
			t.Fatalf("missing topology spec %q in %#v", rel, specs)
		}
		if got.ParentRel != want.parent {
			t.Fatalf("spec %q parent = %q, want %q", rel, got.ParentRel, want.parent)
		}
		if got.Mems != "" {
			t.Fatalf("spec %q mems = %q, want empty; cpuset_mems plugin owns cpuset.mems", rel, got.Mems)
		}
	}
}

func TestBuildTopologyNodeSpecsFromViewRejectsCrossNUMADesiredCPU(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes: []string{"reclaimed/reclaimed-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(3),
		ReclaimEffective:        machine.NewCPUSet(0, 2),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 2)},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 0},
		2: {NUMANodeID: 1},
		3: {NUMANodeID: 1},
	}
	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, nil, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	dag, err := topology.BuildDAG(specs)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	snapshot := &topology.CompleteSnapshot{
		Entries: map[string]topology.EntryState{
			"kubepods":              {Identity: topology.CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(3)},
			"reclaimed":             {Identity: topology.CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0)},
			"reclaimed/reclaimed-0": {Identity: topology.CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		},
		DomainUnion: map[topology.DomainID]machine.CPUSet{
			topology.DomainPrimary: machine.NewCPUSet(3),
			topology.DomainReclaim: machine.NewCPUSet(0),
		},
	}
	plan, err := topology.BuildPhasePlan(topology.PhasePlanInput{
		Kind:     topology.PhaseExpand,
		DAG:      dag,
		Snapshot: snapshot,
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods":              machine.NewCPUSet(3),
			"reclaimed":             machine.NewCPUSet(0, 2),
			"reclaimed/reclaimed-0": machine.NewCPUSet(0, 2),
		},
		DesiredMemsByRel: map[string]string{
			"reclaimed":             "0-1",
			"reclaimed/reclaimed-0": "0",
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3),
		Budget:      topology.NewBudgetTracker(topology.ConvergenceBudget{}),
	})
	if !errors.Is(err, topology.ErrInvalidReclaimBucketTarget) {
		t.Fatalf("BuildPhasePlan error = %v, want %v", err, topology.ErrInvalidReclaimBucketTarget)
	}
	if len(plan.Operations) != 0 {
		t.Fatalf("invalid NUMA target published writes: %#v", plan.Operations)
	}
}

func TestBuildTopologyNodeSpecsFromViewDoesNotPublishMemsTargetsOrConstraints(t *testing.T) {
	t.Parallel()

	cfg := bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:      "kubepods",
		BulkheadReclaimRelPaths:     []string{"custom-reclaim-root"},
		BulkheadReclaimNumaPrefixes: []string{"custom-reclaim-root/reclaimed-"},
	}
	view := &model.CPUSetPartitionView{
		NonReclaimPool:          machine.NewCPUSet(2, 3),
		ReclaimEffective:        machine.NewCPUSet(0, 1),
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: machine.NewCPUSet(0, 1)},
	}
	cpuDetails := machine.CPUDetails{
		0: {NUMANodeID: 0},
		1: {NUMANodeID: 0},
		2: {NUMANodeID: 1},
		3: {NUMANodeID: 1},
	}
	specs, err := BuildTopologyNodeSpecsFromView(cfg, view, cpuDetails, nil, nil)
	if err != nil {
		t.Fatalf("BuildTopologyNodeSpecsFromView: %v", err)
	}
	dag, err := topology.BuildDAG(specs)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	for _, spec := range specs {
		if spec.Role != topology.TopoNodeRoleReclaimNUMABucket {
			continue
		}
		if !spec.Constraint.MemUpperBound.IsEmpty() {
			t.Fatalf("spec %q mem upper bound = %s, want empty; cpuset_mems plugin owns cpuset.mems",
				spec.Rel, spec.Constraint.MemUpperBound.String())
		}
	}

	plan, err := topology.BuildPhasePlan(topology.PhasePlanInput{
		Kind: topology.PhaseDrain,
		DAG:  dag,
		Snapshot: &topology.CompleteSnapshot{
			Entries: map[string]topology.EntryState{
				"kubepods":                        {Identity: topology.CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0-1"},
				"custom-reclaim-root":             {Identity: topology.CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0-1"},
				"custom-reclaim-root/reclaimed-0": {Identity: topology.CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0-1"},
			},
			DomainUnion: map[topology.DomainID]machine.CPUSet{
				topology.DomainPrimary: machine.NewCPUSet(0, 1, 2, 3),
				topology.DomainReclaim: machine.NewCPUSet(0, 1, 2, 3),
			},
		},
		DesiredByRel: map[string]machine.CPUSet{
			"kubepods":                        machine.NewCPUSet(2, 3),
			"custom-reclaim-root":             machine.NewCPUSet(0, 1),
			"custom-reclaim-root/reclaimed-0": machine.NewCPUSet(0, 1),
		},
		AllowedCPUs: machine.NewCPUSet(0, 1, 2, 3),
		Budget:      topology.NewBudgetTracker(topology.ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan: %v", err)
	}
	for _, operation := range plan.Operations {
		if operation.WriteMems {
			t.Fatalf("operation %q unexpectedly writes cpuset.mems target=%q; operations=%#v",
				operation.Rel, operation.Target.Mems, plan.Operations)
		}
	}
}
