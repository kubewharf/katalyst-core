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
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/isolation"
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
// 1. Support dedicated without and with numa binding but non numa exclusive containers

// metric names for resource advisor
const (
	metricRegionStatus    = "region_status"
	metricRegionOvershoot = "region_overshoot"
)

func init() {
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyCanonical, provisionpolicy.NewPolicyCanonical)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyRama, provisionpolicy.NewPolicyRama)

	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyNUMAExclusive, headroompolicy.NewPolicyNUMAExclusive)

	provisionassembler.RegisterInitializer(types.CPUProvisionAssemblerCommon, provisionassembler.NewProvisionAssemblerCommon)

	headroomassembler.RegisterInitializer(types.CPUHeadroomAssemblerCommon, headroomassembler.NewHeadroomAssemblerCommon)
	headroomassembler.RegisterInitializer(types.CPUHeadroomAssemblerDedicated, headroomassembler.NewHeadroomAssemblerDedicated)
}

// cpuResourceAdvisor is the entrance of updating cpu resource provision advice for
// all qos regions, and merging them into cpu provision result to notify cpu server.
// Smart algorithms and calculators could be adopted to give accurate realtime resource
// provision hint for each region.
type cpuResourceAdvisor struct {
	conf      *config.Configuration
	extraConf interface{}

	recvCh         chan struct{}
	sendCh         chan types.InternalCPUCalculationResult
	startTime      time.Time
	advisorUpdated bool

	regionMap          map[string]region.QoSRegion // map[regionName]region
	reservedForReclaim map[int]int                 // map[numaID]reservedForReclaim
	numaAvailable      map[int]int                 // map[numaID]availableResource
	numRegionsPerNuma  map[int]int                 // map[numaID]regionQuantity
	nonBindingNumas    machine.CPUSet              // numas without numa binding pods

	provisionAssembler provisionassembler.ProvisionAssembler
	headroomAssembler  headroomassembler.HeadroomAssembler

	isolator isolation.Isolator

	mutex      sync.RWMutex
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

		recvCh:         make(chan struct{}),
		sendCh:         make(chan types.InternalCPUCalculationResult, 1),
		startTime:      time.Now(),
		advisorUpdated: false,

		regionMap:          make(map[string]region.QoSRegion),
		reservedForReclaim: make(map[int]int),
		numaAvailable:      make(map[int]int),
		numRegionsPerNuma:  make(map[int]int),
		nonBindingNumas:    machine.NewCPUSet(),

		isolator: isolation.NewLoadIsolator(conf, extraConf, emitter, metaCache, metaServer),

		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}

	coreNumReservedForReclaim := conf.DynamicAgentConfiguration.GetDynamicConfiguration().MinReclaimedResourceForAllocate[v1.ResourceCPU]
	cra.reservedForReclaim = machine.GetCoreNumReservedForReclaim(int(coreNumReservedForReclaim.Value()), metaServer.KatalystMachineInfo.NumNUMANodes)

	if err := cra.initializeProvisionAssembler(); err != nil {
		klog.Errorf("[qosaware-cpu] initialize provision assembler failed: %v", err)
	}
	if err := cra.initializeHeadroomAssembler(); err != nil {
		klog.Errorf("[qosaware-cpu] initialize headroom assembler failed: %v", err)
	}

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

	if !cra.advisorUpdated {
		return resource.Quantity{}, fmt.Errorf("starting up")
	}

	if cra.headroomAssembler == nil {
		klog.Errorf("[qosaware-cpu] get headroom failed: no legal assembler")
		return resource.Quantity{}, fmt.Errorf("no legal assembler")
	}

	headroom, err := cra.headroomAssembler.GetHeadroom()
	if err != nil {
		klog.Errorf("[qosaware-cpu] get headroom failed: %v", err)
	}
	return headroom, err
}

