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
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
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

// todo:
// 1. Isolate bursting pods or containers to isolation regions
// 2. Support dedicated without and with numa binding but non numa exclusive containers

func init() {
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyCanonical, provisionpolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyUtilization, headroompolicy.NewPolicyUtilization)
}

// cpuResourceAdvisor is the entrance of updating cpu resource provision advice for
// all qos regions, and merging them into cpu provision result to notify cpu server.
// Smart algorithms and calculators could be adopted to give accurate realtime resource
// provision hint for each region.
type cpuResourceAdvisor struct {
	conf      *config.Configuration
	extraConf interface{}

	recvCh    chan struct{}
	sendCh    chan types.InternalCalculationResult
	startTime time.Time

	regionMap          map[string]region.QoSRegion // map[regionName]region
	reservedForReclaim map[int]int                 // map[numaID]reservedForReclaim
	numRegionsPerNuma  map[int]int                 // map[numaID]regionQuantity
	nonBindingNumas    machine.CPUSet              // numas without numa binding pods
	calculationResult  types.InternalCalculationResult
	mutex              sync.RWMutex

	metaCache  metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *cpuResourceAdvisor {

	cra := &cpuResourceAdvisor{
		conf:      conf,
		extraConf: extraConf,

		recvCh:    make(chan struct{}),
		sendCh:    make(chan types.InternalCalculationResult),
		startTime: time.Now(),

		regionMap:         make(map[string]region.QoSRegion),
		numRegionsPerNuma: make(map[int]int),
		nonBindingNumas:   machine.NewCPUSet(),
		calculationResult: types.InternalCalculationResult{PoolEntries: make(map[string]map[int]int)},

		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}
	cra.initializeReservedForReclaim()

	return cra
}

func (cra *cpuResourceAdvisor) Run(ctx context.Context) {
	for {
		select {
		case <-cra.recvCh:
			klog.Infof("[qosaware-cpu] receive update trigger from cpu server")
			cra.update()
		case <-ctx.Done():
			return
		}
	}
}

func (cra *cpuResourceAdvisor) GetChannels() (interface{}, interface{}) {
	return cra.recvCh, cra.sendCh
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	cra.mutex.RLock()
	defer cra.mutex.RUnlock()

	// return zero when reclaim is disabled
	if !cra.conf.ReclaimedResourceConfiguration.EnableReclaim() {
		return *resource.NewQuantity(0, resource.DecimalSI), nil
	}

	totalHeadroom := 0.0

	// calculate headroom of binding numas by adding region headroom together
	for _, r := range cra.regionMap {
		if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			headroom, err := r.GetHeadroom()
			if err != nil {
				return resource.Quantity{}, fmt.Errorf("get headroom of region %v failed: %v", r.Name(), err)
			}
			totalHeadroom += headroom
		}
	}

	// calculate headroom of non binding numas according to the corresponding reclaim pool entry
	nonBindingNumasHeadroom, ok := cra.calculationResult.GetPoolEntry(state.PoolNameReclaim, cpuadvisor.FakedNUMAID)
	if !ok {
		return resource.Quantity{}, fmt.Errorf("get headroom of non binding numas failed")
	}
	totalHeadroom += float64(nonBindingNumasHeadroom)

	return *resource.NewQuantity(int64(totalHeadroom), resource.DecimalSI), nil
}

