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

// todo:
// 1. implement headroom policy related

package region

import (
	"fmt"
	"math"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provisionpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	minShareCPURequirement   int           = 4
	minReclaimCPURequirement int           = 4
	maxRampUpStep            float64       = 10
	maxRampDownStep          float64       = 2
	minRampDownPeriod        time.Duration = 30 * time.Second
)

// policyPriority defines the priority of results offered by several policies.
// Larger value indicates higher priority.
var policyPriority = map[types.CPUProvisionPolicyName]int{
	types.CPUProvisionPolicyCanonical: 0,
	types.CPUProvisionPolicyRama:      1,
}

type provisionPolicyWrapper struct {
	policy  provisionpolicy.ProvisionPolicy
	isLegal bool
}

type QoSRegionShare struct {
	*QoSRegionBase

	isInitialized bool

	// policy and calculator maps for comparing and merging different policy results
	provisionPolicyMap map[types.CPUProvisionPolicyName]*provisionPolicyWrapper
	calculatorMap      map[types.CPUProvisionPolicyName]*CPUCalculator
}

// NewQoSRegionShare returns a share qos region instance
func NewQoSRegionShare(name string, ownerPoolName string, regionType types.QoSRegionType,
	provisionPolicyName types.CPUProvisionPolicyName, headroomPolicy headroompolicy.HeadroomPolicy, numaLimit int,
	conf *config.Configuration, metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) QoSRegion {

	numaIDs := sets.NewInt()
	for numaID := 0; numaID < numaLimit; numaID++ {
		numaIDs.Insert(numaID)
	}
	r := &QoSRegionShare{
		QoSRegionBase:      NewQoSRegionBase(name, ownerPoolName, regionType, types.QosRegionPriorityShare, provisionPolicyName, headroomPolicy, numaIDs, metaCache, emitter),
		isInitialized:      false,
		provisionPolicyMap: make(map[types.CPUProvisionPolicyName]*provisionPolicyWrapper),
		calculatorMap:      make(map[types.CPUProvisionPolicyName]*CPUCalculator),
	}

	initializers := provisionpolicy.GetRegisteredInitializers()

	// canonical policy with calculator as default
	policyName := types.CPUProvisionPolicyCanonical
	initializer := initializers[policyName]
	r.provisionPolicyMap[policyName] = &provisionPolicyWrapper{
		initializer(policyName, metaCache),
		false,
	}
	r.calculatorMap[policyName] = NewCPUCalculator(maxRampUpStep, maxRampDownStep, minRampDownPeriod)
	r.provisionPolicy = policyName

	// Try new another policy with calculator according to config
	policyName = types.CPUProvisionPolicyName(conf.CPUAdvisorConfiguration.CPUProvisionPolicy)
	if initializer, ok := initializers[policyName]; ok {
		r.provisionPolicyMap[policyName] = &provisionPolicyWrapper{
			initializer(policyName, metaCache),
			false,
		}
		r.calculatorMap[policyName] = NewCPUCalculator(maxRampUpStep, maxRampDownStep, minRampDownPeriod)
		r.provisionPolicy = policyName
	}

	return r
}

func (r *QoSRegionShare) TryUpdateControlKnob() error {
	for policyName, wrapper := range r.provisionPolicyMap {
		wrapper.isLegal = false
		p := wrapper.policy
		c := r.calculatorMap[policyName]

		// Set essentials for policy
		p.SetContainerSet(r.containerSet)

		// Set essentials for calculator
		c.SetupCPURequirement(minShareCPURequirement, r.cpuLimit-r.reservePoolSize-minReclaimCPURequirement, r.cpuLimit-r.reservePoolSize, r.reservedForAllocate)

		// Try set initial cpu requirement to restore calculator after restart
		if !r.isInitialized {
			if poolSize, ok := r.metaCache.GetPoolSize(r.ownerPoolName); ok {
				c.SetLastestCPURequirement(poolSize)
				klog.Infof("[qosaware-cpu] set initial cpu requirement %v", poolSize)
			}
			r.isInitialized = true
		}

		// Run an episode of policy and calculator update
		if err := p.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] policy %v update failed: %v", policyName, err)
			return fmt.Errorf("[qosaware-cpu] policy %v update failed: %v", policyName, err)
		}
		wrapper.isLegal = true
		cpuRequirementRaw := p.GetControlKnobAdjusted()[types.ControlKnobGuranteedCPUSetSize].Value
		c.RegulateRequirement(cpuRequirementRaw)
	}
	return nil
}

func (r *QoSRegionShare) GetControlKnobUpdated() (types.ControlKnob, error) {
	policyName := r.selectProvisionPolicy()
	c, ok := r.calculatorMap[policyName]
	if !ok {
		return nil, fmt.Errorf("no legal policy results")
	}

	return types.ControlKnob{
		types.ControlKnobGuranteedCPUSetSize: types.ControlKnobValue{
			Value:  float64(c.GetCPURequirement()),
			Action: types.ControlKnobActionNone,
		},
		types.ControlKnobReclaimedCPUSetSize: types.ControlKnobValue{
			Value:  float64(c.GetCPURequirementReclaimed()),
			Action: types.ControlKnobActionNone,
		},
	}, nil
}

func (r *QoSRegionShare) GetHeadroom() (resource.Quantity, error) {
	policyName := r.selectProvisionPolicy()
	c, ok := r.calculatorMap[policyName]
	if !ok {
		return *resource.NewQuantity(0, resource.DecimalSI), fmt.Errorf("illegal policy results")
	}

	return *resource.NewQuantity(int64(c.GetCPURequirementReclaimed()), resource.DecimalSI), nil
}

func (r *QoSRegionShare) selectProvisionPolicy() types.CPUProvisionPolicyName {
	selected := types.CPUProvisionPolicyNone
	max := math.MinInt

	for policyName, wrapper := range r.provisionPolicyMap {
		if !wrapper.isLegal {
			continue
		}
		if priority, ok := policyPriority[policyName]; ok && priority > max {
			selected = policyName
			max = priority
		}
	}
	return selected
}

func (r *QoSRegionShare) TryUpdateHeadroom() error {
	return r.headroomPolicy.Update()
}
