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

package cpu

import (
	"fmt"
	"sort"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// todo:
// 1. support getting checkpoint synchronously
// 2. assign non shared containers to their regions
// 3. assign shared containers with different cpu enhancement to different share regions
// 4. support isolating bursting containers to isolation regions

const (
	cpuResourceAdvisorName string = "cpu-resource-advisor"

	startUpPeriod time.Duration = 30 * time.Second
)

// CPUProvision is the internal data structure for conveying minimal provision result
// to cpu server
type CPUProvision struct {
	// [poolName][numaId]cores
	PoolSizeMap map[string]map[int]resource.Quantity
}

// cpuResourceAdvisor is the entrance of updating cpu resource provision advice for
// all qos regions, and merging them into cpu provision result to notify cpu server.
// Smart algorithms and calculators could be adopted to give accurate realtime resource
// provision hint for each region.
type cpuResourceAdvisor struct {
	name                string
	provisionPolicyName types.CPUProvisionPolicyName
	headroomPolicyMap   map[types.QoSRegionType]headroompolicy.HeadroomPolicy
	startTime           time.Time
	cpuLimitSystem      int
	numaLimitSystem     int
	reservedForAllocate int

	regionMap          map[string]region.QoSRegion              // map[regionName]region
	containerRegionMap map[string]map[string][]region.QoSRegion // map[podUID][containerName]regions
	poolRegionMap      map[string][]region.QoSRegion            // map[poolName]regions
	advisorCh          chan CPUProvision

	conf      *config.Configuration
	metaCache *metacache.MetaCache
	emitter   metrics.MetricEmitter

	mutex            sync.RWMutex
	lastUpdateStatus types.UpdateStatus
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, metaCache *metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) (*cpuResourceAdvisor, error) {

	headroomPolicies := make(map[types.QoSRegionType]headroompolicy.HeadroomPolicy)
	headroomInitFuncs := headroompolicy.GetRegisteredInitializers()
	for qosRegion, policy := range conf.CPUHeadroomPolicies {
		initFunc, ok := headroomInitFuncs[policy]
		if !ok {
			initFunc = headroompolicy.NewPolicyCanonical
		}
		headroomPolicies[qosRegion] = initFunc(metaCache, metaServer)
	}

	cra := &cpuResourceAdvisor{
		name:                cpuResourceAdvisorName,
		provisionPolicyName: types.CPUProvisionPolicyName(conf.CPUAdvisorConfiguration.CPUProvisionPolicy),
		headroomPolicyMap:   headroomPolicies,
		startTime:           time.Now(),
		cpuLimitSystem:      metaServer.NumCPUs,
		numaLimitSystem:     metaServer.NumNUMANodes,

		regionMap:          make(map[string]region.QoSRegion),
		containerRegionMap: make(map[string]map[string][]region.QoSRegion),
		poolRegionMap:      make(map[string][]region.QoSRegion),
		advisorCh:          make(chan CPUProvision),

		conf:      conf,
		metaCache: metaCache,
		emitter:   emitter,
	}

	// empty region should be always existed
	emptyRegionName := string(types.QoSRegionTypeEmpty) + string(uuid.NewUUID())
	r := region.NewQoSRegionEmpty(emptyRegionName, cra.numaLimitSystem, cra.metaCache, cra.emitter)
	cra.regionMap[r.Name()] = r

	// todo: support dynamic reserved resource
	reserved := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceCPU]
	cra.reservedForAllocate = int(reserved.Value())

	return cra, nil
}

func (cra *cpuResourceAdvisor) Name() string {
	return cra.name
}

// sort from the highest priority to lowest
func sortRegionMapByPriority(regionMap map[string]region.QoSRegion) region.QosRegions {
	regions := make(region.QosRegions, 0)
	for _, r := range regionMap {
		regions = append(regions, r)
	}
	sort.Sort(sort.Reverse(regions))
	return regions
}

