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
	policy              types.CPUProvisionPolicyName
	startTime           time.Time
	cpuLimitSystem      int
	reservedForAllocate int

	regionMap          map[string]region.QoSRegion              // map[regionName]region
	containerRegionMap map[string]map[string][]region.QoSRegion // map[podUID][containerName]regions
	poolRegionMap      map[string][]region.QoSRegion            // map[poolName]regions
	advisorCh          chan CPUProvision

	conf      *config.Configuration
	metaCache *metacache.MetaCache
	emitter   metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, metaCache *metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) (*cpuResourceAdvisor, error) {
	cra := &cpuResourceAdvisor{
		name:           cpuResourceAdvisorName,
		policy:         types.CPUProvisionPolicyName(conf.CPUAdvisorConfiguration.CPUProvisionPolicy),
		startTime:      time.Now(),
		cpuLimitSystem: metaServer.NumCPUs,

		regionMap:          make(map[string]region.QoSRegion),
		containerRegionMap: make(map[string]map[string][]region.QoSRegion),
		poolRegionMap:      make(map[string][]region.QoSRegion),
		advisorCh:          make(chan CPUProvision),

		conf:      conf,
		metaCache: metaCache,
		emitter:   emitter,
	}

	// todo: support dynamic reserved resource
	reserved := conf.ReclaimedResourceConfiguration.ReservedResourceForAllocate[v1.ResourceCPU]
	cra.reservedForAllocate = int(reserved.Value())

	return cra, nil
}

func (cra *cpuResourceAdvisor) Name() string {
	return cra.name
}

func (cra *cpuResourceAdvisor) Update() {
	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	reservePoolSize, ok := cra.metaCache.GetPoolSize(state.PoolNameReserve)
	if !ok {
		klog.Warningf("[qosaware-cpu] skip update: reserve pool not exist")
		return
	}

	// Assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		klog.Errorf("[qosaware-cpu] skip update: %v", err)
		return
	}
	klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))

	// Run an episode of policy update for each region
	for _, r := range cra.regionMap {
		// Set essentials for regions
		if r.Type() == region.QoSRegionTypeShare {
			r.SetCPULimit(cra.cpuLimitSystem)
			r.SetReservedForAllocate(cra.reservedForAllocate)
			r.SetReservePoolSize(reservePoolSize)
		} else if r.Type() == region.QoSRegionTypeDedicatedNuma {
			// todo
		}

		r.TryUpdateControlKnob()
	}

	// Skip notifying cpu server during startup
	if time.Now().Before(cra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-cpu] skip notifying cpu server: starting up")
		return
	}

	// Generate internal calculation result.
	// Must make sure pool names from cpu provision following qrm definition;
	// numa ID set as -1 means no numa-preference is needed.
	provision := CPUProvision{
		PoolSizeMap: map[string]map[int]resource.Quantity{},
	}

	for _, r := range cra.regionMap {
		c, err := r.GetControlKnobUpdated()
		if err != nil {
			klog.Errorf("[qosaware-cpu] skip notifying cpu server: %v", err)
			return
		}

		if r.Type() == region.QoSRegionTypeShare {
			sharePoolSize := c[types.ControlKnobCPUSetSize].Value
			provision.PoolSizeMap[state.PoolNameShare] = make(map[int]resource.Quantity)
			provision.PoolSizeMap[state.PoolNameShare][-1] = *resource.NewQuantity(int64(sharePoolSize), resource.DecimalSI)
		} else if r.Type() == region.QoSRegionTypeDedicatedNuma {
			// todo
		}
	}

	// Notify cpu server
	cra.advisorCh <- provision
	klog.Infof("[qosaware-cpu] notify cpu server: %+v", provision)
}

func (cra *cpuResourceAdvisor) GetChannel() interface{} {
	return cra.advisorCh
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	return resource.Quantity{}, fmt.Errorf("not supported")
}

func (cra *cpuResourceAdvisor) assignContainersToRegions() error {
	var errList []error

	// Clear containers for all regions
	for _, region := range cra.regionMap {
		region.Clear()
	}

	// Sync containers
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		regions := cra.assignToRegions(ci)
		if regions == nil {
			klog.Warningf("[qosaware-cpu] assign %v/%v to nil region list", podUID, containerName)
			return true
		}

		for _, r := range regions {
			r.AddContainer(ci.PodUID, ci.ContainerName)
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

func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo) []region.QoSRegion {
	if ci.QoSLevel == consts.PodAnnotationQoSLevelSharedCores {
		// Shared
		if regions, ok := cra.getPoolRegions(ci.OwnerPoolName); ok {
			return regions
		}
		name := string(region.QoSRegionTypeShare) + string(uuid.NewUUID())
		r := region.NewQoSRegionShare(name, ci.OwnerPoolName, region.QoSRegionTypeShare, cra.policy, cra.conf, cra.metaCache, cra.emitter)
		return []region.QoSRegion{r}
	} else if ci.IsNumaBinding() {
		// dedicated cores with numa binding
		if regions, ok := cra.getContainerRegions(ci); ok {
			return regions
		}
		regions := make([]region.QoSRegion, 0)
		for numaID := range ci.TopologyAwareAssignments {
			name := string(region.QoSRegionTypeDedicated) + fmt.Sprintf("%d", numaID)
			r, ok := cra.regionMap[name]
			if !ok {
				r = region.NewQoSRegionDedicatedNuma(name, ci.OwnerPoolName, region.QoSRegionTypeDedicatedNuma, cra.policy, cra.metaCache, cra.emitter)
			}
			regions = append(regions, r)
		}
		return regions
	}

	return nil
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
