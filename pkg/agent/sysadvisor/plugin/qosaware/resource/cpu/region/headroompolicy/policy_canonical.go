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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type PolicyCanonical struct {
	*PolicyBase
}

func NewPolicyCanonical(regionName string, _ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(regionName, metaReader, metaServer, emitter),
	}

	return p
}

func (p *PolicyCanonical) Update() error {
	regionInfo, ok := p.MetaReader.GetRegionInfo(p.RegionName)
	if !ok {
		return fmt.Errorf("get region info for %v failed", p.RegionName)
	}

	controlKnobValue := types.ControlKnobValue{}
	switch regionInfo.RegionType {
	case types.QoSRegionTypeShare:
		controlKnobValue, ok = regionInfo.ControlKnobMap[types.ControlKnobNonReclaimedCPUSetSize]
		if !ok {
			return fmt.Errorf("get control knob value failed")
		}
		cpuRequirement := controlKnobValue.Value
		p.HeadroomValue = math.Max(float64(p.Total-p.ReservePoolSize)-cpuRequirement, 0)
	case types.QoSRegionTypeDedicatedNumaExclusive:
		controlKnobValue, ok = regionInfo.ControlKnobMap[types.ControlKnobReclaimedCPUSupplied]
		if !ok {
			return fmt.Errorf("get control knob value failed")
		}
		p.HeadroomValue = math.Max(controlKnobValue.Value, 0)
	default:
		return fmt.Errorf("region type %v is invalid", regionInfo.RegionType)
	}

	return nil
}

func (p *PolicyCanonical) GetHeadroom() (float64, error) {
	return p.HeadroomValue, nil
}
