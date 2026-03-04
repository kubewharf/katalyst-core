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
	"context"
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	idleThreshold = 1
)

// todo:
// 1. support numa mem bandwidth as indicator
// 2. get service indicator target from spd

type QoSRegionDedicated struct {
	*QoSRegionBase
}

// NewQoSRegionDedicated returns a region instance for dedicated cores
// with numa binding and numa exclusive container
func NewQoSRegionDedicated(ci *types.ContainerInfo, conf *config.Configuration, numaID int,
	extraConf interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) QoSRegion {
	regionName := getRegionNameFromMetaCache(ci, numaID, metaReader)
	if regionName == "" {
		regionName = string(configapi.QoSRegionTypeDedicated) + types.RegionNameSeparator + string(uuid.NewUUID())
	}

	isNumaBinding := numaID != commonstate.FakedNUMAID
	r := &QoSRegionDedicated{
		QoSRegionBase: NewQoSRegionBase(regionName, ci.OwnerPoolName, configapi.QoSRegionTypeDedicated, conf, extraConf,
			isNumaBinding, ci.IsDedicatedNumaExclusive(), metaReader, metaServer, emitter),
	}

	if isNumaBinding {
		r.bindingNumas = machine.NewCPUSet(numaID)
	} else {
		r.bindingNumas = machine.NewCPUSet()
		for numaID := range ci.TopologyAwareAssignments {
			r.bindingNumas.Add(numaID)
		}
	}

	r.indicatorCurrentGetters = map[string]types.IndicatorCurrentGetter{
		string(workloadapis.ServiceSystemIndicatorNameCPI):           r.getPodCPICurrent,
		string(workloadapis.ServiceSystemIndicatorNameCPUUsageRatio): r.getCPUUsageRatio,
	}
	r.enableReclaim = r.EnableReclaim

	return r
}

func (r *QoSRegionDedicated) getPodUID() (string, error) {
	if len(r.podSet) != 1 {
		return "", fmt.Errorf("more than one pod are assgined to this policy")
	}
	for podUID := range r.podSet {
		return podUID, nil
	}
	return "", fmt.Errorf("should never get here")
}

func (r *QoSRegionDedicated) EnableReclaim() bool {
	podUID, err := r.getPodUID()
	if err != nil {
		general.ErrorS(err, "getPodUID failed")
		return false
	}

	enableReclaim, err := helper.PodEnableReclaim(context.Background(), r.metaServer, podUID, r.ResourceEssentials.EnableReclaim)
	if err != nil {
		general.ErrorS(err, "failed to check PodEnableReclaim", "name", r.name)
		return false
	}
	return enableReclaim
}

func (r *QoSRegionDedicated) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	r.updateIdleStatus()

	// update each provision policy
	r.updateProvisionPolicy()

	// get raw provision control knob
	rawControlKnobs := r.getProvisionControlKnob()

	// restrict control knobs by reference policy
	restrictedControlKnobs := r.restrictProvisionControlKnob(rawControlKnobs)

	// regulate control knobs
	r.regulateProvisionControlKnob(restrictedControlKnobs, r.getEffectiveControlKnobs())
}

func (r *QoSRegionDedicated) updateProvisionPolicy() {
	r.ControlEssentials = types.ControlEssentials{
		ControlKnobs:   r.getEffectiveControlKnobs(),
		ReclaimOverlap: true,
	}

	indicators, err := r.getIndicators()
	if err != nil {
		klog.Errorf("[qosaware-cpu] get indicators failed: %v", err)
	} else {
		r.ControlEssentials.Indicators = indicators
	}

	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetBindingNumas(r.bindingNumas, r.isNumaBinding)
		internal.policy.SetEssentials(r.ResourceEssentials, r.ControlEssentials)

		// run an episode of policy update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionDedicated) updateIdleStatus() {
	idle := true
	for podUID, containers := range r.podSet {
		for containerName := range containers {
			v, err := r.metaReader.GetContainerMetric(podUID, containerName, consts.MetricCPUUsageContainer)
			if err != nil {
				general.ErrorS(err, "failed to get metric", "podUID", podUID, "containerName", containerName, "metricName", consts.MetricCPUUsageContainer)
				continue
			}
			if v.Value >= idleThreshold {
				idle = false
				goto out
			}
			v, err = r.metaReader.GetContainerMetric(podUID, containerName, consts.MetricLoad1MinContainer)
			if err != nil {
				general.ErrorS(err, "failed to get metric", "podUID", podUID, "containerName", containerName, "metricName", consts.MetricLoad1MinContainer)
				continue
			}
			if v.Value >= idleThreshold {
				idle = false
				goto out
			}
		}
	}
	if idle {
		general.InfoS("region is idle", "regionName", r.name)
	}
out:
	r.idle.Store(idle)
}

