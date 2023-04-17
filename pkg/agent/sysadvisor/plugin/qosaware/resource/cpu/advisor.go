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
	"math"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provisionpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func init() {
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyCanonical, provisionpolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
}

// todo:
// 1. Support getting checkpoint synchronously
// 2. Support dedicated with numa binding non-exclusive containers
// 3. Support shared cores containers with different cpu enhancement
// 4. Isolate bursting containers to isolation regions

const (
	cpuResourceAdvisorName string = "cpu-resource-advisor"

	startUpPeriod time.Duration = 30 * time.Second
)

const regionNameSeparator = "-"

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
	conf      *config.Configuration
	extraConf interface{}

	name        string
	advisorCh   chan CPUProvision
	startTime   time.Time
	systemNumas machine.CPUSet

	regionMap          map[string]region.QoSRegion              // map[regionName]region
	containerRegionMap map[string]map[string][]region.QoSRegion // map[podUID][containerName]regions
	poolRegionMap      map[string][]region.QoSRegion            // map[poolName]regions

	sharedNumas machine.CPUSet // numas without numa binding workloads
	mutex       sync.RWMutex

	metaCache  metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *cpuResourceAdvisor {

	cra := &cpuResourceAdvisor{
		name:        cpuResourceAdvisorName,
		advisorCh:   make(chan CPUProvision),
		startTime:   time.Now(),
		systemNumas: metaServer.CPUDetails.NUMANodes(),

		regionMap:          make(map[string]region.QoSRegion),
		containerRegionMap: make(map[string]map[string][]region.QoSRegion),
		poolRegionMap:      make(map[string][]region.QoSRegion),

		sharedNumas: machine.NewCPUSet(),

		conf:      conf,
		extraConf: extraConf,

		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}

	return cra
}

func (cra *cpuResourceAdvisor) Name() string {
	return cra.name
}

// Update works in a monolith way to maintain lifecycle and trigger update actions for all regions;
// todo, re-consider whether it's efficient or we should make start individual goroutine for each region
func (cra *cpuResourceAdvisor) Update() {
	cra.mutex.Lock()
	defer cra.mutex.Unlock()

	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	reservePoolInfo, ok := cra.metaCache.GetPoolInfo(state.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Warningf("[qosaware-cpu] skip update: reserve pool not exist")
		return
	}

	// Assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		klog.Errorf("[qosaware-cpu] assign containers to regions failed: %v", err)
		return
	}
	klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))

	// Run an episode of provision policy update for each region
	for _, r := range cra.regionMap {
		regionNumas := r.GetBindingNumas()

		// Calculate region max available cpu limit,
		// which equals the number of cpus in region numas
		regionCPULimit := regionNumas.Size() * cra.metaServer.CPUsPerNuma()

		// Calculate region reserve pool size value, which equals the cpuset intersection
		// size between region numas and node reserve pool
		regionReservePoolSize := 0
		for _, numaID := range regionNumas.ToSliceInt() {
			if cpuset, ok := reservePoolInfo.TopologyAwareAssignments[numaID]; ok {
				regionReservePoolSize += cpuset.Size()
			}
		}

		reserved := cra.conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate()[v1.ResourceCPU]
		reservedForAllocate := reserved.Value()

		// Calculate region reserved for allocate, which equals average per numa reserved
		// value times the number of numa nodes
		regionReservedForAllocate := int(math.Ceil(float64(int(reservedForAllocate)*regionNumas.Size()) /
			float64(cra.metaServer.NumNUMANodes)))

		r.SetEssentials(types.ResourceEssentials{
			Total:               regionCPULimit,
			ReservePoolSize:     regionReservePoolSize,
			ReservedForAllocate: regionReservedForAllocate,
			EnableReclaim:       cra.conf.ReclaimedResourceConfiguration.EnableReclaim(),
		})

		r.TryUpdateProvision()
	}

	// Sync region information to metacache
	regionEntries, err := cra.assembleRegionEntries()
	if err != nil {
		klog.Errorf("[qosaware-cpu] assemble region entries failed: %v", err)
		return
	}
	_ = cra.metaCache.UpdateRegionEntries(regionEntries)

	// Skip notifying cpu server during startup
	if time.Now().Before(cra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip notifying cpu server: starting up")
		return
	}

	// Assemble provision result from each region
	provision, err := cra.assembleProvision()
	if err != nil {
		klog.Errorf("[qosaware-cpu] assemble provision failed: %v", err)
		return
	}

	// Notify cpu server about provision result
	cra.advisorCh <- provision
	klog.Infof("[qosaware-cpu] notify cpu server: %+v", provision)

	// Update headroom policy. Do this after updating provision because headroom policy
	// may need the latest region provision result from metacache.
	for _, r := range cra.regionMap {
		r.TryUpdateHeadroom()
	}
}

