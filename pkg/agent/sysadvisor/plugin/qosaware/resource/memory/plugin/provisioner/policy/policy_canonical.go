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

package policy

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type PolicyCanonical struct {
	*PolicyBase
	memoryProvisions machine.MemoryDetails
}

func (p *PolicyCanonical) Update() error {
	var (
		memFreeNuma  metric.MetricData
		memTotalNuma metric.MetricData
	)

	memoryProvisions := make(machine.MemoryDetails)
	memoryTotals := make(machine.MemoryDetails)

	// 1. get all numa nodes
	allNUMAs := p.metaServer.CPUDetails.NUMANodes()

	// 2. get all reclaimed cores containers
	availNUMAs, reclaimedCoresContainers, err := helper.GetAvailableNUMAsAndReclaimedCores(p.conf, p.metaReader, p.metaServer)
	if err != nil {
		return err
	}
	// if no available numa nodes, we should set all numa nodes to 0
	nums := availNUMAs.Size()
	if nums == 0 {
		general.Infof("no available numa nodes")
		p.mutex.Lock()
		defer p.mutex.Unlock()
		p.memoryProvisions = memoryProvisions.FillNUMANodesWithZero(allNUMAs)
		return nil
	}

	// 3. get all numa memory provision
	availNUMATotal := float64(0)
	for _, numaID := range availNUMAs.ToSliceInt() {
		memFreeNuma, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			return err
		}

		memTotalNuma, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			return err
		}

		memoryProvisions[numaID] += uint64(memFreeNuma.Value)
		memoryTotals[numaID] += uint64(memTotalNuma.Value)
		availNUMATotal += memTotalNuma.Value
		general.InfoS("numa memory free",
			"numaID", numaID,
			"numaFree", general.FormatMemoryQuantity(memFreeNuma.Value))
	}

	// 4. add reclaimed cores containers memory
	for _, containerInfo := range reclaimedCoresContainers {
		// if nil, we should range all numa nodes
		if containerInfo.TopologyAwareAssignments == nil {
			c := uint64(containerInfo.MemoryRequest) / uint64(nums)
			for numaID := range memoryProvisions {
				memoryProvisions[numaID] += c
			}

		} else {
			memset := machine.GetCPUAssignmentNUMAs(containerInfo.TopologyAwareAssignments)
			if memset.IsEmpty() {
				return fmt.Errorf("container(%v/%v) TopologyAwareAssignments is empty", containerInfo.PodName, containerInfo.ContainerName)
			}

			c := uint64(containerInfo.MemoryRequest) / uint64(memset.Size())
			for _, numaID := range memset.ToSliceInt() {
				memoryProvisions[numaID] += c
			}
		}
	}

	// 5. sub reserved memory and scale_factor
	reserved := p.essentials.ReservedForAllocate
	reservedAvg := uint64(int(reserved) / nums)

	watermarkScaleFactor, err := p.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		general.Errorf("Can not get system watermark scale factor: %v", err)
		return err
	}
	// calculate system factor  with double scale_factor to make kswapd less happened
	systemWatermarkReserved := availNUMATotal * 2 * watermarkScaleFactor.Value / 10000
	systemWatermarkReservedAvg := uint64(int(systemWatermarkReserved) / nums)

	for numaID := range memoryProvisions {
		memoryProvisions[numaID] = uint64(general.Clamp(float64(memoryProvisions[numaID]-reservedAvg-systemWatermarkReservedAvg), .0, float64(memoryTotals[numaID])))
	}

	// set other numa nodes to 0
	memoryProvisions = memoryProvisions.FillNUMANodesWithZero(allNUMAs)
	// store the memoryProvisions
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.memoryProvisions = memoryProvisions
	general.Infof("memoryProvisions: %+v", p.memoryProvisions)

	return nil
}

func (p *PolicyCanonical) GetProvision() machine.MemoryDetails {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	if p.memoryProvisions == nil {
		general.Warningf("memory provisioner is nil")
	}

	return p.memoryProvisions
}

func NewPolicyCanonical(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionPolicy {
	return &PolicyCanonical{
		PolicyBase: NewPolicyBase(conf, extraConfig, metaReader, metaServer, emitter),
	}
}
