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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type PolicyNUMAUsed struct {
	*PolicyBase

	memoryHeadroom     resource.Quantity
	numaMemoryHeadroom map[int]resource.Quantity

	conf *config.Configuration
}

func NewPolicyNUMAUsed(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter,
) HeadroomPolicy {
	p := PolicyNUMAUsed{
		PolicyBase:         NewPolicyBase(metaReader, metaServer),
		numaMemoryHeadroom: make(map[int]resource.Quantity),
		conf:               conf,
	}

	return &p
}

func (p *PolicyNUMAUsed) Name() types.MemoryHeadroomPolicyName {
	return types.MemoryHeadroomPolicyNUMAUsed
}

func (p *PolicyNUMAUsed) Update() (err error) {
	var totalMemoryHeadroom float64 = 0

	defer func() {
		p.UpdateStatus(err)
	}()

	allNUMAs := p.metaServer.CPUDetails.NUMANodes().ToSliceInt()
	numaHeadroom := make(map[int]float64, len(allNUMAs))

	for _, numaID := range allNUMAs {
		var numaTotal float64
		var data metric.MetricData
		data, err = p.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			general.Errorf("Can not get numa memory total, numaID: %v, %v", numaID, err)
			return err
		}
		numaTotal = data.Value

		var numaLimitSum float64 = 0
		var numaUsedEstimate float64 = 0

		f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
			if ci == nil {
				return true
			}

			if ci.QoSLevel != apiconsts.PodAnnotationQoSLevelSharedCores &&
				ci.QoSLevel != apiconsts.PodAnnotationQoSLevelDedicatedCores {
				return true
			}

			containerNUMAs := machine.GetCPUAssignmentNUMAs(ci.TopologyAwareAssignments)
			if containerNUMAs.Size() == 0 {
				return true
			}

			if !containerNUMAs.Contains(numaID) {
				return true
			}

			var containerNumaLimit float64
			if containerNUMAs.Size() > 0 {
				containerNumaLimit = ci.MemoryLimit / float64(containerNUMAs.Size())
			} else {
				containerNumaLimit = ci.MemoryLimit
			}
			numaLimitSum += containerNumaLimit

			containerNumaRequest := ci.MemoryRequest / float64(containerNUMAs.Size())
			numaUsedEstimate += containerNumaRequest

			return true
		}
		p.metaReader.RangeContainer(f)

		headroom := numaTotal - numaLimitSum + (numaLimitSum-numaUsedEstimate)*0.5
		headroom = math.Max(headroom, 0)

		numaHeadroom[numaID] = headroom
		totalMemoryHeadroom += headroom

		general.InfoS("NUMA memory headroom calculation",
			"numaID", numaID,
			"numaTotal", general.FormatMemoryQuantity(numaTotal),
			"numaLimitSum", general.FormatMemoryQuantity(numaLimitSum),
			"numaUsedEstimate", general.FormatMemoryQuantity(numaUsedEstimate),
			"headroom", general.FormatMemoryQuantity(headroom),
		)
	}

	numaHeadroomQuantity := make(map[int]resource.Quantity, len(allNUMAs))
	for _, numaID := range allNUMAs {
		if _, ok := numaHeadroom[numaID]; !ok {
			numaHeadroomQuantity[numaID] = *resource.NewQuantity(0, resource.BinarySI)
		} else {
			numaHeadroomQuantity[numaID] = *resource.NewQuantity(int64(numaHeadroom[numaID]), resource.BinarySI)
		}
	}

	p.numaMemoryHeadroom = numaHeadroomQuantity
	p.memoryHeadroom = *resource.NewQuantity(int64(totalMemoryHeadroom), resource.BinarySI)

	general.InfoS("total memory headroom",
		"memoryHeadroom", general.FormatMemoryQuantity(totalMemoryHeadroom),
	)

	return nil
}

func (p *PolicyNUMAUsed) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	if !p.IsStatusSucceeded() {
		return resource.Quantity{}, nil, fmt.Errorf("last update failed")
	}

	return p.memoryHeadroom, p.numaMemoryHeadroom, nil
}
