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
	"math"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type QoSRegionShare struct {
	*QoSRegionBase

	configTranslator general.SuffixTranslator
}

// NewQoSRegionShare returns a region instance for shared pool
func NewQoSRegionShare(ci *types.ContainerInfo, conf *config.Configuration, extraConf interface{}, numaID int,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
	pinnedCPUSetInfo *PinnedCPUSetInfo,
) QoSRegion {
	regionName := getRegionNameFromMetaCache(ci, numaID, metaReader)
	if regionName == "" {
		regionName = string(configapi.QoSRegionTypeShare) + types.RegionNameSeparator + string(uuid.NewUUID())
	}

	// Why OriginOwnerPoolName ?
	// Case 1. create a share pool with OwnerPoolName:
	//	When receive a new pod with new share pool from QRM, advisor should create a new share region with OwnerPoolName (OriginOwnerPoolName == OwnerPoolName).
	// Case 2. create a share pool with OriginOwnerPoolName:
	//	When put isolation pods back to share pool, advisor should create a new share region with OriginOwnerPoolName (OriginOwnerPoolName != OwnerPoolName).
	isNumaBinding := numaID != commonstate.FakedNUMAID
	r := &QoSRegionShare{
		QoSRegionBase:    NewQoSRegionBase(regionName, ci.OriginOwnerPoolName, configapi.QoSRegionTypeShare, conf, extraConf, isNumaBinding, false, metaReader, metaServer, emitter, pinnedCPUSetInfo),
		configTranslator: commonstate.OwnerPoolNameTranslator,
	}

	if isNumaBinding {
		r.bindingNumas = machine.NewCPUSet(numaID)
	}
	r.indicatorCurrentGetters = map[string]types.IndicatorCurrentGetter{
		string(v1alpha1.ServiceSystemIndicatorNameCPUSchedWait):             r.getPoolCPUSchedWait,
		string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio):            r.getPoolCPUUsageRatio,
		string(v1alpha1.ServiceSystemIndicatorNameMemoryAccessReadLatency):  r.getMemoryAccessReadLatency,
		string(v1alpha1.ServiceSystemIndicatorNameMemoryAccessWriteLatency): r.getMemoryAccessWriteLatency,
		string(v1alpha1.ServiceSystemIndicatorNameMemoryL3MissLatency):      r.getMemoryL3MissLatency,
	}
	return r
}

func (r *QoSRegionShare) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	// update each provision policy
	r.updateProvisionPolicy()

	// get raw provision control knob
	rawControlKnobs := r.getProvisionControlKnob()

	// restrict control knobs by reference policy
	restrictedControlKnobs := r.restrictProvisionControlKnob(rawControlKnobs)

	// regulate control knobs
	r.regulateProvisionControlKnob(restrictedControlKnobs, r.getEffectiveControlKnobs())
}

func (r *QoSRegionShare) updateProvisionPolicy() {
	r.ControlEssentials = types.ControlEssentials{
		ControlKnobs:   r.getEffectiveControlKnobs(),
		ReclaimOverlap: r.AllowSharedCoresOverlapReclaimedCores,
	}

	indicators, err := r.getIndicators()
	if err != nil {
		klog.Warningf("[qosaware-cpu] failed to get indicators, ignore it: %v", err)
	} else {
		r.ControlEssentials.Indicators = indicators
		general.Infof("indicators %v for region %v", indicators, r.name)
	}

	// update internal policy
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

func (r *QoSRegionShare) getEffectiveControlKnobs() types.ControlKnob {
	quota, _, err := r.getEffectiveReclaimResource()
	if err != nil {
		klog.Errorf("[qosaware-cpu] failed to get effective reclaim resource, ignore it: %v", err)
	} else if r.isNumaBinding {
		if quota > 0 {
			return types.ControlKnob{
				configapi.ControlKnobReclaimedCoresCPUQuota: {
					Value:  quota,
					Action: types.ControlKnobActionNone,
				},
			}
		}
	}

	regionInfo, ok := r.metaReader.GetRegionInfo(r.name)
	if ok {
		if _, existed := regionInfo.ControlKnobMap[configapi.ControlKnobNonReclaimedCPURequirement]; existed {
			return regionInfo.ControlKnobMap
		}
	}
	var requirement int
	if r.conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores {
		requirement = int(math.Ceil(r.getPodsRequest()))
	} else {
		requirement, ok = r.metaReader.GetPoolSize(r.ownerPoolName)
		if !ok {
			klog.Warningf("[qosaware-cpu] pool %v not exist", r.ownerPoolName)
			return nil
		}
	}
	if requirement <= 0 {
		klog.Errorf("[qosaware-cpu] pool %v of non positive requirement", r.ownerPoolName)
		return nil
	}

	return types.ControlKnob{
		configapi.ControlKnobNonReclaimedCPURequirement: {
			Value:  float64(requirement),
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionShare) getPoolCPUSchedWait() (float64, error) {
	poolInfo, ok := r.metaReader.GetPoolInfo(r.ownerPoolName)
	if !ok {
		klog.Warningf("[qosaware-cpu] pool %v not exist", r.ownerPoolName)
		return 0, nil
	}

	cpuSet := poolInfo.TopologyAwareAssignments.MergeCPUSet()
	schedWait := r.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUSchedwait, metric.AggregatorAvg)
	return schedWait.Value, nil
}

func (r *QoSRegionShare) getPoolCPUUsageRatio() (float64, error) {
	poolInfo, ok := r.metaReader.GetPoolInfo(r.ownerPoolName)
	if !ok {
		klog.Warningf("[qosaware-cpu] pool %v not exist", r.ownerPoolName)
		return 0, nil
	}

	cpuSet := poolInfo.TopologyAwareAssignments.MergeCPUSet()
	usageRatio := r.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUUsageRatio, metric.AggregatorAvg)
	return usageRatio.Value, nil
}

func (r *QoSRegionShare) EnableReclaim() bool {
	return r.QoSRegionBase.EnableReclaim() &&
		!sets.NewString(r.conf.GetDynamicConfiguration().DisableReclaimSharePools...).Has(r.configTranslator.Translate(r.ownerPoolName))
}
