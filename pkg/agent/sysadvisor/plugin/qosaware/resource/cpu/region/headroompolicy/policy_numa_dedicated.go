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
	"fmt"
	"math"

	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PolicyNUMADedicated struct {
	*PolicyBase
	headroom float64
}

// NOTE: NewPolicyNUMADedicated can only for dedicated_cores with numa exclusive region

func NewPolicyNUMADedicated(regionName string, regionType configapi.QoSRegionType, ownerPoolName string,
	_ *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) HeadroomPolicy {
	p := &PolicyNUMADedicated{
		PolicyBase: NewPolicyBase(regionName, regionType, ownerPoolName, metaReader, metaServer, emitter),
	}
	return p
}

func (p *PolicyNUMADedicated) getContainerInfos() (string, []*types.ContainerInfo, error) {
	if len(p.podSet) != 1 {
		return "", nil, fmt.Errorf("more than one pod are assgined to this policy")
	}
	cis := make([]*types.ContainerInfo, 0)
	for podUID, containers := range p.podSet {
		for _, container := range containers.List() {
			ci, ok := p.metaReader.GetContainerInfo(podUID, container)
			if !ok {
				return "", nil, fmt.Errorf("failed to find continaer(%s/%s)", podUID, container)
			}
			cis = append(cis, ci)
		}
		return podUID, cis, nil
	}
	return "", nil, fmt.Errorf("should never get here")
}

func (p *PolicyNUMADedicated) Update() error {
	cpuEstimation := 0.0
	containerCnt := 0

	podUID, containers, err := p.getContainerInfos()
	if err != nil {
		return err
	}
	enableReclaim, err := helper.PodEnableReclaim(context.Background(), p.metaServer, podUID, p.EnableReclaim)
	if err != nil {
		general.Errorf("check pod reclaim status failed: %v, %v", podUID, err)
		return err
	}
	if !enableReclaim {
		p.headroom = 0
		return nil
	}

	totalRequest := 0.0
	for _, ci := range containers {
		containerEstimation, err := helper.EstimateContainerCPUUsage(ci, p.metaReader, enableReclaim)
		if err != nil {
			general.Errorf("EstimateContainerCPUUsage failed: %v, %v", ci.PodName, err)
			return err
		}

		var zeroAssignment bool
		containerEstimation, zeroAssignment = helper.SplitCPUByBindingNUMAs(containerEstimation, p.bindingNumas, ci)
		if zeroAssignment {
			general.Warningf("[qosaware-cpu-numa-exclusive] cpuAssignmentCPUs is 0 for container %s/%s", ci.PodUID, ci.ContainerName)
			containerEstimation = 0
		}

		cpuEstimation += containerEstimation
		totalRequest += ci.CPURequest
		containerCnt += 1
	}
	cpuEstimation += p.ReservedForAllocate

	originHeadroom := math.Max(p.ResourceUpperBound-cpuEstimation+p.ReservedForReclaim, 0)
	score, err := helper.PodPerformanceScore(context.Background(), p.metaServer, podUID)
	if err != nil {
		general.Errorf("get pps failed: %v, %v", podUID, err)
		return err
	}
	p.headroom = scaleHeadroomByPerformance(originHeadroom, score)

	klog.Infof("[qosaware-cpu-numa-exclusive] region %v cpuEstimation %v with reservedForAllocate %v reservedForReclaim %v"+
		" originHeadroom %v headroom %v score %v #container %v", p.regionName, cpuEstimation, p.ReservedForAllocate,
		p.ReservedForReclaim, originHeadroom, p.headroom, score, containerCnt)

	return nil
}

func (p *PolicyNUMADedicated) GetHeadroom() (float64, error) {
	return p.headroom, nil
}

// scaleHeadroomByPerformance scales the raw headroom by the performance score and
// clamps the result to be non-negative. The linear factor extrapolates below
// spd.MinPerformanceScore when the score falls out of the valid range; without
// the clamp that extrapolation would yield a negative headroom, which is
// meaningless (headroom cannot supply negative spare CPUs).
func scaleHeadroomByPerformance(originHeadroom, score float64) float64 {
	factor := (score - spd.MinPerformanceScore) / (spd.MaxPerformanceScore - spd.MinPerformanceScore)
	return math.Max(originHeadroom*factor, 0)
}
