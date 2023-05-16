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

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
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

type internalPolicyState struct {
	updateStatus types.PolicyUpdateStatus
	initDoOnce   sync.Once
}

type internalProvisionPolicy struct {
	name   types.CPUProvisionPolicyName
	policy provisionpolicy.ProvisionPolicy
	internalPolicyState
}

type internalHeadroomPolicy struct {
	name   types.CPUHeadroomPolicyName
	policy headroompolicy.HeadroomPolicy
	internalPolicyState
}

type QoSRegionBase struct {
	sync.Mutex

	name          string
	ownerPoolName string
	regionType    types.QoSRegionType

	types.ResourceEssentials
	// bindingNumas records numas assigned to this region
	bindingNumas machine.CPUSet
	// podSet records current pod and containers in region keyed by pod uid and container name
	podSet types.PodSet
	// containerTopologyAwareAssignment changes dynamically by adding container
	containerTopologyAwareAssignment types.TopologyAwareAssignment

	// provisionPolicies for comparing and merging different provision policy results,
	// the former has higher priority; provisionPolicyInUse indicates the provision policy
	// that is in-use currently
	provisionPolicies    []*internalProvisionPolicy
	provisionPolicyInUse *internalProvisionPolicy
	// headroomPolicies for comparing and merging different headroom policy results,
	// the former has higher priority; headroomPolicyInUse indicates the provision policy
	// that is in-use currently
	headroomPolicies    []*internalHeadroomPolicy
	headroomPolicyInUse *internalHeadroomPolicy

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// getRegionName returns the name of region owned by container. If numaID specified, the BindingNumas of the region
// will be checked, otherwise only one region should be owned by container.
func getRegionName(ci *types.ContainerInfo, numaID int, metaReader metacache.MetaReader) string {
	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		if len(ci.RegionNames) == 1 {
			// get region name from metaCache
			regionName := ci.RegionNames.List()[0]
			regionInfo, ok := metaReader.GetRegionInfo(regionName)
			if ok && regionInfo.RegionType == types.QoSRegionTypeShare {
				return regionName
			}
		}
	} else if ci.IsNumaBinding() {
		for regionName := range ci.RegionNames {
			regionInfo, ok := metaReader.GetRegionInfo(regionName)
			if ok && regionInfo.RegionType == types.QoSRegionTypeDedicatedNumaExclusive {
				regionNUMAs := regionInfo.BindingNumas.ToSliceInt()
				if len(regionNUMAs) == 1 && regionNUMAs[0] == numaID {
					return regionName
				}
			}
		}
	}
	return ""
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

		provisionPolicies: make([]*internalProvisionPolicy, 0),
		headroomPolicies:  make([]*internalHeadroomPolicy, 0),

		metaReader: metaReader,
		metaServer: metaServer,
		emitter:    emitter,
	}

	r.initHeadroomPolicy(conf, extraConf, metaReader, metaServer, emitter)
	r.initProvisionPolicy(conf, extraConf, metaReader, metaServer, emitter)

	klog.Infof("region [%v/%v/%v] created", r.Name(), r.Type(), r.OwnerPoolName())
	return r
}

func (r *QoSRegionBase) Name() string {
	return r.name
}

func (r *QoSRegionBase) Type() types.QoSRegionType {
	return r.regionType
}

func (r *QoSRegionBase) OwnerPoolName() string {
	return r.ownerPoolName
}

func (r *QoSRegionBase) IsEmpty() bool {
	r.Lock()
	defer r.Unlock()

	return len(r.podSet) <= 0
}

func (r *QoSRegionBase) Clear() {
	r.Lock()
	defer r.Unlock()

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

func (r *QoSRegionBase) GetProvisionPolicy() (policyTopPriority types.CPUProvisionPolicyName, policyInUse types.CPUProvisionPolicyName) {
	r.Lock()
	defer r.Unlock()

	policyTopPriority = types.CPUProvisionPolicyNone
	if len(r.provisionPolicies) > 0 {
		policyTopPriority = r.provisionPolicies[0].name
	}

	policyInUse = types.CPUProvisionPolicyNone
	if r.provisionPolicyInUse != nil {
		policyInUse = r.provisionPolicyInUse.name
	}

	return
}

func (r *QoSRegionBase) GetHeadRoomPolicy() (policyTopPriority types.CPUHeadroomPolicyName, policyInUse types.CPUHeadroomPolicyName) {
	r.Lock()
	defer r.Unlock()

	policyTopPriority = types.CPUHeadroomPolicyNone
	if len(r.headroomPolicies) > 0 {
		policyTopPriority = r.headroomPolicies[0].name
	}

	policyInUse = types.CPUHeadroomPolicyNone
	if r.headroomPolicyInUse != nil {
		policyInUse = r.headroomPolicyInUse.name
	}

	return
}

// initProvisionPolicy initializes provision by adding additional policies into default ones
func (r *QoSRegionBase) initProvisionPolicy(conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	configuredProvisionPolicy, ok := conf.CPUAdvisorConfiguration.ProvisionPolicies[r.regionType]
	if !ok {
		klog.Warningf("failed to find provision policies for region %v", r.regionType)
		return
	}

	// try new policies
	// todo move to separate functions
	initializers := provisionpolicy.GetRegisteredInitializers()
	for _, policyName := range configuredProvisionPolicy {
		if initializer, ok := initializers[policyName]; ok {
			cpuRegulator := regulator.NewCPURegulator()
			policy := initializer(r.name, conf, extraConf, cpuRegulator, metaReader, metaServer, emitter)
			policy.SetBindingNumas(r.bindingNumas)
			r.provisionPolicies = append(r.provisionPolicies, &internalProvisionPolicy{
				name:                policyName,
				policy:              policy,
				internalPolicyState: internalPolicyState{updateStatus: types.PolicyUpdateFailed},
			})
		}
	}
}

// initHeadroomPolicy initializes headroom by adding additional policies into default ones
func (r *QoSRegionBase) initHeadroomPolicy(conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) {
	configuredHeadroomPolicy, ok := conf.CPUAdvisorConfiguration.HeadroomPolicies[r.regionType]
	if !ok {
		klog.Warningf("failed to find provision policies for region %v", r.regionType)
		return
	}

	// try new policies
	headroomInitializers := headroompolicy.GetRegisteredInitializers()
	for _, policyName := range configuredHeadroomPolicy {
		if initializer, ok := headroomInitializers[policyName]; ok {
			policy := initializer(r.name, conf, extraConf, metaReader, metaServer, emitter)
			r.headroomPolicies = append(r.headroomPolicies, &internalHeadroomPolicy{
				name:                policyName,
				policy:              policy,
				internalPolicyState: internalPolicyState{updateStatus: types.PolicyUpdateFailed},
			})
		}
	}
}

func (r *QoSRegionBase) buildProvisionEssentials(minRequirement int) types.ResourceEssentials {
	return types.ResourceEssentials{
		EnableReclaim:       r.EnableReclaim,
		ReservedForAllocate: r.ReservedForAllocate,
		MinRequirement:      minRequirement,
		MaxRequirement:      r.Total - r.ReservePoolSize - int(math.Ceil(float64(types.MinDedicatedCPURequirement)/float64(r.metaServer.NumNUMANodes)))*r.bindingNumas.Size(),
	}
}
