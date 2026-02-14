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
	"math"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
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
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// todo:
// 1. Support dedicated without and with numa binding but non numa exclusive containers

// metric names for cpu advisor
const (
	metricCPUAdvisorPoolSize           = "cpu_advisor_pool_size"
	metricCPUAdvisorPoolQuota          = "cpu_advisor_pool_quota"
	metricCPUAdvisorUpdateDuration     = "cpu_advisor_update_duration"
	metricRegionStatus                 = "region_status"
	metricRegionIndicatorTargetPrefix  = "region_indicator_target_"
	metricRegionIndicatorCurrentPrefix = "region_indicator_current_"
	metricRegionIndicatorErrorPrefix   = "region_indicator_error_"

	cpuAdvisorHealthCheckName     = "cpu_advisor_update"
	healthCheckTolerationDuration = 30 * time.Second
)

var errIsolationSafetyCheckFailed = fmt.Errorf("isolation safety check failed")

func init() {
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyNone, provisionpolicy.NewPolicyNone)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyCanonical, provisionpolicy.NewPolicyCanonical)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyRama, provisionpolicy.NewPolicyRama)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyDynamicQuota, provisionpolicy.NewPolicyDynamicQuota)

	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyNone, headroompolicy.NewPolicyNone)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyNUMADedicated, headroompolicy.NewPolicyNUMADedicated)

	provisionassembler.RegisterInitializer(types.CPUProvisionAssemblerCommon, provisionassembler.NewProvisionAssemblerCommon)

	headroomassembler.RegisterInitializer(types.CPUHeadroomAssemblerCommon, headroomassembler.NewHeadroomAssemblerCommon)
	// TODO: CPUHeadroomAssemblerDedicated policy has removed, its name is retained for compatibility.
	headroomassembler.RegisterInitializer(types.CPUHeadroomAssemblerDedicated, headroomassembler.NewHeadroomAssemblerCommon)
}

