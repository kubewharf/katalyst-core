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
	"context"
	"math"

	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
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
	headroom float64
}

func NewPolicyCanonical(regionName string, regionType types.QoSRegionType, ownerPoolName string,
	_ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) HeadroomPolicy {
	p := &PolicyCanonical{
		PolicyBase: NewPolicyBase(regionName, regionType, ownerPoolName, metaReader, metaServer, emitter),
	}
	return p
}

func (p *PolicyCanonical) Update() error {
	cpuEstimation := 0.0
	containerCnt := 0

	for podUID, containerSet := range p.podSet {
		enableReclaim, err := helper.PodEnableReclaim(context.Background(), p.metaServer, podUID, p.EnableReclaim)
		if err != nil {
			return err
		}

		for containerName := range containerSet {
			ci, ok := p.metaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu-headroom] illegal container info of %v/%v", podUID, containerName)
				continue
			}
			var containerEstimation float64 = 0
			if ci.IsNumaBinding() && !enableReclaim {
				if ci.ContainerType == v1alpha1.ContainerType_MAIN {
					bindingNumas := machine.GetCPUAssignmentNUMAs(ci.TopologyAwareAssignments)
					for range bindingNumas.ToSliceInt() {
						containerEstimation += float64(p.metaServer.CPUsPerNuma())
					}
					general.Infof("container %s/%s occupied cpu %v", ci.PodName, ci.ContainerName, containerEstimation)
				} else {
					containerEstimation = 0
				}
			} else {
				containerEstimation, err = helper.EstimateContainerCPUUsage(ci, p.metaReader, enableReclaim)
				if err != nil {
					return err
				}
			}

			// FIXME: metric server doesn't support to report cpu usage in numa granularity,
			// so we split cpu usage evenly across the binding numas of container.
			if p.bindingNumas.Size() > 0 {
				cpuSize := 0
				for _, numaID := range p.bindingNumas.ToSliceInt() {
					cpuSize += ci.TopologyAwareAssignments[numaID].Size()
				}
				containerEstimation = containerEstimation * float64(cpuSize) / float64(machine.CountCPUAssignmentCPUs(ci.TopologyAwareAssignments))
			}

			cpuEstimation += containerEstimation
			containerCnt += 1
		}
	}
	cpuEstimation += p.ReservedForAllocate

	p.headroom = math.Max(p.ResourceUpperBound-cpuEstimation+p.ReservedForReclaim, 0)

	klog.Infof("[qosaware-cpu-canonical] region %v cpuEstimation %v with reservedForAllocate %v reservedForReclaim %v headroom %v #container %v",
		p.regionName, cpuEstimation, p.ReservedForAllocate, p.ReservedForReclaim, p.headroom, containerCnt)

	return nil
}

func (p *PolicyCanonical) GetHeadroom() (float64, error) {
	return p.headroom, nil
}
