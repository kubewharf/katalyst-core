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

// todo: implement provision and headroom policy for this region

package region

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type QoSRegionDedicatedNumaExclusive struct {
	*QoSRegionBase
}

// NewQoSRegionDedicatedNumaExclusive returns a region instance for dedicated cores
// with numa binding and numa exclusive container
func NewQoSRegionDedicatedNumaExclusive(ci *types.ContainerInfo, conf *config.Configuration, numaID int,
	extraConf interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	regionName := getRegionName(ci, numaID, metaReader)
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

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.buildProvisionEssentials(types.MinDedicatedCPURequirement))
		internal.policy.SetBindingNumas(r.bindingNumas)

		// try set initial cpu requirement to restore calculator after metaCache has been initialized
		internal.initDoOnce.Do(func() {
			reclaimedCpuSize := 0
			if reclaimedInfo, ok := r.metaReader.GetPoolInfo(state.PoolNameReclaim); ok {
				for _, numaID := range r.bindingNumas.ToSliceInt() {
					reclaimedCpuSize += reclaimedInfo.TopologyAwareAssignments[numaID].Size()
				}
			}
			cpuRequirement := r.Total - r.ReservePoolSize - reclaimedCpuSize
			internal.policy.SetRequirement(cpuRequirement)
			klog.Infof("[qosaware-cpu] set initial cpu requirement %v", cpuRequirement)
		})

		// run an episode of policy and calculator update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionDedicatedNumaExclusive) TryUpdateHeadroom() {
	r.Lock()
	defer r.Unlock()

	for _, internal := range r.headroomPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials)

		// run an episode of policy and calculator update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionDedicatedNumaExclusive) GetProvision() (types.ControlKnob, error) {
	r.Lock()
	defer r.Unlock()

	if !r.EnableReclaim {
		return types.ControlKnob{
			types.ControlKnobReclaimedCPUSupplied: types.ControlKnobValue{
				Value:  math.Ceil(float64(types.MinReclaimCPURequirement)/float64(r.metaServer.NumNUMANodes)) * float64(r.bindingNumas.Size()),
				Action: types.ControlKnobActionNone,
			},
		}, nil
	}

	for _, internal := range r.provisionPolicies {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}
		controlKnobValue, err := internal.policy.GetControlKnobAdjusted()
		if err != nil {
			klog.Errorf("GetControlKnobAdjusted by policy %v err %v", internal.name, err)
			continue
		}
		return types.ControlKnob{
			types.ControlKnobReclaimedCPUSupplied: types.ControlKnobValue{
				Value:  float64(r.Total-r.ReservePoolSize) - controlKnobValue[types.ControlKnobNonReclaimedCPUSetSize].Value,
				Action: types.ControlKnobActionNone,
			},
		}, nil
	}
	return types.ControlKnob{}, fmt.Errorf("failed to get valid provison")
}

func (r *QoSRegionDedicatedNumaExclusive) GetHeadroom() (resource.Quantity, error) {
	r.Lock()
	defer r.Unlock()

	for _, internal := range r.headroomPolicies {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}
		headroom, err := internal.policy.GetHeadroom()
		if err != nil {
			klog.Errorf("GetHeadroom by policy %v err %v", internal.name, err)
			continue
		}
		r.headroomPolicyInUse = internal
		return *resource.NewQuantity(int64(headroom), resource.DecimalSI), nil
	}

	return resource.Quantity{}, fmt.Errorf("failed to get valid headroom")
}
