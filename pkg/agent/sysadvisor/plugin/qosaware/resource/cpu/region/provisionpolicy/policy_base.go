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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
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
	regionType          configapi.QoSRegionType
	ownerPoolName       string
	podSet              types.PodSet
	bindingNumas        machine.CPUSet
	isNUMABinding       bool
	controlKnobAdjusted types.ControlKnob

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

func NewPolicyBase(regionName string, regionType configapi.QoSRegionType, ownerPoolName string,
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

func (p *PolicyBase) SetBindingNumas(numas machine.CPUSet, isNUMABinding bool) {
	p.bindingNumas = numas
	p.isNUMABinding = isNUMABinding
}

func (p *PolicyBase) GetControlKnobAdjusted() (types.ControlKnob, error) {
	switch p.regionType {
	case configapi.QoSRegionTypeShare, configapi.QoSRegionTypeDedicatedNumaExclusive:
		return p.controlKnobAdjusted.Clone(), nil

	case configapi.QoSRegionTypeIsolation:
		return map[configapi.ControlKnobName]types.ControlKnobItem{
			configapi.ControlKnobNonIsolatedUpperCPUSize: {
				Value:  p.ResourceUpperBound,
				Action: types.ControlKnobActionNone,
			},
			configapi.ControlKnobNonIsolatedLowerCPUSize: {
				Value:  p.ResourceLowerBound,
				Action: types.ControlKnobActionNone,
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported region type %v", p.regionType)
	}
}

func (p *PolicyBase) GetMetaInfo() string {
	return fmt.Sprintf("[regionName: %s, regionType: %s, ownerPoolName: %s, NUMAs: %v]", p.regionName, p.regionType, p.ownerPoolName, p.bindingNumas.String())
}
