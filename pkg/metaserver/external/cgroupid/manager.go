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

package cgroupid

import (
	"context"
	"fmt"
)

// CgroupIDManager maintains the mapping of pod to cgroup id.
type CgroupIDManager interface {
	Run(ctx context.Context)

	GetCgroupIDForContainer(podUID, containerID string) (uint64, error)
	ListCgroupIDsForPod(podUID string) ([]uint64, error)
}

type CgroupIDManagerStub struct {
	ContainerCGroupIDMap map[string]map[string]uint64
}

func (c *CgroupIDManagerStub) Run(_ context.Context) {
}

func (c *CgroupIDManagerStub) GetCgroupIDForContainer(podUID, containerID string) (uint64, error) {
	if containerCGroupIDMap, ok := c.ContainerCGroupIDMap[podUID]; ok {
		if containerCGroupID, ok := containerCGroupIDMap[containerID]; ok {
			return containerCGroupID, nil
		}
	}
	return 0, fmt.Errorf("container %s not found in cgroup id %s", containerID, podUID)
}

func (c *CgroupIDManagerStub) ListCgroupIDsForPod(podUID string) ([]uint64, error) {
	if containerCGroupIDMap, ok := c.ContainerCGroupIDMap[podUID]; ok {
		var containerCGroupIDs []uint64
		for _, containerCGroupID := range containerCGroupIDMap {
			containerCGroupIDs = append(containerCGroupIDs, containerCGroupID)
		}
		return containerCGroupIDs, nil
	}

	return nil, fmt.Errorf("container %s not found in cgroup id %s", podUID, podUID)
}