// update works in a monolithic way to maintain lifecycle and triggers update actions for all regions;
// todo: re-consider whether it's efficient or we should make start individual goroutine for each region
func (cra *cpuResourceAdvisor) update() {
	cra.mutex.Lock()
	defer cra.mutex.Unlock()

	// skip updating during startup
	if time.Now().Before(cra.startTime.Add(types.StartUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip updating: starting up")
		return
	}

	// sanity check: if reserve pool exists
	reservePoolInfo, ok := cra.metaCache.GetPoolInfo(state.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Errorf("[qosaware-cpu] skip update: reserve pool does not exist")
		return
	}

	cra.updateNumasAvailableResource()
	cra.setIsolatedContainers()

	// assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		klog.Errorf("[qosaware-cpu] assign containers to regions failed: %v", err)
		return
	}

	cra.gcRegionMap()
	cra.setRegionEntries()
	cra.updateAdvisorEssentials()

	// run an episode of provision and headroom policy update for each region
	for _, r := range cra.regionMap {
		r.SetEssentials(types.ResourceEssentials{
			EnableReclaim:       cra.conf.GetDynamicConfiguration().EnableReclaim,
			ResourceUpperBound:  cra.getRegionMaxRequirement(r),
			ResourceLowerBound:  cra.getRegionMinRequirement(r),
			ReservedForReclaim:  cra.getRegionReservedForReclaim(r),
			ReservedForAllocate: cra.getRegionReservedForAllocate(r),
		})

		r.TryUpdateProvision()
		cra.updateRegionProvision()

		r.TryUpdateHeadroom()
		cra.updateRegionHeadroom()
	}
	cra.advisorUpdated = true

	klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))

	// assemble provision result from each region
	calculationResult, boundUpper, err := cra.assembleProvision()
	if err != nil {
		klog.Errorf("[qosaware-cpu] assemble provision failed: %v", err)
		return
	}
	cra.updateRegionStatus(boundUpper)

	// notify cpu server
	select {
	case cra.sendCh <- calculationResult:
		general.Infof("notify cpu server: %+v", calculationResult)
	default:
		general.Errorf("channel is full")
	}
}

// setIsolatedContainers get isolation status from isolator and update into containers
func (cra *cpuResourceAdvisor) setIsolatedContainers() {
	isolatedPods := sets.NewString(cra.isolator.GetIsolatedPods()...)
	if len(isolatedPods) > 0 {
		general.Infof("current isolated pod: %v", isolatedPods.List())
	}

	_ = cra.metaCache.RangeAndUpdateContainer(func(podUID string, _ string, ci *types.ContainerInfo) bool {
		ci.Isolated = false
		if isolatedPods.Has(podUID) {
			ci.Isolated = true
		}
		return true
	})
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
		if ci.OwnerPoolName == state.PoolNameDedicated {
			// dedicated pool should not exist in metaCache.poolEntries
			return true
		} else if ci.Isolated && !strings.HasPrefix(ci.OwnerPoolName, state.PoolNameIsolation) {
			// if we haven't generated corresponding pods isolated containers, just return
			return true
		} else {
			// todo currently, we may call setPoolRegions multiple time, and we
			//  depend on the reentrant of it, need to refine
			if err := cra.setPoolRegions(ci.OwnerPoolName, regions); err != nil {
				errList = append(errList, err)
				return true
			}
		}

		return true
	}
	_ = cra.metaCache.RangeAndUpdateContainer(f)

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

		if ci.Isolated {
			if ci.OwnerPoolName != state.PoolNameShare {
				regions := cra.getPoolRegions(ci.OwnerPoolName)
				if len(regions) > 0 {
					return regions, nil
				}
			}

			r := region.NewQoSRegionIsolation(ci, cra.conf, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)
			return []region.QoSRegion{r}, nil
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
// 1. non-binding numas, i.e. numas without numa binding containers
// 2. binding numas of non numa binding regions
// 3. region quantity of each numa
func (cra *cpuResourceAdvisor) updateAdvisorEssentials() {
	cra.nonBindingNumas = cra.metaServer.CPUDetails.NUMANodes()

	// update non-binding numas
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
func (cra *cpuResourceAdvisor) assembleProvision() (types.InternalCPUCalculationResult, bool, error) {
	if cra.provisionAssembler == nil {
		return types.InternalCPUCalculationResult{}, false, fmt.Errorf("no legal provision assembler")
	}

	return cra.provisionAssembler.AssembleProvision()
}