// update works in a monolithic way to maintain lifecycle and triggers update actions for all regions;
// todo: re-consider whether it's efficient or we should make start individual goroutine for each region
func (cra *cpuResourceAdvisor) update() {
	cra.mutex.Lock()
	defer cra.mutex.Unlock()

	// sanity check: if reserve pool exists
	reservePoolInfo, ok := cra.metaCache.GetPoolInfo(state.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Errorf("[qosaware-cpu] skip update: reserve pool does not exist")
		return
	}

	// assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		klog.Errorf("[qosaware-cpu] assign containers to regions failed: %v", err)
		return
	}

	cra.gcRegionMap()
	cra.updateAdvisorEssentials()
	klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))

	// run an episode of provision and headroom policy update for each region
	for name, r := range cra.regionMap {
		r.SetEssentials(types.ResourceEssentials{
			EnableReclaim:       cra.conf.ReclaimedResourceConfiguration.EnableReclaim(),
			ResourceUpperBound:  cra.getRegionMaxRequirement(name),
			ResourceLowerBound:  cra.getRegionMinRequirement(name),
			ReservedForAllocate: cra.getRegionReservedForAllocate(name),
		})
		r.TryUpdateProvision()
		r.TryUpdateHeadroom()
	}

	// skip notifying cpu server during startup
	if time.Now().Before(cra.startTime.Add(types.StartUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip notifying cpu server: starting up")
		return
	}

	// assemble provision result from each region and notify cpu server
	if err := cra.assembleProvision(); err != nil {
		klog.Errorf("[qosaware-cpu] assemble provision failed: %v", err)
		return
	}
	cra.sendCh <- cra.calculationResult
	klog.Infof("[qosaware-cpu] notify cpu server: %+v", cra.calculationResult)

	// sync region information to metacache
	regionEntries, err := cra.assembleRegionEntries()
	if err != nil {
		klog.Errorf("[qosaware-cpu] assemble region entries failed: %v", err)
		return
	}
	_ = cra.metaCache.UpdateRegionEntries(regionEntries)
}

// assignContainersToRegions re-construct regions every time (instead of an incremental way),
// and this requires metaCache to ensure data integrity
func (cra *cpuResourceAdvisor) assignContainersToRegions() error {
	var errList []error

	// clear containers for all regions
	for _, r := range cra.regionMap {
		r.Clear()
	}

	// sync containers
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		regions, err := cra.assignToRegions(ci)
		if err != nil {
			errList = append(errList, err)
		}
		if regions == nil {
			return true
		}

		// update region pod set and region map
		for _, r := range regions {
			if err := r.AddContainer(ci); err != nil {
				errList = append(errList, err)
				return true
			}
			// region may be set in regionMap for multiple times, and it is reentrant
			cra.regionMap[r.Name()] = r
		}

		// update container info
		cra.setContainerRegions(ci, regions)

		// update pool info
		if ci.OwnerPoolName != state.PoolNameDedicated {
			// dedicated pool should not exist in metaCache.poolEntries
			return true
		}
		if err := cra.setPoolRegions(ci.OwnerPoolName, regions); err != nil {
			errList = append(errList, err)
			return true
		}

		return true
	}
	cra.metaCache.RangeAndUpdateContainer(f)

	return errors.NewAggregate(errList)
}

// assignToRegions returns the region list for the given container;
// may need to construct region structures if they don't exist.
func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	if ci == nil {
		return nil, fmt.Errorf("container info is nil")
	}

	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		// do not assign shared container to region when ramping up because its owner pool name is empty
		if ci.RampUp {
			return nil, nil
		}

		// assign shared cores container. focus on pool.
		regions := cra.getPoolRegions(ci.OwnerPoolName)
		if len(regions) > 0 {
			return regions, nil
		}

		// create one region by owner pool name
		r := region.NewQoSRegionShare(ci, cra.conf, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)
		return []region.QoSRegion{r}, nil

	} else if ci.IsNumaBinding() {
		// assign dedicated cores numa exclusive containers. focus on container.
		regions, err := cra.getContainerRegions(ci)
		if err != nil {
			return nil, err
		}
		if len(regions) > 0 {
			return regions, nil
		}

		// create regions by numa node
		for numaID := range ci.TopologyAwareAssignments {
			r := region.NewQoSRegionDedicatedNumaExclusive(ci, cra.conf, numaID, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)
			regions = append(regions, r)
		}
		return regions, nil
	}

	return nil, nil
}

