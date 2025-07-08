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
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"

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

type PolicyNUMAAware struct {
	*PolicyBase

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom     resource.Quantity
	numaMemoryHeadroom map[int]resource.Quantity
	updateStatus       types.PolicyUpdateStatus

	conf *config.Configuration

	numaBindingReclaimRelativeRootCgroupPaths map[int]string
}

func NewPolicyNUMAAware(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter,
) HeadroomPolicy {
	p := PolicyNUMAAware{
		PolicyBase:         NewPolicyBase(metaReader, metaServer),
		numaMemoryHeadroom: make(map[int]resource.Quantity),
		updateStatus:       types.PolicyUpdateFailed,
		conf:               conf,
		numaBindingReclaimRelativeRootCgroupPaths: common.GetNUMABindingReclaimRelativeRootCgroupPaths(conf.ReclaimRelativeRootCgroupPath,
			metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt()),
	}

	return &p
}

func (p *PolicyNUMAAware) Name() types.MemoryHeadroomPolicyName {
	return types.MemoryHeadroomPolicyNUMAAware
}

func (p *PolicyNUMAAware) Update() (err error) {
	defer func() {
		if err != nil {
			p.updateStatus = types.PolicyUpdateFailed
		} else {
			p.updateStatus = types.PolicyUpdateSucceeded
		}
	}()

	var (
		reclaimableMemory     float64 = 0
		numaReclaimableMemory map[int]float64
		availNUMATotal        float64 = 0
		reservedForAllocate   float64 = 0
		data                  metric.MetricData
	)
	dynamicConfig := p.conf.GetDynamicConfiguration()

	availNUMAs, reclaimedCoresContainers, err := helper.GetAvailableNUMAsAndReclaimedCores(p.conf, p.metaReader, p.metaServer)
	if err != nil {
		general.Errorf("GetAvailableNUMAsAndReclaimedCores failed: %v", err)
		return err
	}

	numaReclaimableMemory = make(map[int]float64)
	for _, numaID := range availNUMAs.ToSliceInt() {
		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			general.Errorf("Can not get numa memory free, numaID: %v, %v", numaID, err)
			return err
		}
		free := data.Value

		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemInactiveFileNuma)
		if err != nil {
			general.Errorf("Can not get numa memory inactiveFile, numaID: %v, %v", numaID, err)
			return err
		}
		inactiveFile := data.Value

		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			general.Errorf("Can not get numa memory total, numaID: %v, %v", numaID, err)
			return err
		}
		total := data.Value
		availNUMATotal += total
		reservedForAllocate += p.essentials.ReservedForAllocate / float64(p.metaServer.NumNUMANodes)

		numaReclaimable := free + inactiveFile*dynamicConfig.CacheBasedRatio

		general.InfoS("NUMA memory info", "numaID", numaID,
			"total", general.FormatMemoryQuantity(total), "free", general.FormatMemoryQuantity(free),
			"inactiveFile", general.FormatMemoryQuantity(inactiveFile), "CacheBasedRatio", dynamicConfig.CacheBasedRatio,
			"numaReclaimable", general.FormatMemoryQuantity(numaReclaimable),
		)

		reclaimableMemory += numaReclaimable
		numaReclaimableMemory[numaID] = numaReclaimable
	}

	for _, container := range reclaimedCoresContainers {
		if container.MemoryRequest > 0 && len(container.TopologyAwareAssignments) > 0 {
			reclaimableMemory += container.MemoryRequest
			reclaimableMemoryPerNuma := container.MemoryRequest / float64(len(container.TopologyAwareAssignments))
			for numaID := range container.TopologyAwareAssignments {
				numaReclaimableMemory[numaID] += reclaimableMemoryPerNuma
			}
		}
	}

	watermarkScaleFactor, err := p.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		general.Infof("Can not get system watermark scale factor: %v", err)
		return err
	}

	// reserve memory for watermark_scale_factor to make kswapd less happened
	systemWatermarkReserved := availNUMATotal * watermarkScaleFactor.Value / 10000

	memoryHeadroom := math.Max(reclaimableMemory-systemWatermarkReserved-reservedForAllocate, 0)
	reduceRatio := 0.0
	if reclaimableMemory > 0 {
		reduceRatio = memoryHeadroom / reclaimableMemory
	}

	allNUMAs := p.metaServer.CPUDetails.NUMANodes().ToSliceInt()
	numaHeadroom := make(map[int]float64, len(allNUMAs))
	totalNUMAHeadroom := 0.0
	for numaID := range numaReclaimableMemory {
		numaHeadroom[numaID] = numaReclaimableMemory[numaID] * reduceRatio
		totalNUMAHeadroom += numaHeadroom[numaID]
		general.InfoS("numa memory headroom", "NUMA-ID", numaID, "headroom", general.FormatMemoryQuantity(numaHeadroom[numaID]))
	}

	// revise memory headroom by dynamic config
	numaHeadroom, totalNUMAHeadroom, err = p.reviseNUMAHeadroomMemory(dynamicConfig, numaHeadroom, totalNUMAHeadroom, availNUMAs)
	if err != nil {
		general.Errorf("reviseNUMAHeadroomMemory failed: %v", err)
		return err
	}

	numaHeadroomQuantity := make(map[int]resource.Quantity, len(allNUMAs))
	for _, numaID := range allNUMAs {
		if _, ok := numaHeadroom[numaID]; !ok {
			numaHeadroomQuantity[numaID] = *resource.NewQuantity(0, resource.BinarySI)
		} else {
			numaHeadroomQuantity[numaID] = *resource.NewQuantity(int64(numaHeadroom[numaID]), resource.BinarySI)
		}
		general.InfoS("revised numa memory headroom", "NUMA-ID", numaID, "headroom", general.FormatMemoryQuantity(numaHeadroom[numaID]))
	}

	p.numaMemoryHeadroom = numaHeadroomQuantity
	p.memoryHeadroom = *resource.NewQuantity(int64(totalNUMAHeadroom), resource.BinarySI)

	general.InfoS("total memory reclaimable",
		"reclaimableMemory", general.FormatMemoryQuantity(reclaimableMemory),
		"memoryHeadroom", general.FormatMemoryQuantity(memoryHeadroom),
		"ResourceUpperBound", general.FormatMemoryQuantity(p.essentials.ResourceUpperBound),
		"systemWatermarkReserved", general.FormatMemoryQuantity(systemWatermarkReserved),
		"reservedForAllocate", general.FormatMemoryQuantity(reservedForAllocate),
		"totalNUMAHeadroom", general.FormatMemoryQuantity(totalNUMAHeadroom),
		"numaHeadroom", numaHeadroom,
	)
	return nil
}

