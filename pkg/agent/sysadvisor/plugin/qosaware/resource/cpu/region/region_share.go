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
	"math"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provisionpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	minShareCPURequirement   int           = 4
	minReclaimCPURequirement int           = 4
	maxRampUpStep            float64       = 10
	maxRampDownStep          float64       = 2
	minRampDownPeriod        time.Duration = 30 * time.Second
)

// Policy priority defines the priority of available provision or headroom policies.
// Result of the policy with higher priority will be preferred when it is legal.
// Larger value indicates higher priority.
var (
	provisionPolicyPriority = map[types.CPUProvisionPolicyName]int{
		types.CPUProvisionPolicyCanonical: 0,
		types.CPUProvisionPolicyRama:      1,
	}
)

type provisionPolicyWrapper struct {
	policy       provisionpolicy.ProvisionPolicy
	updateStatus types.PolicyUpdateStatus
}

type QoSRegionShare struct {
	*QoSRegionBase
	isInitialized bool

	// provisionPolicyMap for comparing and merging different policy results
	provisionPolicyMap map[types.CPUProvisionPolicyName]*provisionPolicyWrapper

	// regulatorMap for regulating cpu requirement from provision results
	regulatorMap map[types.CPUProvisionPolicyName]*cpuRegulator
}

// NewQoSRegionShare returns a region instance for shared pool
func NewQoSRegionShare(name string, ownerPoolName string, conf *config.Configuration, extraConf interface{},
	metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) QoSRegion {

	r := &QoSRegionShare{
		QoSRegionBase: NewQoSRegionBase(name, ownerPoolName, types.QoSRegionTypeShare, metaCache, metaServer, emitter),
		isInitialized: false,

		provisionPolicyMap: make(map[types.CPUProvisionPolicyName]*provisionPolicyWrapper),
		regulatorMap:       make(map[types.CPUProvisionPolicyName]*cpuRegulator),
	}

	// Keep canonical policy by default as baseline
	provisionPolicyList := []types.CPUProvisionPolicyName{types.CPUProvisionPolicyCanonical}

	configuredProvisionPolicy, ok := conf.CPUAdvisorConfiguration.CPUProvisionPolicy[r.regionType]
	if ok && configuredProvisionPolicy != types.CPUProvisionPolicyCanonical {
		provisionPolicyList = append(provisionPolicyList, configuredProvisionPolicy)
	}

	// Try new policies
	initializers := provisionpolicy.GetRegisteredInitializers()
	for _, policyName := range provisionPolicyList {
		if initializer, ok := initializers[policyName]; ok {
			r.provisionPolicyMap[policyName] = &provisionPolicyWrapper{
				initializer(conf, extraConf, metaCache, metaServer, emitter),
				types.PolicyUpdateFailed,
			}
			r.regulatorMap[policyName] = newCPURegulator(maxRampUpStep, maxRampDownStep, minRampDownPeriod)
		}
	}

	return r
}

func (r *QoSRegionShare) AddContainer(ci *types.ContainerInfo) error {
	r.Lock()
	defer r.Unlock()

	if ci == nil {
		return fmt.Errorf("container info nil")
	}

	r.podSet.Insert(ci.PodUID, ci.ContainerName)

	if len(r.containerTopologyAwareAssignment) <= 0 {
		r.containerTopologyAwareAssignment = ci.TopologyAwareAssignments.Clone()
	} else {
		// Sanity check: all containers in the region share the same cpuset
		// Do not return error when sanity check fails to prevent unnecessary stall
		if !r.containerTopologyAwareAssignment.Equals(ci.TopologyAwareAssignments) {
			klog.Warningf("[qosaware-cpu] sanity check failed")
		}
	}

	return nil
}

func (r *QoSRegionShare) TryUpdateProvision() {
	r.Lock()
	defer r.Unlock()

	for policyName, wrapper := range r.provisionPolicyMap {
		wrapper.updateStatus = types.PolicyUpdateFailed
		p := wrapper.policy
		regulator := r.regulatorMap[policyName]

		// Set essentials for policy and regulator
		p.SetPodSet(r.podSet)
		regulator.setEssentials(minShareCPURequirement, r.total-r.reservePoolSize-minReclaimCPURequirement, r.total-r.reservePoolSize, r.reservedForAllocate)

		// Try set initial cpu requirement to restore calculator after restart
		if !r.isInitialized {
			if poolSize, ok := r.metaCache.GetPoolSize(r.ownerPoolName); ok {
				regulator.setLatestCPURequirement(poolSize)
				klog.Infof("[qosaware-cpu] set initial cpu requirement %v", poolSize)
			}
			r.isInitialized = true
		}

		// Run an episode of policy and calculator update
		if err := p.Update(); err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", policyName, err)
		}
		wrapper.updateStatus = types.PolicyUpdateSucceeded

		controlKnob, err := p.GetControlKnobAdjusted()
		if err != nil {
			klog.Errorf("[qosaware-cpu] update policy %v failed: %v", policyName, err)
		}
		cpuRequirementRaw := controlKnob[types.ControlKnobSharedCPUSetSize].Value
		regulator.regulate(cpuRequirementRaw)
	}
}

func (r *QoSRegionShare) TryUpdateHeadroom() {
	r.Lock()
	defer r.Unlock()
}

func (r *QoSRegionShare) GetProvision() (types.ControlKnob, error) {
	r.Lock()
	defer r.Unlock()

	policyName := r.selectProvisionPolicy()
	regulator, ok := r.regulatorMap[policyName]
	if !ok {
		return nil, fmt.Errorf("no legal policy result")
	}

	return types.ControlKnob{
		types.ControlKnobSharedCPUSetSize: types.ControlKnobValue{
			Value:  float64(regulator.getCPURequirement()),
			Action: types.ControlKnobActionNone,
		},
	}, nil
}

func (r *QoSRegionShare) GetHeadroom() (resource.Quantity, error) {
	r.Lock()
	defer r.Unlock()

	policyName := r.selectProvisionPolicy()
	regulator, ok := r.regulatorMap[policyName]
	if !ok {
		return *resource.NewQuantity(0, resource.DecimalSI), fmt.Errorf("no legal policy result")
	}

	return *resource.NewQuantity(int64(regulator.getCPURequirementReclaimed()), resource.DecimalSI), nil
}

func (r *QoSRegionShare) selectProvisionPolicy() types.CPUProvisionPolicyName {
	selected := types.CPUProvisionPolicyNone
	max := math.MinInt

	for policyName, wrapper := range r.provisionPolicyMap {
		if wrapper.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}
		if priority, ok := provisionPolicyPriority[policyName]; ok && priority > max {
			selected = policyName
			max = priority
		}
	}
	return selected
}
