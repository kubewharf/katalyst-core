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

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	idleThreshold = 1
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
		QoSRegionBase: NewQoSRegionBase(regionName, ci.OwnerPoolName, types.QoSRegionTypeDedicatedNumaExclusive, conf, extraConf, true, metaReader, metaServer, emitter),
	}
	r.bindingNumas = machine.NewCPUSet(numaID)
	r.indicatorCurrentGetters = map[string]types.IndicatorCurrentGetter{
		string(workloadapis.ServiceSystemIndicatorNameCPI): r.getPodCPICurrent,
	}
	r.enableReclaim = r.EnableReclaim

	return r
}

func (r *QoSRegionDedicatedNumaExclusive) getPodUID() (string, error) {
	if len(r.podSet) != 1 {
		return "", fmt.Errorf("more than one pod are assgined to this policy")
	}
	for podUID := range r.podSet {
		return podUID, nil
	}
	return "", fmt.Errorf("should never get here")
}

func (r *QoSRegionDedicatedNumaExclusive) EnableReclaim() bool {
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

func (r *QoSRegionDedicatedNumaExclusive) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	r.updateIdleStatus()

	// update each provision policy
	r.updateProvisionPolicy()

	// get raw provision control knob
	rawControlKnobs := r.getProvisionControlKnob()

	// regulate control knobs
	r.regulateProvisionControlKnob(rawControlKnobs, &r.ControlKnobs)
}

func (r *QoSRegionDedicatedNumaExclusive) updateProvisionPolicy() {
	r.ControlEssentials = types.ControlEssentials{
		ControlKnobs:   r.getControlKnobs(),
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
		internal.policy.SetBindingNumas(r.bindingNumas)
		internal.policy.SetEssentials(r.ResourceEssentials, r.ControlEssentials)

		// run an episode of policy update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionDedicatedNumaExclusive) updateIdleStatus() {
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

func (r *QoSRegionDedicatedNumaExclusive) getPodCPICurrent() (float64, error) {
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
