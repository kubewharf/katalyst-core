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

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
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

func NewPolicyCanonical(regionName string, regionType types.QoSRegionType, ownerPoolName string,
	_ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(regionName, regionType, ownerPoolName, metaReader, metaServer, emitter),
	}
	return p
}

func (p *PolicyCanonical) Update() error {
	// sanity check
	if err := p.sanityCheck(); err != nil {
		return err
	}

	cpuEstimation, err := p.estimateCPUUsage()
	if err != nil {
		return err
	}

	p.controlKnobAdjusted = types.ControlKnob{
		types.ControlKnobNonReclaimedCPUSize: types.ControlKnobValue{
			Value:  cpuEstimation,
			Action: types.ControlKnobActionNone,
		},
	}
	return nil
}

func (p *PolicyCanonical) sanityCheck() error {
	var (
		isLegal bool
		errList []error
	)

	// 1. check control knob legality
	isLegal = true
	if p.ControlKnobs != nil {
		v, ok := p.ControlKnobs[types.ControlKnobNonReclaimedCPUSize]
		if !ok || v.Value <= 0 {
			isLegal = false
		}
	}
	if !isLegal {
		errList = append(errList, fmt.Errorf("illegal control knob %v", p.ControlKnobs))
	}

	return errors.NewAggregate(errList)
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
				klog.Errorf("[qosaware-cpu-canonical] illegal container info of %v/%v", podUID, containerName)
				continue
			}

			var containerEstimation float64 = 0
			if ci.IsDedicatedNumaBinding() && !enableReclaim {
				if ci.ContainerType == v1alpha1.ContainerType_MAIN {
					bindingNumas := machine.GetCPUAssignmentNUMAs(ci.TopologyAwareAssignments)
					for range bindingNumas.ToSliceInt() {
						containerEstimation += float64(p.metaServer.CPUsPerNuma())
					}
					klog.Infof("[qosaware-cpu-canonical] container %s/%s occupied cpu %v", ci.PodName, ci.ContainerName, containerEstimation)
				} else {
					containerEstimation = 0
				}
			} else {
				// when ramping up, estimation of cpu should be set as cpu request
				containerEstimation, err = helper.EstimateContainerCPUUsage(ci, p.metaReader, enableReclaim && !ci.RampUp)
				if err != nil {
					return 0, err
				}
			}

			// FIXME: metric server doesn't support to report cpu usage in numa granularity,
			// so we split cpu usage evenly across the binding numas of container.
			if p.bindingNumas.Size() > 0 {
				cpuSize := 0
				for _, numaID := range p.bindingNumas.ToSliceInt() {
					cpuSize += ci.TopologyAwareAssignments[numaID].Size()
				}
				cpuAssignmentCPUs := machine.CountCPUAssignmentCPUs(ci.TopologyAwareAssignments)
				if cpuAssignmentCPUs != 0 {
					containerEstimation = containerEstimation * float64(cpuSize) / float64(cpuAssignmentCPUs)
				} else {
					// handle the case that cpuAssignmentCPUs is 0
					klog.Warningf("[qosaware-cpu-canonical] cpuAssignmentCPUs is 0 for %v/%v", podUID, containerName)
					containerEstimation = 0
				}
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}
	klog.Infof("[qosaware-cpu-canonical] region %v cpu estimation %.2f #container %v", p.regionName, cpuEstimation, containerCnt)

	return cpuEstimation, nil
}
