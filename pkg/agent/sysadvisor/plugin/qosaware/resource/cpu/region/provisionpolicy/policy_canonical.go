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
	"context"
	"fmt"
	"math"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/regulator"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type PolicyCanonical struct {
	*PolicyBase
}

func NewPolicyCanonical(regionName string, _ *config.Configuration, _ interface{}, regulator *regulator.CPURegulator,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) ProvisionPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(regionName, regulator, metaReader, metaServer, emitter),
	}
	return p
}

func (p *PolicyCanonical) estimateCPUUsage() (float64, error) {
	cpuEstimation := 0.0
	containerCnt := 0

	for podUID, containerSet := range p.podSet {
		enableReclaim, err := helper.PodEnableReclaim(context.Background(), p.metaServer, podUID, p.EnableReclaim)
		if err != nil {
			return 0, err
		}

		for containerName := range containerSet {
			ci, ok := p.metaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu-provision] illegal container info of %v/%v", podUID, containerName)
				continue
			}
			// when ramping up, estimation of cpu should be set as cpu request
			containerEstimation, err := helper.EstimateContainerCPUUsage(ci, p.metaReader, enableReclaim && !ci.RampUp)
			if err != nil {
				return 0, err
			}

			// FIXME: metric server doesn't support to report cpu usage in numa granularity,
			// so we split cpu usage evenly across the binding numas of container.
			if p.bindingNumas.Size() > 0 {
				cpuSize := 0
				for _, numaID := range p.bindingNumas.ToSliceInt() {
					cpuSize += ci.TopologyAwareAssignments[numaID].Size()
				}
				general.Infof("[qosaware-cpu-provision] container %v cpuSize %v CountCPUAssignmentCPUs %v containerEstimation %v", ci.ContainerName, cpuSize, machine.CountCPUAssignmentCPUs(ci.TopologyAwareAssignments), containerEstimation)
				containerEstimation = containerEstimation * float64(cpuSize) / float64(machine.CountCPUAssignmentCPUs(ci.TopologyAwareAssignments))
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}

	klog.Infof("[qosaware-cpu] #container %v", containerCnt)

	return cpuEstimation, nil
}

func (p *PolicyCanonical) Update() error {
	cpuEstimation, err := p.estimateCPUUsage()
	if err != nil {
		return err
	}

	// we need to call SetLatestCPURequirement to ensure the previous requirements are passed to
	// regulator in case that sysadvisor restarts, to avoid the slow-start always begin with zero.
	p.regulator.SetLatestCPURequirement(p.cpuRequirement)
	p.regulator.Regulate(cpuEstimation)
	p.cpuRequirement = p.regulator.GetCPURequirement()

	return nil
}

func (p *PolicyCanonical) GetControlKnobAdjusted() (types.ControlKnob, error) {
	regionInfo, ok := p.metaReader.GetRegionInfo(p.regionName)
	if !ok || regionInfo == nil {
		return nil, fmt.Errorf("illegal region info")
	}

	switch regionInfo.RegionType {
	case types.QoSRegionTypeShare:
		return map[types.ControlKnobName]types.ControlKnobValue{
			types.ControlKnobNonReclaimedCPUSetSize: {
				Value:  float64(p.cpuRequirement),
				Action: types.ControlKnobActionNone,
			},
		}, nil

	case types.QoSRegionTypeDedicatedNumaExclusive:
		// Do conversion because canonical policy calculates resource estimation according to
		// non reclaim workloads and is ignorant of reclaimed cpu supplied
		return types.ControlKnob{
			types.ControlKnobReclaimedCPUSupplied: types.ControlKnobValue{
				Value:  math.Max(p.ResourceUpperBound-float64(p.cpuRequirement), 0),
				Action: types.ControlKnobActionNone,
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported region type %v", regionInfo.RegionType)
	}
}
