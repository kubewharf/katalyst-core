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
	"time"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/policy"
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
var policyPriority = map[types.CPUAdvisorPolicyName]int{
	types.CPUAdvisorPolicyCanonical: 0,
	types.CPUAdvisorPolicyRama:      1,
}

type QoSRegionShare struct {
	*QoSRegionBase

	isInitialized bool

	// policy and calculator map for comparing and merging different policy results
	policyMap     map[types.CPUAdvisorPolicyName]policy.Policy  // map[policyName]policy
	calculatorMap map[types.CPUAdvisorPolicyName]*cpuCalculator // map[policyName]calculator
}

// NewQoSRegionShare returns a share qos region instance
func NewQoSRegionShare(name string, ownerPoolName string, regionType QoSRegionType, regionPolicy types.CPUAdvisorPolicyName,
	conf *config.Configuration, metaCache *metacache.MetaCache, emitter metrics.MetricEmitter) QoSRegion {
	r := &QoSRegionShare{
		QoSRegionBase: NewQoSRegionBase(name, ownerPoolName, regionType, regionPolicy, metaCache, emitter),
		isInitialized: false,
		policyMap:     make(map[types.CPUAdvisorPolicyName]policy.Policy),
		calculatorMap: make(map[types.CPUAdvisorPolicyName]*cpuCalculator),
	}

	// New canonical policy with calculator as default
	policyName := types.CPUAdvisorPolicyCanonical
	p, _ := policy.NewPolicy(policyName, metaCache)
	r.policyMap[policyName] = p
	r.calculatorMap[policyName] = newCPUCalculator(conf, r.metaCache, maxRampUpStep, maxRampDownStep, minRampDownPeriod)
	r.regionPolicy = policyName

	// New another policy with calculator based on config
	policyName = types.CPUAdvisorPolicyName(conf.CPUAdvisorConfiguration.CPUAdvisorPolicy)
	if policyName != types.CPUAdvisorPolicyCanonical {
		p, err := policy.NewPolicy(policyName, metaCache)
		if err != nil {
			klog.Errorf("[qosaware-cpu] new policy %v failed: %v", err)
		} else {
			r.policyMap[policyName] = p
			r.calculatorMap[policyName] = newCPUCalculator(conf, r.metaCache, maxRampUpStep, maxRampDownStep, minRampDownPeriod)
			r.regionPolicy = policyName
		}
	}

	return r
}

func (r *QoSRegionShare) TryUpdateControlKnob() {
	for policyName, p := range r.policyMap {
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
		p.Update()
		cpuRequirementRaw := p.GetProvisionResult().(float64)
		c.update(cpuRequirementRaw)
	}
}

func (r *QoSRegionShare) GetControlKnobUpdated() types.ControlKnob {
	policyName := r.selectPolicy()
	c := r.calculatorMap[policyName]

	return map[types.ControlKnobName]types.ControlKnobValue{
		types.ControlKnobCPUSetSize: {
			Value:  float64(c.getCPURequirement()),
			Action: types.ControlKnobActionNone,
		},
	}
}

func (r *QoSRegionShare) GetHeadroom() int {
	policyName := r.selectPolicy()
	c := r.calculatorMap[policyName]

	return c.getCPURequirementReclaimed()
}

func (r *QoSRegionShare) selectPolicy() types.CPUAdvisorPolicyName {
	selected := types.CPUAdvisorPolicyNone
	max := math.MinInt

	for policyName := range r.policyMap {
		if priority, ok := policyPriority[policyName]; ok && priority > max {
			selected = policyName
			max = priority
		}
	}
	return selected
}
