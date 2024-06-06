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
	"context"
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/util/errors"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
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
	conf            *config.Configuration
	metaReader      metacache.MetaReader
	metaServer      *metaserver.MetaServer
	emitter         metrics.MetricEmitter
	containerMemset map[consts.PodContainerName]machine.CPUSet
}

func NewMemsetBinder(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memsetBinder{
		conf:       conf,
		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}
}

func (mb *memsetBinder) reclaimedContainersFilter(ci *types.ContainerInfo) bool {
	return ci != nil && ci.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores
}

func (mb *memsetBinder) Reconcile(status *types.MemoryPressureStatus) error {
	var errList []error

	allNUMAs := mb.metaServer.CPUDetails.NUMANodes()

	availNUMAs := allNUMAs

	containerMemset := make(map[consts.PodContainerName]machine.CPUSet)
	containers := make([]*types.ContainerInfo, 0)
	mb.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if mb.reclaimedContainersFilter(containerInfo) {
			containers = append(containers, containerInfo)
			return true
		}

		nodeReclaim := mb.conf.GetDynamicConfiguration().EnableReclaim
		reclaimEnable, err := helper.PodEnableReclaim(context.Background(), mb.metaServer, podUID, nodeReclaim)
		if err != nil {
			errList = append(errList, err)
			return true
		}

		if containerInfo.IsDedicatedNumaExclusive() && !reclaimEnable {
			memset := machine.GetCPUAssignmentNUMAs(containerInfo.TopologyAwareAssignments)
			if memset.IsEmpty() {
				errList = append(errList, fmt.Errorf("container(%v/%v) TopologyAwareAssignments is empty", containerInfo.PodName, containerName))
				return true
			}
			availNUMAs = availNUMAs.Difference(memset)
		}
		return true
	})

	err := errors.NewAggregate(errList)
	if err != nil {
		return err
	}

	if availNUMAs.IsEmpty() {
		availNUMAs = allNUMAs
		general.InfoS("availNUMAs is empty, have to bind all NUMAs to reclaimed_cores containers")
	} else {
		onPressureNUMAs := machine.NewCPUSet()
		for _, numaID := range availNUMAs.ToSliceInt() {
			condition := status.NUMAConditions[numaID]
			if condition != nil && condition.State != types.MemoryPressureNoRisk {
				onPressureNUMAs.Add(numaID)
			}
		}
		if !onPressureNUMAs.Equals(availNUMAs) {
			availNUMAs = availNUMAs.Difference(onPressureNUMAs)
		}
	}

	for _, ci := range containers {
		containerMemset[native.GeneratePodContainerName(ci.PodUID, ci.ContainerName)] = availNUMAs
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
