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

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type PolicyNUMAAware struct {
	*PolicyBase

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom float64
	updateStatus   types.PolicyUpdateStatus

	conf *config.Configuration
}

func NewPolicyNUMAAware(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) HeadroomPolicy {
	p := PolicyNUMAAware{
		PolicyBase:   NewPolicyBase(metaReader, metaServer),
		updateStatus: types.PolicyUpdateFailed,
		conf:         conf,
	}

	return &p
}

func (p *PolicyNUMAAware) Name() types.MemoryHeadroomPolicyName {
	return types.MemoryHeadroomPolicyNUMAAware
}

func (p *PolicyNUMAAware) reclaimedContainersFilter(ci *types.ContainerInfo) bool {
	return ci != nil && ci.QoSLevel == apiconsts.PodAnnotationQoSLevelReclaimedCores
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
		reclaimableMemory float64
		data              metric.MetricData
	)
	dynamicConfig := p.conf.GetDynamicConfiguration()

	availNUMAs, reclaimedCoresContainers, err := helper.GetAvailableNUMAsAndReclaimedCores(p.conf, p.metaReader, p.metaServer)
	if err != nil {
		return err
	}

	availNUMATotal := float64(0)
	for _, numaID := range availNUMAs.ToSliceInt() {
		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			general.Errorf("Can not get numa memory free, numaID: %v", numaID)
			return err
		}
		reclaimableMemory += data.Value
		general.InfoS("reclaimable numa memory free", "numaID", numaID, "numaFree", general.FormatMemoryQuantity(data.Value))

		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemInactiveFileNuma)
		if err != nil {
			return err
		}
		reclaimableMemory += data.Value * dynamicConfig.CacheBasedRatio
		general.InfoS("reclaimable numa inactive file", "numaID", numaID, "numaInactiveFile", general.FormatMemoryQuantity(data.Value))

		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			general.Errorf("Can not get numa memory total, numaID: %v", numaID)
			return err
		}
		availNUMATotal += data.Value
		general.InfoS("reclaimable numa memory total", "numaID", numaID, "numaTotal", general.FormatMemoryQuantity(data.Value))
	}

	for _, container := range reclaimedCoresContainers {
		reclaimableMemory += container.MemoryRequest
	}

	watermarkScaleFactor, err := p.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		general.InfoS("Can not get system watermark scale factor")
		return err
	}
	// reserve memory for watermark_scale_factor to make kswapd less happened
	systemWatermarkReserved := availNUMATotal * watermarkScaleFactor.Value / 10000

	general.InfoS("total memory reclaimable",
		"reclaimableMemory", general.FormatMemoryQuantity(reclaimableMemory),
		"ReservedForAllocate", general.FormatMemoryQuantity(p.essentials.ReservedForAllocate),
		"ReservedForWatermark", general.FormatMemoryQuantity(systemWatermarkReserved),
		"ResourceUpperBound", general.FormatMemoryQuantity(p.essentials.ResourceUpperBound))
	p.memoryHeadroom = math.Max(reclaimableMemory-p.essentials.ReservedForAllocate-systemWatermarkReserved, 0)

	return nil
}

func (p *PolicyNUMAAware) GetHeadroom() (resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("last update failed")
	}

	return *resource.NewQuantity(int64(p.memoryHeadroom), resource.BinarySI), nil
}
