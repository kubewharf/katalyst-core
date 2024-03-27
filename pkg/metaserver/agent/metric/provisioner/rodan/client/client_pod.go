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
	"encoding/json"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/types"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func (c *RodanClient) GetPodContainerCPUStats(ctx context.Context, podUID string) (map[string][]types.Cell, error) {
	url := c.urls[types.ContainerCPUPath]

	return c.GetPodContainerStats(ctx, podUID, url)
}

func (c *RodanClient) GetPodContainerCgroupMemStats(ctx context.Context, podUID string) (map[string][]types.Cell, error) {
	url := c.urls[types.ContainerCgroupMemoryPath]

	return c.GetPodContainerStats(ctx, podUID, url)
}

func (c *RodanClient) GetPodContainerLoadStats(ctx context.Context, podUID string) (map[string][]types.Cell, error) {
	url := c.urls[types.ContainerLoadPath]

	return c.GetPodContainerStats(ctx, podUID, url)
}

func (c *RodanClient) GetPodContainerCghardwareStats(ctx context.Context, podUID string) (map[string][]types.Cell, error) {
	url := c.urls[types.ContainerCghardwarePath]

	return c.GetPodContainerStats(ctx, podUID, url)
}

func (c *RodanClient) GetPodContainerCgNumaStats(ctx context.Context, podUId string) (map[string]map[int][]types.Cell, error) {
	url := c.urls[types.ContainerNumaStatPath]

	cghardwareData, err := c.GetPodContainerStats(ctx, podUId, url)
	if err != nil {
		return nil, err
	}

	res := make(map[string]map[int][]types.Cell)
	for container, cells := range cghardwareData {
		res[container] = make(map[int][]types.Cell)

		for _, cell := range cells {
			if cell.Key == "cgnumastat_filepage" {
				continue
			}
			numaNode, metric, err := types.ParseNumastatKey(cell.Key)
			if err != nil {
				return nil, err
			}
			if _, ok := res[container][numaNode]; !ok {
				res[container][numaNode] = make([]types.Cell, 0)
			}
			res[container][numaNode] = append(res[container][numaNode], types.Cell{
				Key: metric,
				Val: cell.Val,
			})
		}
	}

	return res, nil
}

func (c *RodanClient) GetPodContainerStats(ctx context.Context, podUID string, url string) (map[string][]types.Cell, error) {
	pod, err := c.fetcher.GetPod(ctx, podUID)
	if err != nil {
		return nil, fmt.Errorf("GetPodContainerStats %s fetch-pod err %v", podUID, err)
	}

	containersStats := make(map[string][]types.Cell)
	for _, containerStatus := range pod.Status.ContainerStatuses {
		containerID := native.TrimContainerIDPrefix(containerStatus.ContainerID)

		data, err := c.metricFunc(url, map[string]string{
			"container": containerID,
		})
		if err != nil {
			return nil, fmt.Errorf("GetPodContainerStats fail, containerId: %v, err: %v", containerID, err)
		}

		rsp := &types.ContainerResponse{}
		if err = json.Unmarshal(data, &rsp); err != nil {
			err = fmt.Errorf("failed to unmarshal container response, err: %v, data: %v", err, string(data))
			return nil, err
		}

		if len(rsp.Data) == 0 {
			err = fmt.Errorf("GetPodContainerStats fail, containerId: %v, data empty", containerID)
			return nil, err
		}
		if _, ok := rsp.Data[containerID]; !ok {
			err = fmt.Errorf("GetPodContainerStats fail, response without containerId %v", containerID)
			return nil, err
		}
		containersStats[containerStatus.Name] = rsp.Data[containerID]
	}

	return containersStats, nil
}
