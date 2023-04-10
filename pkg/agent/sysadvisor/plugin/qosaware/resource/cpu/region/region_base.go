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
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provisionpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/regulator"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	minShareCPURequirement   int     = 4
	minReclaimCPURequirement int     = 4
	maxRampUpStep            float64 = 10
	maxRampDownStep          float64 = 2
	minRampDownPeriod                = 30 * time.Second
)

// Policy priority defines the priority of available provision or headroom policies.
// Result of the policy with higher priority will be preferred when it is legal.
// Larger value indicates higher priority.
var (
	provisionPolicyPriority = map[types.CPUProvisionPolicyName]int{
		types.CPUProvisionPolicyCanonical: 0,
		types.CPUProvisionPolicyRama:      1,
	}
	headroomPolicyPriority = map[types.CPUHeadroomPolicyName]int{
		types.CPUHeadroomPolicyCanonical: 0,
	}
)

type internalPolicyState struct {
	updateStatus types.PolicyUpdateStatus
	initDoOnce   sync.Once
}

type internalProvisionPolicy struct {
	policy provisionpolicy.ProvisionPolicy
	internalPolicyState
}

type internalHeadroomPolicy struct {
	policy headroompolicy.HeadroomPolicy
	internalPolicyState
}

type QoSRegionBase struct {
	sync.Mutex

	name          string
	ownerPoolName string
	regionType    types.QoSRegionType

	// bindingNumas records numas assigned to this region
	bindingNumas machine.CPUSet

	// podSet records current pod and containers in region keyed by pod uid and container name
	podSet types.PodSet

	// containerTopologyAwareAssignment changes dynamically by adding container
	containerTopologyAwareAssignment types.TopologyAwareAssignment

	types.ResourceEssentials

	// provisionPolicyMap for comparing and merging different provision policy results
	provisionPolicyMap map[types.CPUProvisionPolicyName]*internalProvisionPolicy
	// headroomPolicyMap for comparing and merging different headroom policy results
	headroomPolicyMap map[types.CPUHeadroomPolicyName]*internalHeadroomPolicy

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewQoSRegionBase returns a base qos region instance with common region methods
func NewQoSRegionBase(name string, ownerPoolName string, regionType types.QoSRegionType, conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *QoSRegionBase {
	r := &QoSRegionBase{
		name:          name,
		ownerPoolName: ownerPoolName,
		regionType:    regionType,

		bindingNumas:                     machine.NewCPUSet(),
		podSet:                           make(types.PodSet),
		containerTopologyAwareAssignment: make(types.TopologyAwareAssignment),

		provisionPolicyMap: make(map[types.CPUProvisionPolicyName]*internalProvisionPolicy),
		headroomPolicyMap:  make(map[types.CPUHeadroomPolicyName]*internalHeadroomPolicy),

		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}

	r.initHeadroomPolicy(conf, extraConf, metaReader, metaServer, emitter)
	r.initProvisionPolicy(conf, extraConf, metaReader, metaServer, emitter)

	return r
}

func (r *QoSRegionBase) Name() string {
	return r.name
}

func (r *QoSRegionBase) Type() types.QoSRegionType {
	return r.regionType
}

func (r *QoSRegionBase) IsEmpty() bool {
	r.Lock()
	defer r.Unlock()

	return len(r.podSet) <= 0
}

func (r *QoSRegionBase) Clear() {
	r.Lock()
	defer r.Unlock()

	r.bindingNumas = machine.NewCPUSet()
	r.podSet = make(types.PodSet)
	r.containerTopologyAwareAssignment = make(types.TopologyAwareAssignment)
}

func (r *QoSRegionBase) GetBindingNumas() machine.CPUSet {
	r.Lock()
	defer r.Unlock()

	return r.bindingNumas.Clone()
}

func (r *QoSRegionBase) GetPods() types.PodSet {
	r.Lock()
	defer r.Unlock()

	return r.podSet.Clone()
}

func (r *QoSRegionBase) SetBindingNumas(numas machine.CPUSet) {
	r.Lock()
	defer r.Unlock()

	r.bindingNumas = numas
}

func (r *QoSRegionBase) SetEssentials(essentials types.ResourceEssentials) {
	r.Lock()
	defer r.Unlock()

	r.ResourceEssentials = essentials
}

func (r *QoSRegionBase) AddContainer(ci *types.ContainerInfo) error {
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

// initProvisionPolicy initializes provision by adding additional policies into default ones
func (r *QoSRegionBase) initProvisionPolicy(conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	// keep canonical policy by default as baseline
	provisionPolicyList := []types.CPUProvisionPolicyName{types.CPUProvisionPolicyCanonical}
	configuredProvisionPolicy, ok := conf.CPUAdvisorConfiguration.ProvisionAdditionalPolicy[r.regionType]
	if ok && configuredProvisionPolicy != types.CPUProvisionPolicyCanonical {
		provisionPolicyList = append(provisionPolicyList, configuredProvisionPolicy)
	}

	// try new policies
	// todo move to separate functions
	initializers := provisionpolicy.GetRegisteredInitializers()
	for _, policyName := range provisionPolicyList {
		if initializer, ok := initializers[policyName]; ok {
			cpuRegulator := regulator.NewCPURegulator(maxRampUpStep, maxRampDownStep, minRampDownPeriod)
			policy := initializer(r.name, conf, extraConf, cpuRegulator, metaReader, metaServer, emitter)

			policy.SetBindingNumas(r.bindingNumas)
			r.provisionPolicyMap[policyName] = &internalProvisionPolicy{
				policy: policy,
				internalPolicyState: internalPolicyState{
					updateStatus: types.PolicyUpdateFailed,
				},
			}
		}
	}
}

// selectProvisionPolicy returns policy with the highest priority
func (r *QoSRegionBase) selectProvisionPolicy(provisionPolicyPriority map[types.CPUProvisionPolicyName]int) types.CPUProvisionPolicyName {
	selected := types.CPUProvisionPolicyNone
	max := math.MinInt

	for policyName, internal := range r.provisionPolicyMap {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}
		if priority, ok := provisionPolicyPriority[policyName]; ok && priority > max {
			selected = policyName
			max = priority
		}
	}
	return selected
}

// initHeadroomPolicy initializes headroom by adding additional policies into default ones
func (r *QoSRegionBase) initHeadroomPolicy(conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	// keep canonical policy by default as baseline
	headroomPolicyList := []types.CPUHeadroomPolicyName{types.CPUHeadroomPolicyCanonical}
	configuredHeadroomPolicy, ok := conf.CPUAdvisorConfiguration.HeadroomAdditionalPolicy[r.regionType]
	if ok && configuredHeadroomPolicy != types.CPUHeadroomPolicyCanonical {
		headroomPolicyList = append(headroomPolicyList, configuredHeadroomPolicy)
	}

	// try new policies
	headroomInitializers := headroompolicy.GetRegisteredInitializers()
	for _, policyName := range headroomPolicyList {
		if initializer, ok := headroomInitializers[policyName]; ok {
			policy := initializer(r.name, conf, extraConf, metaReader, metaServer, emitter)

			r.headroomPolicyMap[policyName] = &internalHeadroomPolicy{
				policy: policy,
				internalPolicyState: internalPolicyState{
					updateStatus: types.PolicyUpdateFailed,
				},
			}
		}
	}
}

// selectHeadroomPolicy returns policy with the highest priority
func (r *QoSRegionBase) selectHeadroomPolicy(headroomPolicyPriority map[types.CPUHeadroomPolicyName]int) types.CPUHeadroomPolicyName {
	selected := types.CPUHeadroomPolicyNone
	max := math.MinInt

	for policyName, internal := range r.headroomPolicyMap {
		if internal.updateStatus != types.PolicyUpdateSucceeded {
			continue
		}
		if priority, ok := headroomPolicyPriority[policyName]; ok && priority > max {
			selected = policyName
			max = priority
		}
	}
	return selected
}
