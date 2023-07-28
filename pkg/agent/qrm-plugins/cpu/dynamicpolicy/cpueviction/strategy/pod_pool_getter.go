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

package strategy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var getPodPoolMapFunc = DefaultGetPodPoolMapFunc

// PodPoolMap is a map keyed by pod UID, the value is a map keyed by container name and its value is
// the container info with owner pool.
type PodPoolMap map[string]map[string]*ContainerOwnerPoolInfo

func (p PodPoolMap) PutContainerOwnerPoolInfo(podUID string, containerName string, ownerPool string) {
	containerOwnerPoolInfo := &ContainerOwnerPoolInfo{
		PodUID:        podUID,
		ContainerName: containerName,
		OwnerPool:     ownerPool,
	}

	if podMap, ok := p[podUID]; ok {
		podMap[containerName] = containerOwnerPoolInfo
	} else {
		podMap := map[string]*ContainerOwnerPoolInfo{containerName: containerOwnerPoolInfo}
		p[podUID] = podMap
	}
}

// GetPodPoolMapFunc returns a map keyed by pod UID, the value is a map keyed by container name and its value is
// the container info with owner pool.
type GetPodPoolMapFunc func(pod.PodFetcher, state.ReadonlyState) PodPoolMap

type ContainerOwnerPoolInfo struct {
	PodUID        string
	ContainerName string
	OwnerPool     string
}

// SetGetPodPoolMapFunc provides a hook to change the implementation of GetPodPoolMapFunc
func SetGetPodPoolMapFunc(f GetPodPoolMapFunc) {
	general.Infof("SetGetPodPoolMapFunc called")
	getPodPoolMapFunc = f
}

var DefaultGetPodPoolMapFunc GetPodPoolMapFunc = func(fetcher pod.PodFetcher, readonlyState state.ReadonlyState) PodPoolMap {
	result := make(PodPoolMap)

	for podUID, entry := range readonlyState.GetPodEntries() {
		if entry.IsPoolEntry() {
			continue
		}

		for containerName, containerEntry := range entry {
			if containerEntry == nil {
				continue
			} else if containerEntry.OwnerPoolName == "" {
				general.Infof("skip get pool name for pod: %s, "+
					"container: %s with owner pool name: %s", podUID, containerName, containerEntry.OwnerPoolName)
				continue
			}

			result.PutContainerOwnerPoolInfo(podUID, containerName, containerEntry.OwnerPoolName)
		}
	}

	return result
}