func (cra *cpuResourceAdvisor) Update() (err error) {
	cra.mutex.Lock()
	defer func() {
		if err != nil {
			cra.lastUpdateStatus = types.UpdateFailed
		} else {
			cra.lastUpdateStatus = types.UpdateSucceeded
		}
		cra.mutex.Unlock()
	}()

	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	reservePoolInfo, ok := cra.metaCache.GetPoolInfo(state.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Warningf("[qosaware-cpu] skip update: reserve pool not exist")
		return fmt.Errorf("reserve pool not exist")
	}

	// Assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		klog.Errorf("[qosaware-cpu] skip update: %v", err)
		return err
	}
	klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))

	regions := sortRegionMapByPriority(cra.regionMap)

	// Run an episode of policy update for each region
	for _, r := range regions {
		numaIDs := r.GetNumaIDs().List()
		if cra.numaLimitSystem <= 0 {
			return fmt.Errorf("numaLimitSystem %v invalid", cra.numaLimitSystem)
		}
		reservePoolSize := 0
		for _, numaID := range numaIDs {
			cpuSet, ok := reservePoolInfo.TopologyAwareAssignments[numaID]
			if ok {
				reservePoolSize += cpuSet.Size()
			}
		}
		r.SetReservePoolSize(reservePoolSize)
		r.SetReservedForAllocate(cra.reservedForAllocate * len(numaIDs) / cra.numaLimitSystem)
		r.SetCPULimit(cra.cpuLimitSystem * len(numaIDs) / cra.numaLimitSystem)

		if err = r.TryUpdateControlKnob(); err != nil {
			return err
		}
		if err = r.TryUpdateHeadroom(); err != nil {
			return err
		}
	}

	// Skip notifying cpu server during startup
	if time.Now().Before(cra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip notifying cpu server: starting up")
		return fmt.Errorf("skip notifying cpu server: starting up")
	}

	provision, err := cra.assembleProvision()
	if err != nil {
		return err
	}

	// Notify cpu server
	cra.advisorCh <- provision
	klog.Infof("[qosaware-cpu] notify cpu server: %+v", provision)
	return nil
}

func (cra *cpuResourceAdvisor) assembleProvision() (CPUProvision, error) {
	provision := CPUProvision{PoolSizeMap: map[string]map[int]resource.Quantity{}}
	reservePoolSize, ok := cra.metaCache.GetPoolSize(state.PoolNameReserve)
	if !ok {
		klog.Warningf("[qosaware-cpu] skip update: reserve pool not exist")
		return provision, fmt.Errorf("reserve pool not exist")
	}

	// Generate internal calculation result.
	// Must make sure pool names from cpu provision following qrm definition;
	// numa ID set as -1 means no numa-preference is needed.
	provision.PoolSizeMap[state.PoolNameReserve] = map[int]resource.Quantity{
		-1: *resource.NewQuantity(int64(reservePoolSize), resource.DecimalSI),
	}

	var reclaimedPoolSize = 0.0
	for _, r := range cra.regionMap {
		c, err := r.GetControlKnobUpdated()
		if err != nil {
			klog.Errorf("[qosaware-cpu] skip notifying cpu server: %v", err)
			return provision, fmt.Errorf("skip notifying cpu server: %v", err)
		}

		if r.Type() == types.QoSRegionTypeShare {
			sharePoolSize := c[types.ControlKnobGuranteedCPUSetSize].Value
			provision.PoolSizeMap[state.PoolNameShare] = make(map[int]resource.Quantity)
			provision.PoolSizeMap[state.PoolNameShare][-1] = *resource.NewQuantity(int64(sharePoolSize), resource.DecimalSI)
			reclaimedPoolSize += c[types.ControlKnobReclaimedCPUSetSize].Value
		} else if r.Type() == types.QoSRegionTypeDedicatedNuma {
			// todo
			reclaimedPoolSize += c[types.ControlKnobReclaimedCPUSetSize].Value
		} else if r.Type() == types.QoSRegionTypeEmpty {
			reclaimedPoolSize += c[types.ControlKnobReclaimedCPUSetSize].Value
		}
	}
	provision.PoolSizeMap[state.PoolNameReclaim] = map[int]resource.Quantity{-1: *resource.NewQuantity(int64(reclaimedPoolSize), resource.DecimalSI)}

	return provision, nil
}

func (cra *cpuResourceAdvisor) GetChannel() interface{} {
	return cra.advisorCh
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	cra.mutex.RLock()
	defer cra.mutex.RUnlock()

	if cra.lastUpdateStatus != types.UpdateSucceeded {
		return resource.Quantity{}, fmt.Errorf("headroomUpdated is false")
	}

	totalHeadroom := resource.NewQuantity(0, resource.DecimalSI)
	for _, region := range cra.regionMap {
		headroom, err := region.GetHeadroom()
		if err != nil {
			return headroom, err
		}
		// FIXME: is it reasonable to simply add headroom together?
		totalHeadroom.Add(headroom)
	}
	return *totalHeadroom, nil
}

