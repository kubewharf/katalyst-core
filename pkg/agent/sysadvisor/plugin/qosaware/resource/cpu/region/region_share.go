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
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type QoSRegionShare struct {
	*QoSRegionBase
}

// NewQoSRegionShare returns a region instance for shared pool
func NewQoSRegionShare(name string, ownerPoolName string, conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	r := &QoSRegionShare{
		QoSRegionBase: NewQoSRegionBase(name, ownerPoolName, types.QoSRegionTypeShare, conf, extraConf, metaReader, metaServer, emitter),
	}
	return r
}

func (r *QoSRegionShare) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	for policyName, internal := range r.provisionPolicyMap {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(types.ResourceEssentials{
			MinRequirement:      minShareCPURequirement,
			MaxRequirement:      r.Total - r.ReservePoolSize - minReclaimCPURequirement,
			ReservedForAllocate: r.ReservedForAllocate,
			EnableReclaim:       r.EnableReclaim,
		})

		// try set initial cpu requirement to restore calculator after metaCache has been initialized
		internal.initDoOnce.Do(func() {
			if poolSize, ok := r.metaReader.GetPoolSize(r.ownerPoolName); ok {
				internal.policy.SetRequirement(poolSize)
				klog.Infof("[qosaware-cpu] set initial cpu requirement %v", poolSize)
			}
		})

		// run an episode of policy and calculator update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", policyName, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionShare) TryUpdateHeadroom() {
	r.Lock()
	defer r.Unlock()

	for policyName, internal := range r.headroomPolicyMap {
		internal.updateStatus = types.PolicyUpdateFailed

		// set essentials for policy and regulator
		internal.policy.SetPodSet(r.podSet)
		internal.policy.SetEssentials(r.ResourceEssentials)

		// run an episode of policy and calculator update
		if err := internal.policy.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", policyName, err)
			continue
		}
		internal.updateStatus = types.PolicyUpdateSucceeded
	}
}

func (r *QoSRegionShare) GetProvision() (types.ControlKnob, error) {
	r.Lock()
	defer r.Unlock()

	policyName := r.selectProvisionPolicy(provisionPolicyPriority)
	internal, ok := r.provisionPolicyMap[policyName]
	if !ok {
		return nil, fmt.Errorf("no legal policy result")
	}

	controlKnobValue, err := internal.policy.GetControlKnobAdjusted()
	if err != nil {
		return nil, err
	}

	return controlKnobValue, nil
}

func (r *QoSRegionShare) GetHeadroom() (resource.Quantity, error) {
	r.Lock()
	defer r.Unlock()

	policyName := r.selectHeadroomPolicy(headroomPolicyPriority)
	internal, ok := r.headroomPolicyMap[policyName]
	if !ok {
		return *resource.NewQuantity(0, resource.DecimalSI), fmt.Errorf("no legal policy result")
	}

	headroom, err := internal.policy.GetHeadroom()
	if err != nil {
		return resource.Quantity{}, err
	}

	return *resource.NewQuantity(int64(headroom), resource.DecimalSI), nil
}
