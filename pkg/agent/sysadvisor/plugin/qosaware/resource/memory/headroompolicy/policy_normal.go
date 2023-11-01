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
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"math"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"k8s.io/apimachinery/pkg/api/resource"
)

type PolicyNormal struct {
	*PolicyBase

	// memoryHeadroom is valid to be used iff updateStatus successes
	memoryHeadroom float64
	updateStatus   types.PolicyUpdateStatus

	conf *config.Configuration
}

// NewPolicyNormal return normal policy that presumes all containers are reclaimable
func NewPolicyNormal(conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) HeadroomPolicy {
	p := PolicyNormal{
		PolicyBase:   NewPolicyBase(metaReader, metaServer),
		updateStatus: types.PolicyUpdateFailed,
		conf:         conf,
	}

	return &p
}

func (p *PolicyNormal) Name() types.MemoryHeadroomPolicyName {
	return types.MemoryHeadroomPolicyNormal
}

func (p *PolicyNormal) sumReclaimedCoresContainersMemoryRequest() float64 {
	var memoryReclaimedCoresRequest float64

	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		if ci == nil || ci.QoSLevel != apiconsts.PodAnnotationQoSLevelReclaimedCores {
			return true
		}
		memoryReclaimedCoresRequest += ci.MemoryRequest
		return true
	}
	p.metaReader.RangeContainer(f)
	general.InfoS("sum of memory requests for reclaimed-cores containers", general.FormatMemoryQuantity(memoryReclaimedCoresRequest))
	return memoryReclaimedCoresRequest
}

func (p *PolicyNormal) Update() (err error) {
	defer func() {
		if err != nil {
			p.updateStatus = types.PolicyUpdateFailed
		} else {
			p.updateStatus = types.PolicyUpdateSucceeded
		}
	}()

	memoryMaxAllocatable := p.essentials.ResourceUpperBound - p.essentials.ReservedForAllocate
	metricMemFree, err := p.metaServer.GetNodeMetric(consts.MetricMemFreeSystem)
	if err != nil {
		return err
	}
	memoryFree := metricMemFree.Value

	memoryRequestForReclaimedCores := p.sumReclaimedCoresContainersMemoryRequest()
	p.memoryHeadroom = math.Max(memoryFree+memoryRequestForReclaimedCores-p.essentials.ReservedForAllocate, 0)
	p.memoryHeadroom = math.Min(p.memoryHeadroom, memoryMaxAllocatable)

	general.InfoS("memory details",
		"memory free", general.FormatMemoryQuantity(memoryFree),
		"sum of memory request for reclaimed-cores container", general.FormatMemoryQuantity(memoryRequestForReclaimedCores),
		"final memory headroom", general.FormatMemoryQuantity(p.memoryHeadroom),
		"max memory allocatable", general.FormatMemoryQuantity(memoryRequestForReclaimedCores),
	)

	return nil
}

func (p *PolicyNormal) GetHeadroom() (resource.Quantity, error) {
	if p.updateStatus != types.PolicyUpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("last update failed")
	}

	return *resource.NewQuantity(int64(p.memoryHeadroom), resource.BinarySI), nil
}
