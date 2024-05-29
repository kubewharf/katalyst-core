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

package provisionpolicy

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type PolicyBase struct {
	types.ResourceEssentials
	types.ControlEssentials

	regionName          string
	regionType          types.QoSRegionType
	ownerPoolName       string
	podSet              types.PodSet
	bindingNumas        machine.CPUSet
	controlKnobAdjusted types.ControlKnob

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewPolicyBase(regionName string, regionType types.QoSRegionType, ownerPoolName string,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) *PolicyBase {
	cp := &PolicyBase{
		regionName:    regionName,
		regionType:    regionType,
		ownerPoolName: ownerPoolName,
		metaReader:    metaReader,
		metaServer:    metaServer,
		emitter:       emitter,
	}
	return cp
}

func (p *PolicyBase) SetEssentials(resourceEssentials types.ResourceEssentials, controlEssentials types.ControlEssentials) {
	p.ResourceEssentials = resourceEssentials
	p.ControlEssentials = controlEssentials
}

func (p *PolicyBase) SetPodSet(podSet types.PodSet) {
	p.podSet = podSet.Clone()
}

func (p *PolicyBase) SetBindingNumas(numas machine.CPUSet) {
	p.bindingNumas = numas
}

func (p *PolicyBase) GetControlKnobAdjusted() (types.ControlKnob, error) {
	switch p.regionType {
	case types.QoSRegionTypeShare, types.QoSRegionTypeDedicatedNumaExclusive:
		return p.controlKnobAdjusted.Clone(), nil

	case types.QoSRegionTypeIsolation:
		return map[types.ControlKnobName]types.ControlKnobValue{
			types.ControlKnobNonReclaimedCPUSizeUpper: {
				Value:  p.ResourceUpperBound,
				Action: types.ControlKnobActionNone,
			},
			types.ControlKnobNonReclaimedCPUSizeLower: {
				Value:  p.ResourceLowerBound,
				Action: types.ControlKnobActionNone,
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported region type %v", p.regionType)
	}
}