func (cra *cpuResourceAdvisor) assignContainersToRegions() error {
	var errList []error

	// Clear containers for all regions
	for _, region := range cra.regionMap {
		region.Clear()
	}

	// Sync containers
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		regions, err := cra.assignToRegions(ci)
		if err != nil {
			errList = append(errList, err)
		}
		if regions == nil {
			return true
		}
		for _, r := range regions {
			if err := r.AddContainer(ci); err != nil {
				errList = append(errList, err)
				return true
			}
			cra.regionMap[r.Name()] = r
		}

		cra.setContainerRegions(ci, regions)
		cra.setPoolRegions(ci.OwnerPoolName, regions)

		return true
	}
	cra.metaCache.RangeContainer(f)

	cra.gc()

	// set placeholder container to empty region to indicate the numaIDs it occupied
	var emptyRegion region.QoSRegion = nil
	assignedNumaIDs := sets.NewInt()
	for _, r := range cra.regionMap {
		if r.Type() == types.QoSRegionTypeEmpty {
			emptyRegion = r
		}
		assignedNumaIDs = assignedNumaIDs.Union(r.GetNumaIDs())
	}
	if emptyRegion == nil {
		return fmt.Errorf("empty region not existed")
	}
	allNumaIDs := sets.NewInt()
	for numaID := 0; numaID < cra.numaLimitSystem; numaID++ {
		allNumaIDs.Insert(numaID)
	}
	emptyNumaIDs := allNumaIDs.Difference(assignedNumaIDs)
	placeholder := &types.ContainerInfo{
		TopologyAwareAssignments: map[int]machine.CPUSet{},
	}
	for numaID := range emptyNumaIDs {
		placeholder.TopologyAwareAssignments[numaID] = machine.NewCPUSet(-1)
	}
	emptyRegion.AddContainer(placeholder)

	return errors.NewAggregate(errList)
}

func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		// Shared
		if regions, ok := cra.getPoolRegions(ci.OwnerPoolName); ok {
			return regions, nil
		}
		name := string(types.QoSRegionTypeShare) + string(uuid.NewUUID())
		headroomPolicy, ok := cra.headroomPolicyMap[types.QoSRegionTypeShare]
		if !ok {
			return nil, fmt.Errorf("failed to find headroom policy for QoSRegionTypeShare")
		}
		r := region.NewQoSRegionShare(name, ci.OwnerPoolName, types.QoSRegionTypeShare, cra.provisionPolicyName, headroomPolicy, cra.numaLimitSystem, cra.conf, cra.metaCache, cra.emitter)
		return []region.QoSRegion{r}, nil
	} else if ci.IsNumaBinding() {
		// dedicated cores with numa binding
		if regions, ok := cra.getContainerRegions(ci); ok {
			return regions, nil
		}
		regions := make([]region.QoSRegion, 0)
		for numaID := range ci.TopologyAwareAssignments {
			name := string(types.QoSRegionTypeDedicatedNuma) + string(uuid.NewUUID())
			r, ok := cra.regionMap[name]
			if !ok {
				headroomPolicy, ok := cra.headroomPolicyMap[types.QoSRegionTypeDedicatedNuma]
				if !ok {
					return nil, fmt.Errorf("failed to find headroom policy for QoSRegionTypeDedicatedNuma")
				}
				r = region.NewQoSRegionDedicatedNuma(name, ci.OwnerPoolName, cra.provisionPolicyName, headroomPolicy, numaID, cra.metaCache, cra.emitter)
			}
			regions = append(regions, r)
		}
		return regions, nil
	}

	return nil, nil
}

func (cra *cpuResourceAdvisor) gc() {
	// Delete empty regions in region map
	for regionName, r := range cra.regionMap {
		if r.IsEmpty() {
			delete(cra.regionMap, regionName)
			klog.Infof("[qosaware-cpu] delete region %v", regionName)
		}
	}

	// Delete non exist pods in container region map
	for podUID := range cra.containerRegionMap {
		if _, ok := cra.metaCache.GetContainerEntries(podUID); !ok {
			delete(cra.containerRegionMap, podUID)
		}
	}

	// Delete non exist pools in pool region map
	for poolName := range cra.poolRegionMap {
		if _, ok := cra.metaCache.GetPoolInfo(poolName); !ok {
			delete(cra.poolRegionMap, poolName)
		}
	}
}

func (cra *cpuResourceAdvisor) getContainerRegions(ci *types.ContainerInfo) ([]region.QoSRegion, bool) {
	if v, ok := cra.containerRegionMap[ci.PodUID]; ok {
		if regions, ok := v[ci.ContainerName]; ok {
			return regions, true
		}
	}
	return nil, false
}

func (cra *cpuResourceAdvisor) setContainerRegions(ci *types.ContainerInfo, regions []region.QoSRegion) {
	v, ok := cra.containerRegionMap[ci.PodUID]
	if !ok {
		cra.containerRegionMap[ci.PodUID] = make(map[string][]region.QoSRegion)
		v = cra.containerRegionMap[ci.PodUID]
	}
	v[ci.ContainerName] = regions
}

func (cra *cpuResourceAdvisor) getPoolRegions(poolName string) ([]region.QoSRegion, bool) {
	if regions, ok := cra.poolRegionMap[poolName]; ok {
		return regions, true
	}
	return nil, false
}

func (cra *cpuResourceAdvisor) setPoolRegions(poolName string, regions []region.QoSRegion) {
	cra.poolRegionMap[poolName] = regions
}
