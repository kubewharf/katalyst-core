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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const isolationRegionDefaultOwnerPoolName = "isolation-default"

type QoSRegionIsolation struct {
	*QoSRegionBase
}

// NewQoSRegionIsolation returns a region instance for isolated pods
func NewQoSRegionIsolation(ci *types.ContainerInfo, customRegionName string, conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	regionName := customRegionName
	if regionName == "" {
		regionName = getRegionNameFromMetaCache(ci, cpuadvisor.FakedNUMAID, metaReader)
		if regionName == "" {
			regionName = string(types.QoSRegionTypeIsolation) + types.RegionNameSeparator + ci.PodName + types.RegionNameSeparator + string(uuid.NewUUID())
		}
	}

	r := &QoSRegionIsolation{
		QoSRegionBase: NewQoSRegionBase(regionName, isolationRegionDefaultOwnerPoolName, types.QoSRegionTypeIsolation, conf, extraConf, metaReader, metaServer, emitter),
	}
	return r
}

func (r *QoSRegionIsolation) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	// no need to update provision for isolation region
	// todo should we run regulator processes for isolation region?
	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateSucceeded

		// set essentials for policy
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials, types.ControlEssentials{})
	}
}
