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

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	metricCPUProvisionControlKnobRestricted = "cpu_provision_control_knob_restricted"

	metricTagKeyRefPolicyName    = "ref_policy_name"
	metricTagKeyRestrictedReason = "restricted_reason"
)

type QoSRegionShare struct {
	*QoSRegionBase
}

// NewQoSRegionShare returns a region instance for shared pool
func NewQoSRegionShare(ci *types.ContainerInfo, conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	regionName := getRegionNameFromMetaCache(ci, cpuadvisor.FakedNUMAID, metaReader)
	if regionName == "" {
		regionName = string(types.QoSRegionTypeShare) + types.RegionNameSeparator + string(uuid.NewUUID())
	}

	r := &QoSRegionShare{
		QoSRegionBase: NewQoSRegionBase(regionName, ci.OriginOwnerPoolName, types.QoSRegionTypeShare, conf, extraConf, metaReader, metaServer, emitter),
	}

	r.indicatorCurrentGetters = map[string]types.IndicatorCurrentGetter{
		string(v1alpha1.ServiceSystemIndicatorNameCPUSchedWait):  r.getPoolCPUSchedWait,
		string(v1alpha1.ServiceSystemIndicatorNameCPUUsageRatio): r.getPoolCPUUsageRatio,
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
	r.regulateProvisionControlKnob(restrictedControlKnobs, &r.ControlKnobs)
}

func (r *QoSRegionShare) updateProvisionPolicy() {
	r.ControlEssentials = types.ControlEssentials{
		ControlKnobs:   r.getControlKnobs(),
		ReclaimOverlap: false,
	}

	indicators, err := r.getIndicators()
	if err != nil {
		klog.Errorf("[qosaware-cpu] get indicators failed: %v", err)
	} else {
		r.ControlEssentials.Indicators = indicators
	}

	// update internal policy
	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials, r.ControlEssentials)

		// run an episode of policy update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

// restrictProvisionControlKnob is to restrict provision control knob by reference policy
func (r *QoSRegionShare) restrictProvisionControlKnob(originControlKnob map[types.CPUProvisionPolicyName]types.ControlKnob) map[types.CPUProvisionPolicyName]types.ControlKnob {
	restrictedControlKnob := make(map[types.CPUProvisionPolicyName]types.ControlKnob)
	for policyName, controlKnob := range originControlKnob {
		restrictedControlKnob[policyName] = controlKnob.Clone()
		refPolicyName, ok := r.conf.RestrictRefPolicy[policyName]
		if !ok {
			continue
		}

		refControlKnob, ok := originControlKnob[refPolicyName]
		if !ok {
			klog.Errorf("get control knob from reference policy %v for policy %v failed", refPolicyName, policyName)
			continue
		}

		for controlKnobName, rawKnobValue := range controlKnob {
			refKnobValue, ok := refControlKnob[controlKnobName]
			if !ok {
				continue
			}

			min, max := refKnobValue.Value, refKnobValue.Value
			if maxGap, ok := r.conf.RestrictControlKnobMaxGap[controlKnobName]; ok {
				min = math.Min(min, refKnobValue.Value-maxGap)
				max = math.Max(max, refKnobValue.Value+maxGap)
			}

			if maxGapRatio, ok := r.conf.RestrictControlKnobMaxGapRatio[controlKnobName]; ok {
				min = math.Min(min, refKnobValue.Value*(1-maxGapRatio))
				max = math.Max(max, refKnobValue.Value*(1+maxGapRatio))
			}

			restrictedKnobValue := rawKnobValue

			reason := "none"
			if rawKnobValue.Value > max {
				restrictedKnobValue.Value = max
				reason = "above"
			} else if rawKnobValue.Value < min {
				restrictedKnobValue.Value = min
				reason = "below"
			}

			if restrictedKnobValue != rawKnobValue {
				klog.Infof("[qosaware-cpu] restrict control knob %v for policy %v by policy %v from %.2f to %.2f, reason: %v",
					controlKnobName, policyName, refPolicyName, rawKnobValue.Value, restrictedKnobValue.Value, reason)
			}

			restrictedControlKnob[policyName][controlKnobName] = restrictedKnobValue
			_ = r.emitter.StoreInt64(metricCPUProvisionControlKnobRestricted, int64(r.conf.QoSAwarePluginConfiguration.SyncPeriod.Seconds()), metrics.MetricTypeNameCount, []metrics.MetricTag{
				{Key: metricTagKeyPolicyName, Val: string(policyName)},
				{Key: metricTagKeyRefPolicyName, Val: string(refPolicyName)},
				{Key: metricTagKeyControlKnobName, Val: string(controlKnobName)},
				{Key: metricTagKeyRestrictedReason, Val: reason},
			}...)
		}
	}

	return restrictedControlKnob
}

func (r *QoSRegionShare) getControlKnobs() types.ControlKnob {
	poolSize, ok := r.metaReader.GetPoolSize(r.ownerPoolName)
	if !ok {
		klog.Errorf("[qosaware-cpu] pool %v not exist", r.ownerPoolName)
		return nil
	}
	if poolSize <= 0 {
		klog.Errorf("[qosaware-cpu] pool %v of non positive size", r.ownerPoolName)
		return nil
	}

	return types.ControlKnob{
		types.ControlKnobNonReclaimedCPUSize: {
			Value:  float64(poolSize),
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionShare) getPoolCPUSchedWait() (float64, error) {
	poolInfo, ok := r.metaReader.GetPoolInfo(r.ownerPoolName)
	if !ok {
		klog.Errorf("[qosaware-cpu] pool %v not exist", r.ownerPoolName)
		return 0, nil
	}

	cpuSet := poolInfo.TopologyAwareAssignments.MergeCPUSet()
	schedWait := r.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUSchedwait, metric.AggregatorAvg)
	return schedWait.Value, nil
}

func (r *QoSRegionShare) getPoolCPUUsageRatio() (float64, error) {
	poolInfo, ok := r.metaReader.GetPoolInfo(r.ownerPoolName)
	if !ok {
		klog.Errorf("[qosaware-cpu] pool %v not exist", r.ownerPoolName)
		return 0, nil
	}

	cpuSet := poolInfo.TopologyAwareAssignments.MergeCPUSet()
	usageRatio := r.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUUsageRatio, metric.AggregatorAvg)
	return usageRatio.Value, nil
}
