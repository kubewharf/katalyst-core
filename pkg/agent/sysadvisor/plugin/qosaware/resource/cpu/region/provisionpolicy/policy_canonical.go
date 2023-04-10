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
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/regulator"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
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

func (p *PolicyCanonical) Update() error {
	var (
		cpuEstimation float64 = 0
		containerCnt  float64 = 0
	)

	for podUID, containerSet := range p.PodSet {
		for containerName := range containerSet {
			ci, ok := p.MetaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu-provision] illegal container info of %v/%v", podUID, containerName)
				continue
			}

			var err error
			containerEstimation := 0.0
			if p.EnableReclaim {
				containerEstimation, err = helper.EstimateContainerResourceUsage(ci, v1.ResourceCPU, p.MetaReader)
				if err != nil {
					return err
				}
				// FIXME: metric server doesn't support to report cpu usage in numa granularity,
				//  so we split cpu usage evenly across the binding numas of container.
				if p.BindingNumas.Size() > 0 {
					cpuSize := 0
					for _, numaID := range p.BindingNumas.ToSliceInt() {
						cpuSize += ci.TopologyAwareAssignments[numaID].Size()
					}
					containerEstimation = containerEstimation * float64(cpuSize) / float64(machine.CountCPUAssignmentCPUs(ci.TopologyAwareAssignments))
				}
			} else {
				containerEstimation = ci.CPURequest
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}

	// we need to call SetLatestCPURequirement to ensure the previous requirements are passed to
	// regulator in case that sysadvisor restarts, to avoid the slow-start always begin with zero.
	p.CPURegulator.SetLatestCPURequirement(p.Requirement)
	p.CPURegulator.Regulate(cpuEstimation)

	klog.Infof("[qosaware-cpu-provision] cpu requirement estimation: %.2f, #container %v", cpuEstimation, containerCnt)
	p.Requirement = p.CPURegulator.GetCPURequirement()
	return nil
}

func (p *PolicyCanonical) GetControlKnobAdjusted() (types.ControlKnob, error) {
	return map[types.ControlKnobName]types.ControlKnobValue{
		types.ControlKnobNonReclaimedCPUSetSize: {
			Value:  float64(p.Requirement),
			Action: types.ControlKnobActionNone,
		},
	}, nil
}
