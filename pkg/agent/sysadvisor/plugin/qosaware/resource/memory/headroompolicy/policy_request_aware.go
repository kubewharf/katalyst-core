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

package headroompolicy

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type PolicyRequestAware struct {
	*PolicyBase

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom     resource.Quantity
	numaMemoryHeadroom map[int]resource.Quantity
	updateStatus       types.PolicyUpdateStatus

	conf *config.Configuration

	numaBindingReclaimRelativeRootCgroupPaths map[int]string
}

func NewPolicyRequestAware(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter,
) HeadroomPolicy {
	p := PolicyRequestAware{
		PolicyBase:         NewPolicyBase(metaReader, metaServer),
		numaMemoryHeadroom: make(map[int]resource.Quantity),
		updateStatus:       types.PolicyUpdateFailed,
		conf:               conf,
		numaBindingReclaimRelativeRootCgroupPaths: common.GetNUMABindingReclaimRelativeRootCgroupPaths(conf.ReclaimRelativeRootCgroupPath,
			metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()),
	}

	return &p
}

func (p *PolicyRequestAware) Name() types.MemoryHeadroomPolicyName {
	return types.MemoryHeadroomPolicyRequestAware
}

func (p *PolicyRequestAware) Update() (err error) {
	defer func() {
		if err != nil {
			p.updateStatus = types.PolicyUpdateFailed
		} else {
			p.updateStatus = types.PolicyUpdateSucceeded
		}
	}()

	if !p.metaServer.HasSynced() {
		return fmt.Errorf("metaServer not synced")
	}

	var data metric.MetricData

	availNUMAs, _, err := helper.GetAvailableNUMAsAndReclaimedCores(p.conf, p.metaReader, p.metaServer)
	if err != nil {
		general.Errorf("GetAvailableNUMAsAndReclaimedCores failed: %v", err)
		return err
	}

	numaReclaimable := make(map[int]float64)
	totalReclaimable := 0.0

	// headroom0: max(total - limit, 0)
	// headroom1: [limit - max(used, request)] * factor1 <-0.5
	// headroom2: max(request-used, 0) * factor2 <-0.2

	// total NUMA memory
	for _, numaID := range availNUMAs.ToSliceInt() {
		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			general.Errorf("Can not get numa memory total, numaID: %v, %v", numaID, err)
			return err
		}
		numaReclaimable[numaID] = data.Value
	}

	// headroom0: max(total - limit, 0)
	p.metaReader.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if ci == nil {
			return true
		}
		// only shared_cores / dedicated_cores pods consume regular memory budget;
		if ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores &&
			ci.QoSLevel != apiconsts.PodAnnotationQoSLevelDedicatedCores {
			return true
		}
		for numaID := range ci.TopologyAwareAssignments {
			if !availNUMAs.Contains(numaID) {
				continue
			}
			numaReclaimable[numaID] = general.MaxFloat64(0, numaReclaimable[numaID]-ci.MemoryLimit/float64(len(ci.TopologyAwareAssignments)))
		}
		return true
	})

	// headroom1: [limit - max(used, request)] * factor1 <-0.5
	// headroom2: max(request-used, 0) * factor2 <-0.3
	p.metaReader.RangeContainer(func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if ci == nil {
			return true
		}
		// only shared_cores / dedicated_cores pods consume regular memory budget;
		if ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores &&
			ci.QoSLevel != apiconsts.PodAnnotationQoSLevelDedicatedCores {
			return true
		}
		for numaID := range ci.TopologyAwareAssignments {
			if !availNUMAs.Contains(numaID) {
				continue
			}

			data, err = p.metaServer.GetContainerNumaMetric(podUID, containerName, numaID, consts.MetricsMemTotalPerNumaContainer)
			if err != nil {
				general.ErrorS(err, "Can not get container numa memory total", "numaID", numaID, "containerName", containerName, "podName", ci.PodName)
				return true
			}
			limit := ci.MemoryLimit / float64(len(ci.TopologyAwareAssignments))
			request := ci.MemoryRequest / float64(len(ci.TopologyAwareAssignments))
			used := data.Value
			numaReclaimable[numaID] += general.MaxFloat64(0, limit-general.MaxFloat64(used, request)) * 0.6
			numaReclaimable[numaID] += general.MaxFloat64(0, request-used) * 0.3
			klog.V(5).InfoS("container memory", "numaID", numaID, "containerName", containerName, "podName", ci.PodName,
				"request", request, "used", used, "limit", limit)
		}
		return true
	})

	numaReclaimable, err = p.reviseNUMAHeadroomMemory(p.conf.GetDynamicConfiguration(), numaReclaimable, availNUMAs)
	if err != nil {
		return err
	}

	allNUMAs := p.metaServer.CPUDetails.NUMANodes().ToSliceInt()

	numaHeadroomQuantity := make(map[int]resource.Quantity, len(allNUMAs))
	for _, numaID := range allNUMAs {
		if _, ok := numaReclaimable[numaID]; !ok {
			numaHeadroomQuantity[numaID] = *resource.NewQuantity(0, resource.BinarySI)
		} else {
			totalReclaimable += numaReclaimable[numaID]
			numaHeadroomQuantity[numaID] = *resource.NewQuantity(int64(numaReclaimable[numaID]), resource.BinarySI)
		}
	}

	// 是否还需要考虑下系统内存开销？

	p.numaMemoryHeadroom = numaHeadroomQuantity
	p.memoryHeadroom = *resource.NewQuantity(int64(totalReclaimable), resource.BinarySI)

	general.InfoS("memory reclaimable info",
		"reclaimableMemory", general.FormatMemoryQuantity(totalReclaimable),
		"numaReclaimable", numaReclaimable,
	)

	return nil
}

