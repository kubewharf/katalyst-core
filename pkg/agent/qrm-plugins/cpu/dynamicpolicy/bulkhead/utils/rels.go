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
	"fmt"
	"strings"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type ContainerRelPathResolveStage string

const (
	ContainerRelPathResolveStageContainerID ContainerRelPathResolveStage = "container_id"
	ContainerRelPathResolveStageCgroupPath  ContainerRelPathResolveStage = "cgroup_path"
)

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
	if metaServer == nil {
		return "", fmt.Errorf("nil metaServer")
	}
	containerID, err := metaServer.GetContainerID(podUID, containerName)
	if err != nil {
		return "", &ContainerRelPathResolveError{Stage: ContainerRelPathResolveStageContainerID, Err: err}
	}
	rel, err := cgcommon.GetContainerRelativeCgroupPath(podUID, containerID)
	if err != nil {
		return "", &ContainerRelPathResolveError{Stage: ContainerRelPathResolveStageCgroupPath, Err: err}
	}
	return strings.Trim(rel, "/"), nil
}

func CollectActiveRels(
	cfg bulkheadconfig.BulkheadConfiguration,
	view *CPUSetPartitionView,
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
