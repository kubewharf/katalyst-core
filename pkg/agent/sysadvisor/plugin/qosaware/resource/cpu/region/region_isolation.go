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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const (
	isolationRegionDefaultOwnerPoolName = "isolation-default"
	isolationRegionNUMAOwnerPoolName    = "isolation-numa"
)

type QoSRegionIsolation struct {
	*QoSRegionBase
}

// NewQoSRegionIsolation returns a region instance for isolated pods
func NewQoSRegionIsolation(ci *types.ContainerInfo, customRegionName string, conf *config.Configuration, extraConf interface{}, numaID int,
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) QoSRegion {
	regionName := customRegionName
	if regionName == "" {
		regionName = getRegionNameFromMetaCache(ci, numaID, metaReader)
		if regionName == "" {
			regionName = string(configapi.QoSRegionTypeIsolation) + types.RegionNameSeparator + ci.PodName + types.RegionNameSeparator + string(uuid.NewUUID())
		}
	}

	isNumaBinding := numaID != commonstate.FakedNUMAID
	ownerPoolName := isolationRegionDefaultOwnerPoolName
	if isNumaBinding {
		ownerPoolName = isolationRegionNUMAOwnerPoolName
	}

	_, pkgName := resourcepackage.UnwrapOwnerPoolName(ci.OriginOwnerPoolName)
	r := &QoSRegionIsolation{
		QoSRegionBase: NewQoSRegionBase(resourcepackage.WrapOwnerPoolName(regionName, pkgName), ownerPoolName, pkgName, configapi.QoSRegionTypeIsolation, conf, extraConf, isNumaBinding, false, metaReader, metaServer, emitter),
	}
	if isNumaBinding {
		r.bindingNumas = machine.NewCPUSet(numaID)
	}
	return r
}

func (r *QoSRegionIsolation) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	// update each provision policy
	r.updateProvisionPolicy()

	// get raw provision control knob
	rawControlKnobs := r.getProvisionControlKnob()

	// regulate control knobs without last control knob
	r.regulateProvisionControlKnob(rawControlKnobs, nil)
}

// updateProvisionPolicy intentionally does NOT run an episode of policy.Update()
// for isolation regions: isolation sizes are derived directly from the resource
// bounds (see PolicyBase.GetControlKnobAdjusted for the Isolation region type),
// so there is no per-policy algorithm state to advance.
//
// Serialization contract (P4): the isolation region's provision state
// (provisionPolicies, provisionPolicyResults and their regulators) is only safe
// under the single-period serial execution imposed by the advisor loop, which
// calls TryUpdateProvision() and then GetProvision() within one cycle. Both
// methods already take the region mutex, so concurrent entry is excluded; we do
// NOT add finer-grained locking on top. The regulators here are dummy
// (NonIsolatedUpper/LowerCPUSize are not ramped), so there is no cross-cycle
// ramp state machine to protect beyond the region mutex.
func (r *QoSRegionIsolation) updateProvisionPolicy() {
	// Deliberately skip policy.Update(): isolation regions do not run the
	// regulator/ramp state machine (see the contract above). We only refresh the
	// pod set and essentials so the policy's next GetControlKnob call reads the
	// current inputs; marking updateStatus succeeded keeps the region healthy.
	for _, internal := range r.provisionPolicies {
		internal.updateStatus = types.PolicyUpdateSucceeded

		// set essentials for policy
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials, types.ControlEssentials{})
	}
}