// cpuResourceAdvisor is the entrance of updating cpu resource provision advice for
// all qos regions, and merging them into cpu provision result to notify cpu server.
// Smart algorithms and calculators could be adopted to give accurate realtime resource
// provision hint for each region.
type cpuResourceAdvisor struct {
	conf      *config.Configuration
	extraConf interface{}
	period    time.Duration

	advisorUpdated bool

	regionMap          map[string]region.QoSRegion // map[regionName]region
	reservedForReclaim map[int]int                 // map[numaID]reservedForReclaim
	numaAvailable      map[int]int                 // map[numaID]availableResource
	numRegionsPerNuma  map[int]int                 // map[numaID]regionQuantity
	nonBindingNumas    machine.CPUSet              // numas without numa binding pods

	allowSharedCoresOverlapReclaimedCores bool

	provisionAssembler provisionassembler.ProvisionAssembler
	headroomAssembler  headroomassembler.HeadroomAssembler

	isolator        isolation.Isolator
	isolationSafety bool

	mutex      sync.RWMutex
	metaCache  metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) *cpuResourceAdvisor {
	cra := &cpuResourceAdvisor{
		conf:      conf,
		extraConf: extraConf,
		period:    conf.QoSAwarePluginConfiguration.SyncPeriod,

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

	cra.updateReservedForReclaim()

	if err := cra.initializeProvisionAssembler(); err != nil {
		klog.Errorf("[qosaware-cpu] initialize provision assembler failed: %v", err)
	}
	if err := cra.initializeHeadroomAssembler(); err != nil {
		klog.Errorf("[qosaware-cpu] initialize headroom assembler failed: %v", err)
	}

	return cra
}

func (cra *cpuResourceAdvisor) Run(ctx context.Context) {
	<-ctx.Done()
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	startTime := time.Now()
	klog.Infof("[qosaware-cpu] receive get headroom request")

	cra.mutex.RLock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer cra.mutex.RUnlock()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	if !cra.advisorUpdated {
		klog.Infof("[qosaware-cpu] skip getting headroom: advisor not updated")
		return resource.Quantity{}, nil, fmt.Errorf("advisor not updated")
	}

	if cra.headroomAssembler == nil {
		klog.Errorf("[qosaware-cpu] get headroom failed: no legal assembler")
		return resource.Quantity{}, nil, fmt.Errorf("no legal assembler")
	}

	headroom, numaHeadroom, err := cra.headroomAssembler.GetHeadroom()
	if err != nil {
		klog.Errorf("[qosaware-cpu] get headroom failed: %v", err)
	} else {
		klog.InfoS("get headroom", "headroom", headroom, "numaHeadroom", numaHeadroom)
	}

	return headroom, numaHeadroom, err
}

func (cra *cpuResourceAdvisor) UpdateAndGetAdvice(ctx context.Context) (interface{}, error) {
	startTime := time.Now()
	result, err := cra.update(ctx)
	_ = general.UpdateHealthzStateByError(cpuAdvisorHealthCheckName, err)
	general.InfoS("finished", "duration", time.Since(startTime))
	return result, err
}

// update works in a monolithic way to maintain lifecycle and triggers update actions for all regions;
// todo: re-consider whether it's efficient or we should make start individual goroutine for each region
func (cra *cpuResourceAdvisor) update(ctx context.Context) (*types.InternalCPUCalculationResult, error) {
	startTime := time.Now()
	cra.mutex.Lock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer cra.mutex.Unlock()

	result, err := cra.updateWithIsolationGuardian(ctx, true)
	if err != nil {
		if err == errIsolationSafetyCheckFailed {
			klog.Warningf("[qosaware-cpu] failed to updateWithIsolationGuardian(true): %q", err)
			return cra.updateWithIsolationGuardian(ctx, false)
		}
		return nil, err
	}
	general.InfoS("finished", "duration", time.Since(startTime))
	return result, nil
}

// If updateWithIsolationGuardian fails with isolation enabled, we should try again with isolation disabled.
// todo: we should re-design the mechanism of isolation instead of disabling this functionality
func (cra *cpuResourceAdvisor) updateWithIsolationGuardian(ctx context.Context, tryIsolation bool) (
	*types.InternalCPUCalculationResult,
	error,
) {
	startTime := time.Now()
	defer func(t time.Time) {
		elapsed := time.Since(t)
		_ = cra.emitter.StoreFloat64(metricCPUAdvisorUpdateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
		klog.Infof("[qosaware-cpu] update duration %v", elapsed)
	}(startTime)

	// sanity check: if reserve pool exists
	reservePoolInfo, ok := cra.metaCache.GetPoolInfo(commonstate.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Errorf("[qosaware-cpu] skip update: reserve pool does not exist")
		return nil, fmt.Errorf("reserve pool does not exist")
	}

	cra.updateNumasAvailableResource()
	isolationExists := cra.setIsolatedContainers(tryIsolation)

	numaResourcePackageItems, err := cra.metaServer.ResourcePackageManager.NodeResourcePackages(ctx)
	if err != nil {
		klog.Errorf("[qosaware-cpu] failed to get numa resource package items: %v", err)
		return nil, err
	}

	// assign containers to regions
	if err := cra.assignContainersToRegions(numaResourcePackageItems); err != nil {
		klog.Errorf("[qosaware-cpu] assign containers to regions failed: %q", err)
		return nil, fmt.Errorf("failed to assign containers to regions: %q", err)
	}

	cra.gcRegionMap()
	cra.updateAdvisorEssentials()
	if tryIsolation && isolationExists && !cra.checkIsolationSafety() {
		klog.Errorf("[qosaware-cpu] failed to check isolation")
		return nil, errIsolationSafetyCheckFailed
	}

	allPinnedCPUSets, err := numaResourcePackageItems.GetAllPinnedCPUSetSizeSum()
	if err != nil {
		klog.Errorf("[qosaware-cpu] failed to get all pinned cpuset size: %v", err)
		return nil, err
	}

	// run an episode of provision and headroom policy update for each region
	for _, r := range cra.regionMap {
		r.SetEssentials(types.ResourceEssentials{
			EnableReclaim:       cra.conf.GetDynamicConfiguration().EnableReclaim,
			ResourceUpperBound:  cra.getRegionMaxRequirement(r, allPinnedCPUSets),
			ResourceLowerBound:  cra.getRegionMinRequirement(r),
			ReservedForReclaim:  cra.getRegionReservedForReclaim(r),
			ReservedForAllocate: cra.getRegionReservedForAllocate(r),

			AllowSharedCoresOverlapReclaimedCores: cra.allowSharedCoresOverlapReclaimedCores,
		})

		r.TryUpdateProvision()
		r.TryUpdateHeadroom()
	}
	cra.updateRegionEntries()

	cra.advisorUpdated = true

	if klog.V(6).Enabled() {
		klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))
	}

	// assemble provision result from each region
	calculationResult, err := cra.assembleProvision()
	if err != nil {
		klog.Errorf("[qosaware-cpu] assemble provision failed: %q", err)
		return nil, fmt.Errorf("failed to assemble provisioner: %q", err)
	}
	cra.updateRegionStatus()
	cra.emitMetrics(calculationResult)

	return &calculationResult, nil
}

