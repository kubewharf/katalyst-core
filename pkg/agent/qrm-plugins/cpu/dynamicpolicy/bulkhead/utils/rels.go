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
	"fmt"
	"strings"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type ContainerRelPathResolveStage string

const (
	ContainerRelPathResolveStageContainerID ContainerRelPathResolveStage = "container_id"
	ContainerRelPathResolveStageCgroupPath  ContainerRelPathResolveStage = "cgroup_path"
)

// ErrContainerIdentityChanged indicates that the container was recreated while
// its cgroup path was being resolved.
var (
	ErrContainerIdentityChanged = errors.New("container identity changed")
	ErrContainerNotRunning      = errors.New("container not running")
)

type containerIdentityRefreshScopeKey struct{}

type containerIdentityRefreshState struct {
	once sync.Once
	err  error
}

type ContainerRelPathResolveError struct {
	Stage ContainerRelPathResolveStage
	Err   error
}

func (e *ContainerRelPathResolveError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("resolve container rel path stage=%s: %v", e.Stage, e.Err)
}

func (e *ContainerRelPathResolveError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ResolveContainerRelPath(metaServer *metaserver.MetaServer, podUID, containerName string) (string, error) {
	return ResolveContainerRelPathWithContext(context.Background(), metaServer, podUID, containerName)
}

func getContainerIdentityFetcher(metaServer *metaserver.MetaServer) (metapod.ContainerIdentityFetcher, error) {
	if metaServer == nil || metaServer.MetaAgent == nil || metaServer.PodFetcher == nil {
		return nil, fmt.Errorf("nil pod fetcher")
	}
	fetcher, ok := metaServer.PodFetcher.(metapod.ContainerIdentityFetcher)
	if !ok {
		return nil, fmt.Errorf("pod fetcher does not support container identity operations")
	}
	return fetcher, nil
}

func getContainerIDWithContext(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID, containerName string,
) (string, error) {
	fetcher, err := getContainerIdentityFetcher(metaServer)
	if err != nil {
		return "", err
	}
	return fetcher.GetContainerIDWithContext(ctx, podUID, containerName)
}

// WithContainerIdentityRefreshScope limits identity-cache refreshes to one
// attempt for all container resolutions sharing the returned context.
func WithContainerIdentityRefreshScope(ctx context.Context) context.Context {
	if _, ok := ctx.Value(containerIdentityRefreshScopeKey{}).(*containerIdentityRefreshState); ok {
		return ctx
	}
	return context.WithValue(ctx, containerIdentityRefreshScopeKey{}, &containerIdentityRefreshState{})
}

func refreshContainerIdentityCache(ctx context.Context, metaServer *metaserver.MetaServer) error {
	fetcher, err := getContainerIdentityFetcher(metaServer)
	if err != nil {
		return err
	}
	return fetcher.RefreshKubeletPodCache(ctx)
}

// RefreshContainerIdentityCache refreshes the container identity source. When
// called with a refresh-scoped context, all callers observe the same result.
func RefreshContainerIdentityCache(ctx context.Context, metaServer *metaserver.MetaServer) error {
	if state, ok := ctx.Value(containerIdentityRefreshScopeKey{}).(*containerIdentityRefreshState); ok {
		state.once.Do(func() {
			state.err = refreshContainerIdentityCache(ctx, metaServer)
		})
		return state.err
	}
	return refreshContainerIdentityCache(ctx, metaServer)
}

func getFreshContainerIDWithContext(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID, containerName string,
) (string, error) {
	if err := RefreshContainerIdentityCache(ctx, metaServer); err != nil {
		return "", err
	}
	return getContainerIDWithContext(ctx, metaServer, podUID, containerName)
}

func ensureContainerRunning(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID, containerName, containerID string,
) error {
	pod, err := metaServer.GetPod(ctx, podUID)
	if err != nil {
		return fmt.Errorf("get refreshed pod status: %w", err)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != containerName {
			continue
		}
		if status.State.Running == nil {
			return fmt.Errorf("%w: pod=%s container=%s", ErrContainerNotRunning, podUID, containerName)
		}
		fetcher, err := getContainerIdentityFetcher(metaServer)
		if err != nil {
			return err
		}
		running, err := fetcher.IsContainerRunningInRuntime(
			ctx, podUID, containerName, containerID,
		)
		if err != nil {
			return fmt.Errorf("verify runtime container state: %w", err)
		}
		if !running {
			return fmt.Errorf("%w: pod=%s container=%s", ErrContainerNotRunning, podUID, containerName)
		}
		return nil
	}
	return fmt.Errorf("%w: pod=%s container=%s", metapod.ErrContainerNotFound, podUID, containerName)
}

func ResolveContainerRelPathWithContext(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID, containerName string,
) (string, error) {
	rel, _, err := ResolveContainerRelPathAndIDWithContext(ctx, metaServer, podUID, containerName)
	return rel, err
}

// ResolveContainerRelPathAndIDWithContext returns both the relative cgroup path
// and the exact container ID used to resolve that path. Callers that need to
// validate the resolved leaf against a later Pod snapshot must compare this ID,
// rather than performing another identity lookup.
func ResolveContainerRelPathAndIDWithContext(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID, containerName string,
) (string, string, error) {
	rel, containerID, err := ResolveContainerRelPathAndIDCacheOnlyWithContext(
		ctx, metaServer, podUID, containerName)
	if err == nil {
		return rel, containerID, nil
	}
	var resolveErr *ContainerRelPathResolveError
	if !errors.As(err, &resolveErr) || resolveErr.Stage != ContainerRelPathResolveStageCgroupPath {
		return "", containerID, err
	}

	currentContainerID, confirmErr := getFreshContainerIDWithContext(ctx, metaServer, podUID, containerName)
	if confirmErr != nil {
		return "", "", &ContainerRelPathResolveError{
			Stage: ContainerRelPathResolveStageContainerID,
			Err:   fmt.Errorf("confirm container identity after cgroup path failure: %w", confirmErr),
		}
	}
	if currentContainerID != containerID {
		return "", "", &ContainerRelPathResolveError{
			Stage: ContainerRelPathResolveStageCgroupPath,
			Err: fmt.Errorf("%w: previous=%s current=%s",
				ErrContainerIdentityChanged, containerID, currentContainerID),
		}
	}
	if runningErr := ensureContainerRunning(ctx, metaServer, podUID, containerName, currentContainerID); runningErr != nil {
		return "", "", &ContainerRelPathResolveError{
			Stage: ContainerRelPathResolveStageContainerID,
			Err:   runningErr,
		}
	}
	return "", "", err
}

// ResolveContainerRelPathAndIDCacheOnlyWithContext resolves against the current
// identity cache without refreshing it. If cgroup path resolution fails, the
// returned ID is the exact cached identity used for that failed attempt.
func ResolveContainerRelPathAndIDCacheOnlyWithContext(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	podUID, containerName string,
) (string, string, error) {
	if metaServer == nil {
		return "", "", fmt.Errorf("nil metaServer")
	}
	if metaServer.MetaAgent == nil || metaServer.PodFetcher == nil {
		return "", "", fmt.Errorf("nil pod fetcher")
	}

	containerID, err := getContainerIDWithContext(ctx, metaServer, podUID, containerName)
	if err != nil {
		return "", "", &ContainerRelPathResolveError{Stage: ContainerRelPathResolveStageContainerID, Err: err}
	}
	rel, err := cgcommon.GetContainerRelativeCgroupPath(podUID, containerID)
	if err != nil {
		return "", containerID, &ContainerRelPathResolveError{
			Stage: ContainerRelPathResolveStageCgroupPath,
			Err:   err,
		}
	}
	return strings.Trim(rel, "/"), containerID, nil
}

func CollectActiveRels(
	cfg bulkheadconfig.BulkheadConfiguration,
	view *model.CPUSetPartitionView,
	metaServer *metaserver.MetaServer,
	reclaimSiblings []string,
	relExists RelExistsFunc,
) map[string]struct{} {
	out := map[string]struct{}{}
	out[""] = struct{}{}

	addIfExists := func(rel string) {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			return
		}
		if relExists != nil {
			if err := relExists(rel); err != nil {
				general.InfofV(5, "bulkhead: active rel path does not exist, skipping, rel=%q err=%v", rel, err)
				return
			}
		}
		out[rel] = struct{}{}
	}

	addIfExists(cfg.BulkheadPrimaryRelPath)
	for _, rel := range cfg.BulkheadReclaimRelPaths {
		addIfExists(rel)
	}
	for _, rel := range cfg.BulkheadPartitionRelPaths {
		addIfExists(rel)
	}
	for _, rel := range reclaimSiblings {
		addIfExists(rel)
	}

	if view != nil {
		for reclaimIdx := range cfg.BulkheadReclaimRelPaths {
			for numaID := range view.ReclaimEffectivePerNUMA {
				addIfExists(cfg.ReclaimPerNUMA(reclaimIdx, numaID))
			}
		}
	}

	if view != nil && metaServer != nil {
		for podUID, containers := range view.ContainerCPUSetByPod {
			for containerName := range containers {
				rel, err := ResolveContainerRelPath(metaServer, podUID, containerName)
				if err != nil {
					general.InfofV(5, "bulkhead: CollectActiveRels resolve container rel failed, pod=%q container=%q err=%v",
						podUID, containerName, err)
					continue
				}
				addIfExists(rel)
			}
		}
	}
	return out
}
