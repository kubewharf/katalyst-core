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
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type PolicyCanonical struct {
	*PolicyBase

	cpuRequirement float64
}

func NewPolicyCanonical(_ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, _ metrics.MetricEmitter) ProvisionPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(metaReader, metaServer),
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
			ci, ok := p.metaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu-provision] illegal container info of %v/%v", podUID, containerName)
				continue
			}

			containerEstimation, err := helper.EstimateContainerResourceUsage(ci, v1.ResourceCPU, p.metaReader)
			if err != nil {
				return err
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}

	klog.Infof("[qosaware-cpu-provision] cpu requirement estimation: %.2f, #container %v", cpuEstimation, containerCnt)
	p.cpuRequirement = cpuEstimation
	return nil
}

func (p *PolicyCanonical) GetControlKnobAdjusted() (types.ControlKnob, error) {
	return types.ControlKnob{
		types.ControlKnobSharedCPUSetSize: {
			Value:  p.cpuRequirement,
			Action: types.ControlKnobActionNone,
		},
	}, nil
}