// gcRegionMap deletes empty regions in region map
func (cra *cpuResourceAdvisor) gcRegionMap() {
	for regionName, r := range cra.regionMap {
		if r.IsEmpty() {
			delete(cra.regionMap, regionName)
			klog.Infof("[qosaware-cpu] delete region %v", regionName)
		}
	}
}

// updateAdvisorEssentials updates following essentials after assigning containers to regions:
// 1. non binding numas, i.e. numas without numa binding containers
// 2. binding numas of non numa binding regions
// 3. region quantity of each numa
func (cra *cpuResourceAdvisor) updateAdvisorEssentials() {
	cra.nonBindingNumas = cra.metaServer.CPUDetails.NUMANodes()

	// update non binding numas
	for _, r := range cra.regionMap {
		if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			cra.nonBindingNumas = cra.nonBindingNumas.Difference(r.GetBindingNumas())
		}
	}

	// reset region quantity
	for _, numaID := range cra.metaServer.CPUDetails.NUMANodes().ToSliceInt() {
		cra.numRegionsPerNuma[numaID] = 0
	}

	for _, r := range cra.regionMap {
		// set binding numas for non numa binding regions
		if r.Type() == types.QoSRegionTypeShare {
			r.SetBindingNumas(cra.nonBindingNumas)
		}

		// accumulate region quantity for each numa
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			cra.numRegionsPerNuma[numaID] += 1
		}
	}
}

// assembleProvision generates internal calculation result.
// must make sure pool names from cpu provision following qrm definition;
// numa ID set as -1 means no numa-preference is needed.
func (cra *cpuResourceAdvisor) assembleProvision() error {
	cra.calculationResult.PoolEntries = make(map[string]map[int]int)

	// fill in reserve pool entry
	reservePoolSize, _ := cra.metaCache.GetPoolSize(state.PoolNameReserve)
	cra.calculationResult.SetPoolEntry(state.PoolNameReserve, cpuadvisor.FakedNUMAID, reservePoolSize)

	sharePoolSizes := make(map[string]int)

	for _, r := range cra.regionMap {
		controlKnob, err := r.GetProvision()
		if err != nil {
			return fmt.Errorf("get provision with error: %v", err)
		}

		if r.Type() == types.QoSRegionTypeShare {
			// save raw share pool sizes
			sharePoolSizes[r.OwnerPoolName()] = int(controlKnob[types.ControlKnobNonReclaimedCPUSetSize].Value)

		} else if r.Type() == types.QoSRegionTypeDedicatedNumaExclusive {
			// fill in reclaim pool entry for dedicated numa exclusive regions
			reclaimPoolSize := controlKnob[types.ControlKnobReclaimedCPUSupplied].Value
			regionNuma := r.GetBindingNumas().ToSliceInt()[0] // always one binding numa for this type of region
			cra.calculationResult.SetPoolEntry(state.PoolNameReclaim, regionNuma, int(reclaimPoolSize))
		}
	}

	// regulate share pool sizes
	sharePoolAvailable := int(cra.getNumasAvailableResource(cra.nonBindingNumas))
	enableReclaim := cra.conf.ReclaimedResourceConfiguration.EnableReclaim()
	regulatePoolSizes(sharePoolSizes, sharePoolAvailable, enableReclaim)

	// fill in regulated share pool entries
	for poolName, poolSize := range sharePoolSizes {
		cra.calculationResult.SetPoolEntry(poolName, cpuadvisor.FakedNUMAID, poolSize)
	}

	// fill in reclaim pool entry for non binding numas
	reclaimPoolSizeOfNonBindingNumas := sharePoolAvailable - general.SumUpMapValues(sharePoolSizes) + types.ReservedForReclaim
	cra.calculationResult.SetPoolEntry(state.PoolNameReclaim, cpuadvisor.FakedNUMAID, reclaimPoolSizeOfNonBindingNumas)

	return nil
}