func (p *PolicyNUMAAware) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, nil, fmt.Errorf("last update failed")
	}

	return p.memoryHeadroom, p.numaMemoryHeadroom, nil
}

func (p *PolicyNUMAAware) getReclaimMemoryLimitAndUtil(actualNUMABindingNUMAs, nonActualNUMABindingNUMAs machine.CPUSet) (map[int]float64, map[int]float64, error) {
	numaReclaimMemoryLimit := make(map[int]float64, actualNUMABindingNUMAs.Size()+nonActualNUMABindingNUMAs.Size())
	numaUtil := make(map[int]float64, actualNUMABindingNUMAs.Size()+nonActualNUMABindingNUMAs.Size())
	for _, numaID := range actualNUMABindingNUMAs.ToSliceNoSortInt() {
		cgroupPath := p.numaBindingReclaimRelativeRootCgroupPaths[numaID]
		data, err := p.metaServer.GetCgroupMetric(cgroupPath, consts.MetricMemLimitCgroup)
		if err != nil {
			return nil, nil, fmt.Errorf("get cgroup %s metric failed: %v", cgroupPath, err)
		}
		limit := data.Value

		numaReclaimMemoryLimit[numaID] = limit

		data, err = p.metaServer.GetCgroupMetric(cgroupPath, consts.MetricMemUsageCgroup)
		if err != nil {
			return nil, nil, fmt.Errorf("get cgroup %s metric failed: %v", cgroupPath, err)
		}
		util := data.Value / limit
		if util > 1 {
			util = 1
		} else if util < 0 {
			util = 0
		}
		numaUtil[numaID] = util
	}

	cgroupMetric, err := p.metaServer.GetCgroupMetric(p.conf.ReclaimRelativeRootCgroupPath, consts.MetricMemLimitCgroup)
	if err != nil {
		return nil, nil, err
	}
	reclaimMemoryLimit := cgroupMetric.Value

	cgroupMetric, err = p.metaServer.GetCgroupMetric(p.conf.ReclaimRelativeRootCgroupPath, consts.MetricMemUsageCgroup)
	if err != nil {
		return nil, nil, err
	}
	reclaimMemoryUtil := cgroupMetric.Value / reclaimMemoryLimit
	if reclaimMemoryUtil > 1 {
		reclaimMemoryUtil = 1
	} else if reclaimMemoryUtil < 0 {
		reclaimMemoryUtil = 0
	}

	if !nonActualNUMABindingNUMAs.IsEmpty() {
		reclaimMemoryLimitPerNUMA := reclaimMemoryLimit / float64(nonActualNUMABindingNUMAs.Size())
		for _, numaID := range nonActualNUMABindingNUMAs.ToSliceNoSortInt() {
			numaReclaimMemoryLimit[numaID] = reclaimMemoryLimitPerNUMA
			numaUtil[numaID] = reclaimMemoryUtil
		}
	}

	return numaReclaimMemoryLimit, numaUtil, nil
}