func (r *QoSRegionDedicated) getEffectiveControlKnobs() types.ControlKnob {
	quota, _, err := r.getEffectiveReclaimResource()
	if err != nil {
		klog.Errorf("[qosaware-cpu] failed to get effective reclaim resource, ignore it: %v", err)
	} else if quota > 0 {
		return types.ControlKnob{
			configapi.ControlKnobReclaimedCoresCPUQuota: {
				Value:  quota,
				Action: types.ControlKnobActionNone,
			},
		}
	}

	var cpuRequirement float64
	if r.isNumaExclusive {
		reclaimedCPUSize := 0
		if reclaimedInfo, ok := r.metaReader.GetPoolInfo(commonstate.PoolNameReclaim); ok {
			for _, numaID := range r.bindingNumas.ToSliceInt() {
				reclaimedCPUSize += reclaimedInfo.TopologyAwareAssignments[numaID].Size()
			}
		}

		cpuRequirement = r.ResourceUpperBound + r.ReservedForReclaim - float64(reclaimedCPUSize)
	} else {
		existCPURequirementFound := false
		r.metaReader.RangeRegionInfo(func(regionName string, regionInfo *types.RegionInfo) bool {
			if regionInfo.RegionType != r.regionType {
				return true
			}

			if !apiequality.Semantic.DeepEqual(regionInfo.Pods, r.podSet) ||
				!r.bindingNumas.Equals(regionInfo.BindingNumas) {
				return true
			}

			if _, existed := regionInfo.ControlKnobMap[configapi.ControlKnobNonReclaimedCPURequirement]; existed {
				cpuRequirement = regionInfo.ControlKnobMap[configapi.ControlKnobNonReclaimedCPURequirement].Value
				existCPURequirementFound = true
			}
			return false
		})

		if !existCPURequirementFound {
			cpuRequirement = r.getPodsRequest()
		}
	}

	return types.ControlKnob{
		configapi.ControlKnobNonReclaimedCPURequirement: {
			Value:  cpuRequirement,
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionDedicated) getPodCPICurrent() (float64, error) {
	var (
		cpiSum       float64 = 0
		containerCnt float64 = 0
	)

	for podUID, containerSet := range r.podSet {
		for containerName := range containerSet {
			ci, ok := r.metaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu] illegal container info of %v/%v", podUID, containerName)
				return 0, nil
			}
			if ci.ContainerType == v1alpha1.ContainerType_MAIN {
				cpi, err := r.metaReader.GetContainerMetric(podUID, containerName, consts.MetricCPUCPIContainer)
				if err != nil {
					klog.Errorf("[qosaware-cpu] get %v of %v/%v failed: %v", consts.MetricCPUCPIContainer, podUID, containerName, err)
					return 0, nil
				}
				cpiSum += cpi.Value
				containerCnt += 1
			}
		}
	}

	return cpiSum / containerCnt, nil
}

func (r *QoSRegionDedicated) getCPUUsageRatio() (float64, error) {
	cpuSet := machine.NewCPUSet()
	for podUID, containerSet := range r.podSet {
		for containerName := range containerSet {
			ci, ok := r.metaReader.GetContainerInfo(podUID, containerName)
			if !ok || ci == nil {
				klog.Errorf("[qosaware-cpu] illegal container info of %v/%v", podUID, containerName)
				return 0, nil
			}

			for numaID := range ci.TopologyAwareAssignments {
				if r.bindingNumas.Contains(numaID) {
					cpuSet = cpuSet.Union(ci.TopologyAwareAssignments[numaID])
				}
			}
		}
	}

	usageRatio := r.metaServer.AggregateCoreMetric(cpuSet, consts.MetricCPUUsageRatio, metric.AggregatorAvg)
	return usageRatio.Value, nil
}