func (p *PolicyRequestAware) getReclaimMemoryLimit() (map[int]float64, error) {
	numaReclaimMemoryLimit := make(map[int]float64)
	for _, numaID := range p.metaServer.CPUDetails.NUMANodes().ToSliceInt() {
		cgroupPath := p.numaBindingReclaimRelativeRootCgroupPaths[numaID]
		data, err := p.metaServer.GetCgroupMetric(cgroupPath, consts.MetricMemLimitCgroup)
		if err != nil {
			return nil, fmt.Errorf("get cgroup %s metric failed: %v", cgroupPath, err)
		}

		numaReclaimMemoryLimit[numaID] = data.Value
	}

	cgroupMetric, err := p.metaServer.GetCgroupMetric(p.conf.ReclaimRelativeRootCgroupPath, consts.MetricMemLimitCgroup)
	if err != nil {
		return nil, err
	}
	numaReclaimMemoryLimit[-1] = cgroupMetric.Value

	return numaReclaimMemoryLimit, nil
}

func (p *PolicyRequestAware) reviseNUMAHeadroomMemory(
	conf *dynamic.Configuration,
	numaHeadroom map[int]float64,
	availNUMAs machine.CPUSet,
) (map[int]float64, error) {
	// if MaxOversoldRate <= 0, we will not revise reclaimable memory
	maxOversoldRate := conf.MemoryUtilBasedConfiguration.MaxOversoldRate
	if maxOversoldRate <= 0 {
		return numaHeadroom, nil
	}

	numaReclaimMemoryLimit, err := p.getReclaimMemoryLimit()
	if err != nil {
		return nil, err
	}

	general.InfoS("NUMA memory headroom raw data", "numaReclaimMemoryLimit", numaReclaimMemoryLimit,
		"raw numaHeadroom", numaHeadroom, "maxOversoldRate", maxOversoldRate)

	revisedNUMAHeadroom := make(map[int]float64, len(numaHeadroom))
	for numaID, memory := range numaHeadroom {
		revisedNUMAHeadroom[numaID] = math.Min(memory, numaReclaimMemoryLimit[numaID]*maxOversoldRate)
	}

	return revisedNUMAHeadroom, nil
}

func (p *PolicyRequestAware) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, nil, fmt.Errorf("last update failed")
	}

	return p.memoryHeadroom, p.numaMemoryHeadroom, nil
}