// setIsolatedContainers get isolation status from isolator and update into containers
func (cra *cpuResourceAdvisor) setIsolatedContainers(enableIsolated bool) bool {
	isolatedPods := sets.NewString()
	if enableIsolated {
		isolatedPods = sets.NewString(cra.isolator.GetIsolatedPods()...)
	}
	if len(isolatedPods) > 0 {
		klog.Infof("[qosaware-cpu] current isolated pod: %v", isolatedPods.List())
	}

	_ = cra.metaCache.RangeAndUpdateContainer(func(podUID string, _ string, ci *types.ContainerInfo) bool {
		ci.Isolated = false
		if isolatedPods.Has(podUID) {
			ci.Isolated = true
		}
		return true
	})
	return len(isolatedPods) > 0
}

// checkIsolationSafety returns true iff the isolated-limit-sum and share-pool-size exceed total capacity
// todo: this logic contains a lot of assumptions and should be refined in the future
func (cra *cpuResourceAdvisor) checkIsolationSafety() bool {
	shareAndIsolationPoolSize := 0
	dedicatedNonExclusivePoolSize := 0
	nonBindingNumas := cra.metaServer.CPUDetails.NUMANodes()
	for _, r := range cra.regionMap {
		if r.Type() == configapi.QoSRegionTypeShare {
			controlKnob, err := r.GetProvision()
			if err != nil {
				klog.Errorf("[qosaware-cpu] get controlKnob for %v err: %v", r.Name(), err)
				return false
			}
			shareAndIsolationPoolSize += int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
		} else if r.Type() == configapi.QoSRegionTypeIsolation {
			pods := r.GetPods()
			cra.metaCache.RangeContainer(func(podUID string, _ string, containerInfo *types.ContainerInfo) bool {
				if _, ok := pods[podUID]; ok {
					shareAndIsolationPoolSize += int(containerInfo.CPULimit)
				}
				return true
			})
		} else if r.Type() == configapi.QoSRegionTypeDedicated {
			if r.IsNumaExclusive() {
				nonBindingNumas = nonBindingNumas.Difference(r.GetBindingNumas())
			} else if r.IsNumaBinding() {
				// dedicated numa-binding non-exclusive region, calculate the pool size based on binding numas
				dedicatedNonExclusivePoolSize += int(math.Ceil(r.GetPodsRequest() / float64(r.GetBindingNumas().Size())))
			} else {
				// dedicated non-numa-binding non-exclusive region, calculate the pool size based on pods request
				dedicatedNonExclusivePoolSize += int(math.Ceil(r.GetPodsRequest()))
			}
		}
	}

	nonExclusiveSize := cra.metaServer.NUMAToCPUs.CPUSizeInNUMAs(cra.nonBindingNumas.ToSliceNoSortInt()...)
	klog.Infof("[qosaware-cpu] shareAndIsolationPoolSize %v, nonExclusiveSize %v，dedicatedNonExclusivePoolSize %v",
		shareAndIsolationPoolSize, nonExclusiveSize, dedicatedNonExclusivePoolSize)
	if shareAndIsolationPoolSize+dedicatedNonExclusivePoolSize > nonExclusiveSize {
		return false
	}
	return true
}

