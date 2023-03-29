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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// todo:
// 1. Support getting checkpoint synchronously
// 2. Support dedicated with numa binding non-exclusive containers
// 3. Support shared cores containers with different cpu enhancement
// 4. Isolate bursting containers to isolation regions

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
	advisorCh           chan CPUProvision
	startTime           time.Time
	reservedForAllocate int64
	systemNumas         machine.CPUSet

	regionMap          map[string]region.QoSRegion              // map[regionName]region
	containerRegionMap map[string]map[string][]region.QoSRegion // map[podUID][containerName]regions
	poolRegionMap      map[string][]region.QoSRegion            // map[poolName]regions

	lastUpdateStatus types.UpdateStatus
	mutex            sync.RWMutex

	conf       *config.Configuration
	metaCache  *metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, metaCache *metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *cpuResourceAdvisor {

	cra := &cpuResourceAdvisor{
		name:        cpuResourceAdvisorName,
		advisorCh:   make(chan CPUProvision),
		startTime:   time.Now(),
		systemNumas: metaServer.CPUDetails.NUMANodes(),

		regionMap:          make(map[string]region.QoSRegion),
		containerRegionMap: make(map[string]map[string][]region.QoSRegion),
		poolRegionMap:      make(map[string][]region.QoSRegion),

		conf:       conf,
		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}

	// todo: support dynamic reserved resource from kcc
	reserved := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceCPU]
	cra.reservedForAllocate = reserved.Value()

	return cra
}

func (cra *cpuResourceAdvisor) Name() string {
	return cra.name
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
		return nil
	}

	// Assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		return err
	}
	klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))

	// Run an episode of policy update for each region
	for _, r := range cra.regionMap {
		regionNumas := r.GetNumas()

		// Calculate region max available cpu limit, which equals the number of cpus in
		// region numas
		regionCPULimit := regionNumas.Size()

		// Calculate region reserve pool size value, which equals the cpuset intersaction
		// size between region numas and node reserve pool
		regionReservePoolSize := 0
		for _, numaID := range regionNumas.ToSliceInt() {
			if cpuset, ok := reservePoolInfo.TopologyAwareAssignments[numaID]; ok {
				regionReservePoolSize += cpuset.Size()
			}
		}

		// Calculate region reserved for allocate, which equals average per numa reserved
		// value times the number of numa nodes
		regionReservedForAllocate := int(math.Ceil(float64(cra.reservedForAllocate*int64(regionNumas.Size())) / float64(cra.systemNumas.Size())))

		r.SetEssentials(regionCPULimit, regionReservePoolSize, regionReservedForAllocate)

		if err = r.TryUpdateProvision(); err != nil {
			return err
		}
		if err = r.TryUpdateHeadroom(); err != nil {
			return err
		}
	}

	// Skip notifying cpu server during startup
	if time.Now().Before(cra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip notifying cpu server: starting up")
		return nil
	}

	// Assemble provision result from each region
	provision, err := cra.assembleProvision()
	if err != nil {
		return err
	}

	// Notify cpu server
	cra.advisorCh <- provision
	klog.Infof("[qosaware-cpu] notify cpu server: %+v", provision)
	return nil
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

	return errors.NewAggregate(errList)
}

func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		// Assign shared cores container. Focus on pool.
		if regions, ok := cra.getPoolRegions(ci.OwnerPoolName); ok {
			return regions, nil
		}

		name := string(types.QoSRegionTypeShare) + string(uuid.NewUUID())
		bindingNumas := machine.NewCPUSet() // bindingNumas is trivial for share region
		r := region.NewQoSRegionShare(name, ci.OwnerPoolName, bindingNumas, cra.conf, cra.metaCache, cra.metaServer, cra.emitter)

		return []region.QoSRegion{r}, nil

	} else if ci.IsNumaBinding() {
		// Assign dedicated cores with numa binding and numa exclusive containers. Focus on container.
		if regions, ok := cra.getContainerRegions(ci); ok {
			return regions, nil
		}

		regions := []region.QoSRegion{}

		// Create regions by numa node
		for numaID := range ci.TopologyAwareAssignments {
			name := string(types.QoSRegionTypeDedicatedNumaExclusive) + string(uuid.NewUUID())
			bindingNuma := machine.NewCPUSet(numaID)
			r := region.NewQoSRegionDedicatedNumaExclusive(name, ci.OwnerPoolName, bindingNuma, cra.conf, cra.metaCache, cra.metaServer, cra.emitter)
			regions = append(regions, r)
		}

		return regions, nil
	}

	return nil, nil
}

func (cra *cpuResourceAdvisor) assembleProvision() (CPUProvision, error) {
	// Generate internal calculation result.
	// Must make sure pool names from cpu provision following qrm definition;
	// numa ID set as -1 means no numa-preference is needed.
	provision := CPUProvision{PoolSizeMap: map[string]map[int]resource.Quantity{}}

	// Fill in reserve pool entry
	reservePoolSize, ok := cra.metaCache.GetPoolSize(state.PoolNameReserve)
	if !ok {
		return provision, fmt.Errorf("reserve pool not exist")
	}
	provision.PoolSizeMap[state.PoolNameReserve] = map[int]resource.Quantity{
		-1: *resource.NewQuantity(int64(reservePoolSize), resource.DecimalSI),
	}

	var (
		nonNumaBindingRequirement int            = 0
		sharedNumas               machine.CPUSet = cra.systemNumas
		cpusPerNuma               int            = cra.metaServer.NumCPUs / cra.metaServer.NumNUMANodes
	)

	for _, r := range cra.regionMap {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return provision, err
		}

		if r.Type() == types.QoSRegionTypeShare {
			// Fill in share pool entry
			sharePoolSize := int(controlKnob[types.ControlKnobSharedCPUSetSize].Value)
			provision.PoolSizeMap[state.PoolNameShare] = make(map[int]resource.Quantity)
			provision.PoolSizeMap[state.PoolNameShare][-1] = *resource.NewQuantity(int64(sharePoolSize), resource.DecimalSI)
			nonNumaBindingRequirement += sharePoolSize

		} else if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			// Fill in reclaim pool entry for dedicated numa exclusive regions
			reclaimPoolSize := controlKnob[types.ControlKnobReclaimedCPUSetSize].Value
			bindingNuma := r.GetNumas().ToSliceInt()[0] // Always one binding numa for this type of region
			sharedNumas.Difference(r.GetNumas())
			provision.PoolSizeMap[state.PoolNameReclaim] = map[int]resource.Quantity{bindingNuma: *resource.NewQuantity(int64(reclaimPoolSize), resource.DecimalSI)}
		}
	}

	// Fill in shared reclaimed pool size
	sharedReclaimPoolSize := sharedNumas.Size()*cpusPerNuma - nonNumaBindingRequirement
	provision.PoolSizeMap[state.PoolNameReclaim] = map[int]resource.Quantity{-1: *resource.NewQuantity(int64(sharedReclaimPoolSize), resource.DecimalSI)}

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
