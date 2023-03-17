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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type QoSRegionDedicatedNuma struct {
	*QoSRegionBase
}

// NewQoSRegionDedicatedNuma returns a share qos region instance
func NewQoSRegionDedicatedNuma(name string, ownerPoolName string,
	provisionPolicyName types.CPUProvisionPolicyName, headroomPolicy headroompolicy.HeadroomPolicy, numaID int,
	metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) QoSRegion {
	r := &QoSRegionDedicatedNuma{
		QoSRegionBase: NewQoSRegionBase(name, ownerPoolName, types.QoSRegionTypeDedicatedNuma, types.QosRegionPriorityDedicated, provisionPolicyName, headroomPolicy, sets.NewInt(numaID), metaCache, emitter),
	}
	return r
}

func (r *QoSRegionDedicatedNuma) TryUpdateControlKnob() error {
	return nil
}

func (r *QoSRegionDedicatedNuma) GetControlKnobUpdated() (types.ControlKnob, error) {
	return nil, nil
}

func (r *QoSRegionDedicatedNuma) GetHeadroom() (resource.Quantity, error) {
	return r.headroomPolicy.GetHeadroom()
}

func (r *QoSRegionDedicatedNuma) TryUpdateHeadroom() error {
	r.headroomPolicy.SetContainerSet(r.containerSet)
	r.headroomPolicy.SetCPULimit(int64(r.cpuLimit - r.reservePoolSize))
	r.headroomPolicy.SetCPUReserved(int64(r.reservedForAllocate))

	return r.headroomPolicy.Update()
}
