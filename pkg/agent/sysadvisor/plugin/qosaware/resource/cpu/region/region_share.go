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

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
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
	calculatorMap      map[types.CPUProvisionPolicyName]*cpuCalculator
}

// NewQoSRegionShare returns a share qos region instance
func NewQoSRegionShare(name string, ownerPoolName string, regionType QoSRegionType, regionPolicy types.CPUProvisionPolicyName,
	conf *config.Configuration, metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) QoSRegion {
	r := &QoSRegionShare{
		QoSRegionBase:      NewQoSRegionBase(name, ownerPoolName, regionType, regionPolicy, metaCache, emitter),
		isInitialized:      false,
		provisionPolicyMap: make(map[types.CPUProvisionPolicyName]*provisionPolicyWrapper),
		calculatorMap:      make(map[types.CPUProvisionPolicyName]*cpuCalculator),
	}

	initializers := provisionpolicy.GetRegisteredInitializers()

	// canonical policy with calculator as default
	policyName := types.CPUProvisionPolicyCanonical
	initializer := initializers[policyName]
	r.provisionPolicyMap[policyName] = &provisionPolicyWrapper{
		initializer(policyName, metaCache),
		false,
	}
	r.calculatorMap[policyName] = newCPUCalculator(conf, r.metaCache, maxRampUpStep, maxRampDownStep, minRampDownPeriod)
	r.regionPolicy = policyName

	// Try new another policy with calculator according to config
	policyName = types.CPUProvisionPolicyName(conf.CPUAdvisorConfiguration.CPUProvisionPolicy)
	if initializer, ok := initializers[policyName]; ok {
		r.provisionPolicyMap[policyName] = &provisionPolicyWrapper{
			initializer(policyName, metaCache),
			false,
		}
		r.calculatorMap[policyName] = newCPUCalculator(conf, r.metaCache, maxRampUpStep, maxRampDownStep, minRampDownPeriod)
		r.regionPolicy = policyName
	}

	return r
}

func (r *QoSRegionShare) TryUpdateControlKnob() {
	for policyName, wrapper := range r.provisionPolicyMap {
		wrapper.isLegal = false
		p := wrapper.policy
		c := r.calculatorMap[policyName]

		// Set essentials for policy
		p.SetContainerSet(r.containerSet)

		// Set essentials for calculator
		c.setMinCPURequirement(minShareCPURequirement)
		c.setMaxCPURequirement(r.cpuLimit - r.reservePoolSize - minReclaimCPURequirement)
		c.setTotalCPURequirement(r.cpuLimit - r.reservePoolSize)
		c.setReservedForAllocate(r.reservedForAllocate)

		// Try set initial cpu requirement to restore calculator after restart
		if !r.isInitialized {
			if poolSize, ok := r.metaCache.GetPoolSize(r.ownerPoolName); ok {
				c.setLastestCPURequirement(poolSize)
				klog.Infof("[qosaware-cpu] set initial cpu requirement %v", poolSize)
			}
			r.isInitialized = true
		}

		// Run an episode of policy and calculator update
		if err := p.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] policy %v update failed: %v", policyName, err)
			continue
		}
		wrapper.isLegal = true
		cpuRequirementRaw := p.GetControlKnobAdjusted()[types.ControlKnobCPUSetSize].Value
		c.update(cpuRequirementRaw)
	}
}

func (r *QoSRegionShare) GetControlKnobUpdated() (types.ControlKnob, error) {
	policyName := r.selectProvisionPolicy()
	c, ok := r.calculatorMap[policyName]
	if !ok {
		return nil, fmt.Errorf("no legal policy results")
	}

	return types.ControlKnob{
		types.ControlKnobCPUSetSize: {
			Value:  float64(c.getCPURequirement()),
			Action: types.ControlKnobActionNone,
		},
	}, nil
}

func (r *QoSRegionShare) GetHeadroom() (int, error) {
	policyName := r.selectProvisionPolicy()
	c, ok := r.calculatorMap[policyName]
	if !ok {
		return 0, fmt.Errorf("illegal policy results")
	}

	return c.getCPURequirementReclaimed(), nil
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
