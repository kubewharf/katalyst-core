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

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
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
		QoSRegionBase: NewQoSRegionBase(regionName, ci.OwnerPoolName, types.QoSRegionTypeShare, conf, extraConf, metaReader, metaServer, emitter),
	}

	r.indicatorCurrentGetters = map[string]types.IndicatorCurrentGetter{
		string(v1alpha1.TargetIndicatorNameCPUSchedWait): r.getPodSchedWaitCurrent,
	}
	return r
}

func (r *QoSRegionShare) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	r.ControlEssentials = types.ControlEssentials{
		ControlKnobs:   r.getControlKnobs(),
		ReclaimOverlap: false,
	}

	indicators, err := r.getIndicators(r.indicatorCurrentGetters)
	if err != nil {
		general.Errorf("get indicators failed: %v", err)
	} else {
		r.ControlEssentials.Indicators = indicators
	}

	r.updateStatus()

	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials, r.ControlEssentials)

		// run an episode of policy update
		if err := internal.policy.Update(); err != nil {
			general.Errorf("update policy %v failed: %v", internal.name, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionShare) getControlKnobs() types.ControlKnob {
	poolSize, ok := r.metaReader.GetPoolSize(r.ownerPoolName)
	if !ok {
		general.Errorf("pool %v not exist", r.ownerPoolName)
		return nil
	}
	if poolSize <= 0 {
		general.Errorf("pool %v of non positive size", r.ownerPoolName)
		return nil
	}

	return types.ControlKnob{
		types.ControlKnobNonReclaimedCPUSize: {
			Value:  float64(poolSize),
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionShare) getPodSchedWaitCurrent() (float64, error) {
	poolInfo, ok := r.metaReader.GetPoolInfo(r.ownerPoolName)
	if !ok {
		general.Errorf("pool %v not exist", r.ownerPoolName)
		return 0, nil
	}

	cpuSet := poolInfo.TopologyAwareAssignments.MergeCPUSet()
	shedWait := r.metaServer.AggregateCoreMetric(cpuSet, pkgconsts.MetricCPUSchedwait, metric.AggregatorAvg)
	return shedWait.Value, nil
}
