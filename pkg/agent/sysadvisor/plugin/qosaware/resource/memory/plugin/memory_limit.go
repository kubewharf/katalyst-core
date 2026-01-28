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
	"strconv"

	"go.uber.org/atomic"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	MemoryLimit = "memory-limit"

	memoryLimitStatusSucceeded = "succeeded"
	memoryLimitStatusFailed    = "failed"
)

type memoryLimit struct {
	metaReader            metacache.MetaReader
	metaServer            *metaserver.MetaServer
	emitter               metrics.MetricEmitter
	reconcileStatus       *atomic.String
	reclaimMemoryLimitMap map[string]map[memoryadvisor.MemoryControlKnobName]string // cgroupPath -> cgroupFile(memory.limit_in_bytes) -> target value
	getCgroupPathFunc     func(string, string) (string, error)
}

func NewMemoryLimit(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memoryLimit{
		metaReader:        metaReader,
		metaServer:        metaServer,
		emitter:           emitter,
		reconcileStatus:   atomic.NewString(memoryLimitStatusFailed),
		getCgroupPathFunc: common.GetContainerRelativeCgroupPath,
	}
}

func (ml *memoryLimit) Reconcile(status *types.MemoryPressureStatus) error {
	ml.reconcileStatus.Store(memoryLimitStatusFailed)

	mp := make(map[string]map[memoryadvisor.MemoryControlKnobName]string)
	ml.metaReader.RangeContainer(func(podUID string, containerName string, containerInfo *types.ContainerInfo) bool {
		if containerInfo.QoSLevel != consts.PodAnnotationQoSLevelReclaimedCores {
			return true
		}

		containerID, err := ml.metaServer.GetContainerID(podUID, containerName)
		if err != nil {
			general.ErrorS(err, "failed to get container id", "podUID", podUID, "containerName", containerName)
			return false
		}
		cgroupPath, err := ml.getCgroupPathFunc(podUID, containerID)
		if err != nil {
			general.ErrorS(err, "failed to get container cgroup path", "podUID", podUID, "containerName", containerName)
			return false
		}
		container, err := ml.metaServer.GetContainerSpec(podUID, containerName)
		if err != nil {
			general.ErrorS(err, "failed to get container spec", "podUID", podUID, "containerName", containerName)
			return false
		}

		var limit int64 = -1
		if q := native.DefaultMemoryQuantityGetter(container.Resources.Limits); q.Value() > 0 {
			limit = q.Value()
		}
		mp[cgroupPath] = map[memoryadvisor.MemoryControlKnobName]string{
			memoryadvisor.ControlKnobKeyMemoryLimitInBytes: strconv.FormatInt(limit, 10),
		}
		return true
	})
	ml.reclaimMemoryLimitMap = mp
	general.InfoS("reclaimed pod memory limit", "target", mp)

	ml.reconcileStatus.Store(memoryLimitStatusSucceeded)
	return nil
}

func (ml *memoryLimit) GetAdvices() types.InternalMemoryCalculationResult {
	if ml.reconcileStatus.Load() == memoryLimitStatusFailed {
		general.Errorf("failed to get last reconcile result")
		return types.InternalMemoryCalculationResult{}
	}

	var extraEntries []types.ExtraMemoryAdvices
	for cgroupPath, value := range ml.reclaimMemoryLimitMap {
		if value == nil || value[memoryadvisor.ControlKnobKeyMemoryLimitInBytes] == "" {
			continue
		}
		extraEntries = append(extraEntries, types.ExtraMemoryAdvices{
			CgroupPath: cgroupPath,
			Values: map[string]string{
				string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): value[memoryadvisor.ControlKnobKeyMemoryLimitInBytes],
			},
		})
	}

	return types.InternalMemoryCalculationResult{
		ExtraEntries: extraEntries,
	}
}