// reviseNUMAHeadroomMemory adjusts reclaimable memory based on NUMA configuration and oversold rate settings
// It ensures memory allocation doesn't exceed limits set by MaxOversoldRate for both NUMA-bound and non-NUMA-bound memory
// Returns:
//   - Revised NUMA-specific reclaimable memory map
//   - Total revised reclaimable memory
//   - Error if any occurs during the adjustment process
func (p *PolicyNUMAAware) reviseNUMAHeadroomMemory(
	conf *dynamic.Configuration,
	numaHeadroom map[int]float64,
	totalNUMAHeadroom float64,
	availNUMAs machine.CPUSet,
) (map[int]float64, float64, error) {
	// if MaxOversoldRate <= 0, we will not revise reclaimable memory
	maxOversoldRate := conf.MemoryUtilBasedConfiguration.MaxOversoldRate
	if maxOversoldRate <= 0 {
		return numaHeadroom, totalNUMAHeadroom, nil
	}

	// get actual-numa-binding numa
	actualNUMABindingNUMAs, err := helper.GetActualNUMABindingNUMAsForReclaimedCores(p.conf, p.metaServer)
	if err != nil {
		return nil, 0, err
	}

	nonActualNUMABindingNUMAs := availNUMAs.Difference(actualNUMABindingNUMAs)
	numaReclaimMemoryLimit, numaReclaimMemoryUtil, err := p.getReclaimMemoryLimitAndUtil(actualNUMABindingNUMAs, nonActualNUMABindingNUMAs)
	if err != nil {
		return nil, 0, err
	}

	general.InfoS("NUMA memory headroom raw data", "numaReclaimMemoryLimit", numaReclaimMemoryLimit, "maxOversoldRate", maxOversoldRate, "numaReclaimMemoryUtil", numaReclaimMemoryUtil)

	revisedNUMAHeadroom := make(map[int]float64, len(numaHeadroom))
	revisedTotalNUMAHeadroom := 0.
	for numaID, memory := range numaHeadroom {

		/*
			 oversold rate  ^
							|-----
							|     \
							|      \
							|       \
							|        \
							|         \
							|          \
							|           \
							|            \
							|             \
							|              \
							|               \-------
							|----------------------> util
							0          0.5          1
		*/
		// FIXME: configurable
		scaleFactor, err := strconv.ParseFloat(os.Getenv("scaleFactor"), 64)
		if err != nil {
			scaleFactor = 5
		}
		maxOversoldRate, err = strconv.ParseFloat(os.Getenv("maxOversoldRate"), 64)
		if err != nil {
			maxOversoldRate = 4
		}

		overSoldRate := (maxOversoldRate-1)/2*(1-math.Tanh((numaReclaimMemoryUtil[numaID]-0.5)*scaleFactor)) + 1
		if maxOversoldRate < 1 {
			overSoldRate = maxOversoldRate
		}

		//revisedNUMAHeadroom[numaID] = math.Min(memory, numaReclaimMemoryLimit[numaID]*overSoldRate)
		revisedNUMAHeadroom[numaID] = numaReclaimMemoryLimit[numaID] * overSoldRate

		general.InfoS("revised detail", "raw headroom", general.FormatMemoryQuantity(memory),
			"memoryLimit", general.FormatMemoryQuantity(numaReclaimMemoryLimit[numaID]),
			"revisedNUMAHeadroom", general.FormatMemoryQuantity(revisedNUMAHeadroom[numaID]),
			"overSoldRate", overSoldRate, "maxOversoldRate", maxOversoldRate, "numaID", numaID)

		revisedTotalNUMAHeadroom += revisedNUMAHeadroom[numaID]
	}

	return revisedNUMAHeadroom, revisedTotalNUMAHeadroom, nil
}
