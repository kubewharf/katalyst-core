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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// todo: support rama policy, with cpu schedwait avg as indicator

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
	return r
}

func (r *QoSRegionShare) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateFailed

		controlEssentials := types.ControlEssentials{
			ControlKnobs: r.getControlKnobs(),
			Indicators:   nil,
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