func (cra *cpuResourceAdvisor) GetChannel() interface{} {
	return cra.advisorCh
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	cra.mutex.RLock()
	defer cra.mutex.RUnlock()

	reservePoolSize, ok := cra.metaCache.GetPoolSize(state.PoolNameReserve)
	if !ok {
		return resource.Quantity{}, fmt.Errorf("reserve pool not exist")
	}

	// Return maximum available resource value as headroom when no region exists
	if len(cra.regionMap) <= 0 {
		return *resource.NewQuantity(int64(cra.metaServer.NumCPUs-reservePoolSize), resource.DecimalSI), nil
	}

	totalHeadroom := resource.NewQuantity(0, resource.DecimalSI)
	for _, r := range cra.regionMap {
		headroom, err := r.GetHeadroom()
		if err != nil {
			return headroom, err
		}
		// FIXME: is it reasonable to simply add headroom together?
		totalHeadroom.Add(headroom)
	}

	return *totalHeadroom, nil
}

// assignContainersToRegions re-construct regions every time (instead of an incremental way),
// and this requires metaCache to ensure data integrity
func (cra *cpuResourceAdvisor) assignContainersToRegions() error {
	var errList []error

	// Clear containers for all regions
	for _, r := range cra.regionMap {
		r.Clear()
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
			// region may be set in regionMap for multiple times, and it is reentrant
			cra.regionMap[r.Name()] = r
		}

		cra.setContainerRegions(ci, regions)
		cra.setPoolRegions(ci.OwnerPoolName, regions)

		return true
	}
	cra.metaCache.RangeContainer(f)

	cra.gc()
	cra.updateSharedNumas()

	return errors.NewAggregate(errList)
}

// assignToRegions returns the region list for the given container;
// may need to construct region structures if they don't exist.
func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		// Assign shared cores container. Focus on pool.
		if regions, ok := cra.getPoolRegions(ci.OwnerPoolName); ok {
			return regions, nil
		}

		name := string(types.QoSRegionTypeShare) + regionNameSeparator + string(uuid.NewUUID())
		r := region.NewQoSRegionShare(name, ci.OwnerPoolName, cra.conf, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)

		return []region.QoSRegion{r}, nil

	} else if ci.IsNumaBinding() {
		// Assign dedicated cores numa exclusive containers. Focus on container.
		if regions, ok := cra.getContainerRegions(ci); ok {
			return regions, nil
		}

		var regions []region.QoSRegion

		// Create regions by numa node
		for numaID := range ci.TopologyAwareAssignments {
			name := string(types.QoSRegionTypeDedicatedNumaExclusive) + regionNameSeparator + string(uuid.NewUUID())
			r := region.NewQoSRegionDedicatedNumaExclusive(name, ci.OwnerPoolName, cra.conf, numaID, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)
			regions = append(regions, r)
		}

		return regions, nil
	}

	return nil, nil
}

