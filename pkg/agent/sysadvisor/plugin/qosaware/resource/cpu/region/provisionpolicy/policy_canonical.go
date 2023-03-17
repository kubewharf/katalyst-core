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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type PolicyCanonical struct {
	*PolicyBase
	cpuRequirement float64
}

func NewPolicyCanonical(name types.CPUProvisionPolicyName, metaCache *metacache.MetaCache) ProvisionPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(name, metaCache),
	}
	return p
}

func (p *PolicyCanonical) Update() error {
	var (
		cpuEstimation float64 = 0
		containerCnt  float64 = 0
	)

	for podUID, v := range p.containerSet {
		for containerName := range v {
			ci, ok := p.metaCache.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu-canonical] illegal container info of %v/%v", podUID, containerName)
				continue
			}

			containerEstimation, err := p.estimateContainer(ci)
			if err != nil {
				return err
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}
	klog.Infof("[qosaware-cpu-canonical] cpu requirement estimation: %.2f, #container %v", cpuEstimation, containerCnt)

	p.cpuRequirement = cpuEstimation

	return nil
}

func (p *PolicyCanonical) GetControlKnobAdjusted() types.ControlKnob {
	return types.ControlKnob{
		types.ControlKnobGuranteedCPUSetSize: {
			Value:  p.cpuRequirement,
			Action: types.ControlKnobActionNone,
		},
	}
}

func (p *PolicyCanonical) estimateContainer(ci *types.ContainerInfo) (float64, error) {
	return helper.EstimateContainerResourceUsage(ci, v1.ResourceCPU, p.metaCache)
}
