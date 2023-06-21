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

package region

import (
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// todo:
// 1. support numa mem bandwidth as indicator
// 2. get service indicator target from spd

type QoSRegionDedicatedNumaExclusive struct {
	*QoSRegionBase
}

// NewQoSRegionDedicatedNumaExclusive returns a region instance for dedicated cores
// with numa binding and numa exclusive container
func NewQoSRegionDedicatedNumaExclusive(ci *types.ContainerInfo, conf *config.Configuration, numaID int,
	extraConf interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	regionName := getRegionNameFromMetaCache(ci, numaID, metaReader)
	if regionName == "" {
		regionName = string(types.QoSRegionTypeDedicatedNumaExclusive) + types.RegionNameSeparator + string(uuid.NewUUID())
	}

	r := &QoSRegionDedicatedNumaExclusive{
		QoSRegionBase: NewQoSRegionBase(regionName, ci.OwnerPoolName, types.QoSRegionTypeDedicatedNumaExclusive, conf, extraConf, metaReader, metaServer, emitter),
	}
	r.bindingNumas = machine.NewCPUSet(numaID)

	return r
}

func (r *QoSRegionDedicatedNumaExclusive) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		controlEssentials := types.ControlEssentials{
			ControlKnobs: r.getControlKnobs(),
			Indicators:   r.getIndicators(),
		}

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials, controlEssentials)

		// run an episode of policy update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionDedicatedNumaExclusive) getControlKnobs() types.ControlKnob {
	reclaimedCPUSize := 0
	if reclaimedInfo, ok := r.metaReader.GetPoolInfo(state.PoolNameReclaim); ok {
		for _, numaID := range r.bindingNumas.ToSliceInt() {
			reclaimedCPUSize += reclaimedInfo.TopologyAwareAssignments[numaID].Size()
		}
	}
	cpuRequirement := r.ResourceUpperBound + r.ReservedForReclaim - float64(reclaimedCPUSize)

	return types.ControlKnob{
		types.ControlKnobNonReclaimedCPUSize: {
			Value:  cpuRequirement,
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionDedicatedNumaExclusive) getIndicators() types.Indicator {
	var (
		cpiSum       float64 = 0
		containerCnt float64 = 0
	)

	// get cpi of the main container in this region
	for podUID, containerSet := range r.podSet {
		for containerName := range containerSet {
			ci, ok := r.metaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu] illegal container info of %v/%v", podUID, containerName)
				return nil
			}
			if ci.ContainerType == v1alpha1.ContainerType_MAIN {
				cpi, err := r.metaReader.GetContainerMetric(podUID, containerName, consts.MetricCPUCPIContainer)
				if err != nil {
					klog.Errorf("[qosaware-cpu] get %v of %v/%v failed: %v", consts.MetricCPUCPIContainer, podUID, containerName, err)
					return nil
				}
				cpiSum += cpi
				containerCnt += 1
			}
		}
	}

	return types.Indicator{
		consts.MetricCPUCPIContainer: {
			Current: cpiSum / containerCnt,
			Target:  1.4, // temporary
			Upper:   1.4,
			Lower:   1.4,
		},
	}
}
