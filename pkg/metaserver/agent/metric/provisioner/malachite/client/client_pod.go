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

package client

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	cgroupcm "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func (c *MalachiteClient) GetAllPodContainersStats(ctx context.Context) (map[string]map[string]*types.MalachiteCgroupInfo, error) {
	pods, err := c.fetcher.GetPodList(ctx, func(_ *v1.Pod) bool { return true })
	if err != nil {
		return nil, fmt.Errorf("GetAllPodContainersStats fetch-pods err %v", err)
	}

	podsContainersStats := make(map[string]map[string]*types.MalachiteCgroupInfo)
	for _, pod := range pods {
		stats, err := c.GetPodStats(ctx, string(pod.UID))
		if err != nil {
			general.Errorf("GetAllPodContainersStats err %v", err)
			continue
		} else if len(stats) == 0 {
			continue
		}
		podsContainersStats[string(pod.UID)] = stats
	}
	return podsContainersStats, nil
}

func (c *MalachiteClient) GetPodStats(ctx context.Context, podUID string) (map[string]*types.MalachiteCgroupInfo, error) {
	pod, err := c.fetcher.GetPod(ctx, podUID)
	if err != nil {
		return nil, fmt.Errorf("GetPodStats %s fetch-pod err %v", podUID, err)
	}

	containersStats := make(map[string]*types.MalachiteCgroupInfo)
	for _, containerStatus := range pod.Status.ContainerStatuses {
		containerID := native.TrimContainerIDPrefix(containerStatus.ContainerID)
		stats, err := c.GetPodContainerStats(podUID, containerID)
		if err != nil {
			general.Errorf("GetPodStats err %v", err)
			continue
		}
		containersStats[containerStatus.Name] = stats
	}
	return containersStats, nil
}

func (c *MalachiteClient) GetPodContainerStats(podUID, containerID string) (*types.MalachiteCgroupInfo, error) {
	var cgroupPath string
	var err error

	// if relativePathFunc has been set, we should use it
	if c.relativePathFunc != nil {
		cgroupPath, err = (*c.relativePathFunc)(podUID, containerID)
	} else {
		cgroupPath, err = cgroupcm.GetContainerRelativeCgroupPath(podUID, containerID)
	}
	if err != nil {
		return nil, fmt.Errorf("GetPodContainerStats %s/%v get-relative-path err %v", podUID, containerID, err)
	}

	containersStats, err := c.GetCgroupStats(cgroupPath)
	if err != nil {
		return nil, fmt.Errorf("GetPodContainerStats %s/%v get-status %v err %v", podUID, containerID, cgroupPath, err)
	}
	return containersStats, nil
}
