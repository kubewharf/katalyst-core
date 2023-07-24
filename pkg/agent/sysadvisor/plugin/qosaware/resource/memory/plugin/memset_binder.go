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

package plugin

import (
	"sync"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	MemsetBinder = "memset-binder"
)

type memsetBinder struct {
	mutex           sync.RWMutex
	metaReader      metacache.MetaReader
	emitter         metrics.MetricEmitter
	containerMemset map[consts.PodContainerName]machine.CPUSet
}

func NewMemsetBinder(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memsetBinder{
		metaReader: metaReader,
		emitter:    emitter,
	}
}

func (mb *memsetBinder) reclaimedContainersFilter(ci *types.ContainerInfo) bool {
	return ci != nil && ci.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores
}

func (mb *memsetBinder) Reconcile(status *types.MemoryPressureStatus) error {
	containerMemset := make(map[consts.PodContainerName]machine.CPUSet)
	containers := make([]*types.ContainerInfo, 0)
	mb.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if mb.reclaimedContainersFilter(containerInfo) {
			containers = append(containers, containerInfo)
		}
		return true
	})

	for _, ci := range containers {
		memset := machine.GetCPUAssignmentNUMAs(ci.TopologyAwareAssignments)
		containerMemset[native.GeneratePodContainerName(ci.PodUID, ci.ContainerName)] = memset
	}
	mb.mutex.Lock()
	defer mb.mutex.Unlock()
	mb.containerMemset = containerMemset

	return nil
}

func (mb *memsetBinder) GetAdvices() types.InternalMemoryCalculationResult {
	mb.mutex.RLock()
	defer mb.mutex.RUnlock()
	result := types.InternalMemoryCalculationResult{}
	for podContainerName, memset := range mb.containerMemset {
		podUID, containerName, err := native.ParsePodContainerName(podContainerName)
		if err != nil {
			general.Errorf("parse podContainerName %v err %v", podContainerName, err)
			continue
		}
		entry := types.ContainerMemoryAdvices{
			PodUID:        podUID,
			ContainerName: containerName,
			Values:        map[string]string{string(memoryadvisor.ControlKnobKeyCPUSetMems): memset.String()},
		}
		result.ContainerEntries = append(result.ContainerEntries, entry)
	}

	return result
}