// updateSharedNumas updates shared numa info for shared-regions
// - shared-numa = system-numa - dedicated numa
func (cra *cpuResourceAdvisor) updateSharedNumas() {
	cra.sharedNumas = cra.systemNumas

	for _, r := range cra.regionMap {
		if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			cra.sharedNumas = cra.sharedNumas.Difference(r.GetBindingNumas())
		}
	}

	// Set binding numas for non numa binding regions
	for _, r := range cra.regionMap {
		if r.Type() == types.QoSRegionTypeShare {
			r.SetBindingNumas(cra.sharedNumas)
		}
	}
}

func (cra *cpuResourceAdvisor) assembleRegionEntries() (types.RegionEntries, error) {
	entries := make(types.RegionEntries)

	for regionName, r := range cra.regionMap {
		controlKnobMap, err := r.GetProvision()
		if err != nil {
			return nil, err
		}

		entries[regionName] = &types.RegionInfo{
			ControlKnobMap: controlKnobMap,
			RegionType:     r.Type(),
		}
	}

	return entries, nil
}

func (cra *cpuResourceAdvisor) assembleProvision() (CPUProvision, error) {
	// Generate internal calculation result.
	// Must make sure pool names from cpu provision following qrm definition;
	// numa ID set as -1 means no numa-preference is needed.
	provision := CPUProvision{PoolSizeMap: map[string]map[int]resource.Quantity{}}

	// Fill in reserve pool entry
	reservePoolSize, _ := cra.metaCache.GetPoolSize(state.PoolNameReserve)
	provision.PoolSizeMap[state.PoolNameReserve] = map[int]resource.Quantity{
		-1: *resource.NewQuantity(int64(reservePoolSize), resource.DecimalSI),
	}

	nonNumaBindingRequirement := 0

	for _, r := range cra.regionMap {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return provision, fmt.Errorf("get provision with error: %v", err)
		}

		if r.Type() == types.QoSRegionTypeShare {
			// Fill in share pool entry
			sharePoolSize := int(controlKnob[types.ControlKnobNonReclaimedCPUSetSize].Value)
			provision.PoolSizeMap[state.PoolNameShare] = make(map[int]resource.Quantity)
			provision.PoolSizeMap[state.PoolNameShare][cpuadvisor.FakedNumaID] = *resource.NewQuantity(int64(sharePoolSize), resource.DecimalSI)
			nonNumaBindingRequirement += sharePoolSize

		} else if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			regionNumas := r.GetBindingNumas().ToSliceInt()
			if len(regionNumas) != 1 {
				klog.Errorf("region %v with type %v has invalid numa count: %v", r.Name(), r.Type(), regionNumas)
			}

			// Fill in reclaim pool entry for dedicated numa exclusive regions
			reclaimPoolSize := controlKnob[types.ControlKnobReclaimedCPUSupplied].Value
			regionNuma := regionNumas[0] // Always one binding numa for this type of region
			if provision.PoolSizeMap[state.PoolNameReclaim] == nil {
				provision.PoolSizeMap[state.PoolNameReclaim] = make(map[int]resource.Quantity)
			}
			provision.PoolSizeMap[state.PoolNameReclaim][regionNuma] = *resource.NewQuantity(int64(reclaimPoolSize), resource.DecimalSI)
		}
	}

	// Fill in shared reclaimed pool size
	sharedReservePoolSize := int(math.Ceil(float64(reservePoolSize*cra.sharedNumas.Size()) / float64(cra.metaServer.NumNUMANodes)))
	sharedReclaimPoolSize := cra.sharedNumas.Size()*cra.metaServer.CPUsPerNuma() - nonNumaBindingRequirement - sharedReservePoolSize
	if provision.PoolSizeMap[state.PoolNameReclaim] == nil {
		provision.PoolSizeMap[state.PoolNameReclaim] = make(map[int]resource.Quantity)
	}
	provision.PoolSizeMap[state.PoolNameReclaim][cpuadvisor.FakedNumaID] = *resource.NewQuantity(int64(sharedReclaimPoolSize), resource.DecimalSI)
	return provision, nil
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
		if regions, exist := v[ci.ContainerName]; exist {
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