// assignContainersToRegions re-construct regions every time (instead of an incremental way),
// and this requires metaCache to ensure data integrity
func (cra *cpuResourceAdvisor) assignContainersToRegions(numaResourcePackages resourcepackage.NUMAResourcePackageItems) error {
	var errList []error

	// clear containers for all regions
	for _, r := range cra.regionMap {
		r.Clear()
	}

	// sync containers
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		regions, err := cra.assignToRegions(ci, numaResourcePackages)
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
		if ci.OwnerPoolName == commonstate.PoolNameDedicated {
			// dedicated pool should not exist in metaCache.poolEntries
			return true
		} else if ci.Isolated || cra.conf.IsolationForceEnablePools.Has(ci.OriginOwnerPoolName) {
			// isolated pool should not exist in metaCache.poolEntries
			return true
		} else {
			// todo currently, we may call setPoolRegions multiple time, and we
			//  depend on the reentrant of it, need to refine
			if err := cra.setPoolRegions(ci.OriginOwnerPoolName, regions); err != nil {
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
func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo, numaResourcePackages resourcepackage.NUMAResourcePackageItems) ([]region.QoSRegion, error) {
	if ci == nil {
		return nil, fmt.Errorf("container info is nil")
	}

	switch ci.QoSLevel {
	case consts.PodAnnotationQoSLevelSharedCores:
		return cra.assignShareContainerToRegions(ci, numaResourcePackages)
	case consts.PodAnnotationQoSLevelDedicatedCores:
		return cra.assignDedicatedContainerToRegions(ci, numaResourcePackages)
	default:
		return nil, nil
	}
}

func (cra *cpuResourceAdvisor) assignShareContainerToRegions(ci *types.ContainerInfo, numaResourcePackages resourcepackage.NUMAResourcePackageItems) ([]region.QoSRegion, error) {
	var pinnedCPUSetInfo *region.PinnedCPUSetInfo
	numaID := commonstate.FakedNUMAID
	if cra.conf.GenericSysAdvisorConfiguration.EnableShareCoresNumaBinding && ci.IsNumaBinding() {
		if ci.OwnerPoolName == "" {
			return nil, fmt.Errorf("empty owner pool name, %v/%v", ci.PodUID, ci.ContainerName)
		}

		if len(ci.TopologyAwareAssignments) != 1 {
			return nil, fmt.Errorf("invalid topology aware assignments of container: %s/%s", ci.PodUID, ci.ContainerName)
		}

		for key := range ci.TopologyAwareAssignments {
			numaID = key
		}

		pkgName := ci.GetResourcePackageName()
		if pkgName != "" {
			cpuSize, err := numaResourcePackages.GetPinnedCPUSetSize(numaID, pkgName)
			if err != nil {
				return nil, err
			}

			if cpuSize != nil {
				pinnedCPUSetInfo = &region.PinnedCPUSetInfo{
					PackageName: pkgName,
					NUMASize: map[int]int{
						numaID: *cpuSize,
					},
				}
			}
		}
	} else {
		// do not assign shared container to region when ramping up because its owner pool name is empty
		if ci.RampUp {
			return nil, nil
		}

		// ignore the share pods without requests info
		if ci.OwnerPoolName == "" && math.Abs(ci.CPURequest) < 1e9 {
			return nil, nil
		}

		// return error if container owner pool name is empty
		if !ci.RampUp && ci.OwnerPoolName == "" {
			return nil, fmt.Errorf("empty owner pool name, %v/%v", ci.PodUID, ci.ContainerName)
		}
	}

	// assign isolated container
	if ci.Isolated || cra.conf.IsolationForceEnablePools.Has(ci.OriginOwnerPoolName) {
		regionName := ""
		if cra.conf.IsolationNonExclusivePools.Has(ci.OriginOwnerPoolName) {
			// use origin owner pool name as region name, because all the container in this pool
			// share only one region which is non-exclusive
			regionName = ci.OriginOwnerPoolName

			// if there already exists a non-exclusive isolation region for this pod, just reuse it
			regions := cra.getPoolRegions(regionName)
			if len(regions) > 0 {
				return regions, nil
			}

			// if there already exists a region with same name as this region, just reuse it
			regions = cra.getRegionsByRegionNames(sets.NewString(regionName))
			if len(regions) > 0 {
				return regions, nil
			}
		} else {
			// if there already exists an isolation region for this pod, just reuse it
			regions, err := cra.getContainerRegions(ci, configapi.QoSRegionTypeIsolation)
			if err != nil {
				return nil, err
			} else if len(regions) > 0 {
				return regions, nil
			}
		}

		r := region.NewQoSRegionIsolation(ci, regionName, cra.conf, cra.extraConf, numaID, cra.metaCache, cra.metaServer, cra.emitter, pinnedCPUSetInfo)
		klog.Infof("create a new isolation region (%s/%s) for container %s/%s", r.OwnerPoolName(), r.Name(), ci.PodUID, ci.ContainerName)
		return []region.QoSRegion{r}, nil
	}

	// assign shared cores container. focus on pool.
	// Why OriginOwnerPoolName ?
	// Case 1: a new container
	//	OriginOwnerPoolName == OwnerPoolName
	// Case 2: put the isolation container back to share pool
	// 	OriginOwnerPoolName != OwnerPoolName:
	// Case others:
	//	OriginOwnerPoolName == OwnerPoolName
	regions := cra.getPoolRegions(ci.OriginOwnerPoolName)
	if len(regions) > 0 {
		return regions, nil
	}

	// create one region by owner pool name
	r := region.NewQoSRegionShare(ci, cra.conf, cra.extraConf, numaID, cra.metaCache, cra.metaServer, cra.emitter, pinnedCPUSetInfo)
	klog.Infof("create a new share region (%s/%s) for container %s/%s", r.OwnerPoolName(), r.Name(), ci.PodUID, ci.ContainerName)
	return []region.QoSRegion{r}, nil
}

func (cra *cpuResourceAdvisor) assignDedicatedContainerToRegions(ci *types.ContainerInfo, numaResourcePackages resourcepackage.NUMAResourcePackageItems) ([]region.QoSRegion, error) {
	// assign dedicated cores numa exclusive containers. focus on container.
	regions, err := cra.getContainerRegions(ci, configapi.QoSRegionTypeDedicated)
	if err != nil {
		return nil, err
	} else if len(regions) > 0 {
		return regions, nil
	}

	var pinnedCPUSetInfo *region.PinnedCPUSetInfo
	pkgName := ci.GetResourcePackageName()
	if ci.IsNumaBinding() {
		// create regions by numa node
		for numaID := range ci.TopologyAwareAssignments {
			if pkgName != "" {
				cpuSize, err := numaResourcePackages.GetPinnedCPUSetSize(numaID, pkgName)
				if err != nil {
					return nil, err
				}

				if cpuSize != nil {
					pinnedCPUSetInfo = &region.PinnedCPUSetInfo{
						PackageName: pkgName,
						NUMASize: map[int]int{
							numaID: *cpuSize,
						},
					}
				}
			}
			r := region.NewQoSRegionDedicated(ci, cra.conf, numaID, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter, pinnedCPUSetInfo)
			regions = append(regions, r)
		}
	} else {
		r := region.NewQoSRegionDedicated(ci, cra.conf, commonstate.FakedNUMAID, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter, pinnedCPUSetInfo)
		regions = append(regions, r)
	}
	return regions, nil
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
	cra.allowSharedCoresOverlapReclaimedCores = cra.conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores

	// update non-binding numas
	for _, r := range cra.regionMap {
		if !r.IsNumaBinding() {
			continue
		}
		// ignore isolation region
		if r.Type() == configapi.QoSRegionTypeDedicated || r.Type() == configapi.QoSRegionTypeShare {
			cra.nonBindingNumas = cra.nonBindingNumas.Difference(r.GetBindingNumas())
		}
	}

	// reset region quantity
	for _, numaID := range cra.metaServer.CPUDetails.NUMANodes().ToSliceInt() {
		cra.numRegionsPerNuma[numaID] = 0
	}

	for _, r := range cra.regionMap {
		// set binding numas for non numa binding regions
		if !r.IsNumaBinding() && r.Type() == configapi.QoSRegionTypeShare {
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
func (cra *cpuResourceAdvisor) assembleProvision() (types.InternalCPUCalculationResult, error) {
	if cra.provisionAssembler == nil {
		return types.InternalCPUCalculationResult{}, fmt.Errorf("no legal provision assembler")
	}

	return cra.provisionAssembler.AssembleProvision()
}

func (cra *cpuResourceAdvisor) emitMetrics(calculationResult types.InternalCPUCalculationResult) {
	// emit region indicator related metrics
	for _, r := range cra.regionMap {
		tags := region.GetRegionBasicMetricTags(r)

		_ = cra.emitter.StoreInt64(metricRegionStatus, int64(cra.period.Seconds()), metrics.MetricTypeNameCount, tags...)

		indicators := r.GetControlEssentials().Indicators
		for indicatorName, indicator := range indicators {
			_ = cra.emitter.StoreFloat64(metricRegionIndicatorTargetPrefix+indicatorName, indicator.Target, metrics.MetricTypeNameRaw, tags...)
			_ = cra.emitter.StoreFloat64(metricRegionIndicatorCurrentPrefix+indicatorName, indicator.Current, metrics.MetricTypeNameRaw, tags...)
			_ = cra.emitter.StoreFloat64(metricRegionIndicatorErrorPrefix+indicatorName, indicator.Current-indicator.Target, metrics.MetricTypeNameRaw, tags...)
		}
	}

	// emit calculated pool sizes
	for poolName, poolEntry := range calculationResult.PoolEntries {
		for numaID, cpuResource := range poolEntry {
			_ = cra.emitter.StoreInt64(metricCPUAdvisorPoolSize, int64(cpuResource.Size), metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "name", Val: poolName},
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
				metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(poolName)},
				metrics.MetricTag{Key: "overlap", Val: "none"})
			_ = cra.emitter.StoreFloat64(metricCPUAdvisorPoolQuota, cpuResource.Quota, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "name", Val: poolName},
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
				metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(poolName)})
		}
	}

	for poolName, overlapInfo := range calculationResult.PoolOverlapInfo {
		for numaID, poolOverlapInfo := range overlapInfo {
			for target, overlap := range poolOverlapInfo {
				_ = cra.emitter.StoreInt64(metricCPUAdvisorPoolSize, int64(overlap), metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "name", Val: poolName},
					metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
					metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(poolName)},
					metrics.MetricTag{Key: "overlap", Val: target})
			}
		}
	}

	for poolName, overlapInfo := range calculationResult.PoolOverlapPodContainerInfo {
		for numaID, poolOverlapInfo := range overlapInfo {
			for podUID, overlapContainer := range poolOverlapInfo {
				// todo: current only emit first container overlap, because other containers' overlap is same now
				for _, overlap := range overlapContainer {
					_ = cra.emitter.StoreInt64(metricCPUAdvisorPoolSize, int64(overlap), metrics.MetricTypeNameRaw,
						metrics.MetricTag{Key: "name", Val: poolName},
						metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
						metrics.MetricTag{Key: "pool_type", Val: commonstate.GetPoolType(poolName)},
						metrics.MetricTag{Key: "overlap", Val: podUID})
					break
				}
			}
		}
	}
}
