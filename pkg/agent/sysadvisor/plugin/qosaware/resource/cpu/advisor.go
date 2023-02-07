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

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	cpuResourceAdvisorName string = "cpu-resource-advisor"

	minSharePoolCPURequirement   int           = 4
	minReclaimPoolCPURequirement int           = 4
	maxRampUpStep                float64       = 10
	maxRampDownStep              float64       = 2
	minRampDownPeriod            time.Duration = 30 * time.Second

	startUpPeriod time.Duration = 30 * time.Second
)

// CPUProvision is the internal data structure for pushing minimal provision result to cpu server.
// todo: update this when switching to multi qos region.
type CPUProvision struct {
	// [poolName][numaId]cores
	PoolSizeMap map[string]map[int]resource.Quantity
}

// cpuResourceAdvisor is the entrance of updating cpu resource provision advice for all possible
// regions(pools). For each region(pool) maintained, there will be corresponding cpu calculators
// (if needed) with smart algorithm policy to give realtime resource provision advice. Temporary
// only support updating for share pool resource, but it's easy to extend to multi regions(pools).
// todo: support multi qos region.
type cpuResourceAdvisor struct {
	name           string
	cpuLimitSystem int
	startTime      time.Time
	isReady        bool
	isInitialized  bool

	calculator *cpuCalculator
	advisorCh  chan CPUProvision

	metaCache *metacache.MetaCache
	emitter   metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance with cpu calculator
func NewCPUResourceAdvisor(conf *config.Configuration, metaCache *metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) (*cpuResourceAdvisor, error) {
	cra := &cpuResourceAdvisor{
		name:           cpuResourceAdvisorName,
		cpuLimitSystem: metaServer.NumCPUs,
		startTime:      time.Now(),
		isReady:        false,
		isInitialized:  false,
		advisorCh:      make(chan CPUProvision),
		metaCache:      metaCache,
		emitter:        emitter,
	}

	// New cpu calculator instance
	calculator, err := newCPUCalculator(conf, metaCache, maxRampUpStep, maxRampDownStep, minRampDownPeriod)
	if err != nil {
		return nil, err
	}

	cra.calculator = calculator
	return cra, nil
}

func (cra *cpuResourceAdvisor) Name() string {
	return cra.name
}

func (cra *cpuResourceAdvisor) Update() {
	// Skip update during startup
	if time.Now().Before(cra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-cpu] starting up")
		return
	}

	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	reservePoolSize, err := cra.getPoolSize(state.PoolNameReserve)
	if err != nil {
		klog.Warningf("[qosaware-cpu] skip update. %v", err)
		return
	}

	// Update min/max cpu requirement settings
	cra.calculator.setMinCPURequirement(minSharePoolCPURequirement)
	cra.calculator.setMaxCPURequirement(cra.cpuLimitSystem - reservePoolSize - minReclaimPoolCPURequirement)
	cra.calculator.setTotalCPURequirement(cra.cpuLimitSystem - reservePoolSize)

	// Set initial cpu requirement
	if !cra.isInitialized {
		if sharePoolSize, err := cra.getPoolSize(state.PoolNameShare); err == nil {
			cra.calculator.setLastestCPURequirement(sharePoolSize)
			klog.Infof("[qosaware-cpu] set initial cpu requirement %v", sharePoolSize)
		}
		cra.isInitialized = true
	}

	// Calculate
	cra.calculator.update()
	sharePoolCPURequirement := cra.calculator.getCPURequirement()
	reclaimPoolCPURequirement := cra.calculator.getCPURequirementReclaimed()

	// Notify cpu server
	cpuProvision := CPUProvision{
		// Must make sure pool names from cpu provision following qrm definition!
		PoolSizeMap: map[string]map[int]resource.Quantity{
			state.PoolNameReserve: {
				-1: *resource.NewQuantity(int64(reservePoolSize), resource.DecimalSI),
			},
			state.PoolNameShare: {
				-1: *resource.NewQuantity(int64(sharePoolCPURequirement), resource.DecimalSI),
			},
			state.PoolNameReclaim: {
				-1: *resource.NewQuantity(int64(reclaimPoolCPURequirement), resource.DecimalSI),
			},
		},
	}
	cra.advisorCh <- cpuProvision
	cra.isReady = true
	klog.Infof("[qosaware-cpu] update channel: %+v", cpuProvision)
}

func (cra *cpuResourceAdvisor) GetChannel() interface{} {
	return cra.advisorCh
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	if !cra.isReady {
		return resource.Quantity{}, fmt.Errorf("not ready")
	}

	reclaimPoolCPURequirement := cra.calculator.getCPURequirementReclaimed()
	return resource.MustParse(fmt.Sprintf("%d", reclaimPoolCPURequirement)), nil
}

func (cra *cpuResourceAdvisor) getPoolSize(poolName string) (int, error) {
	pi, ok := cra.metaCache.GetPoolInfo(poolName)
	if !ok {
		return 0, fmt.Errorf("%v pool not exist", poolName)
	}

	poolSize := util.CountCPUAssignmentCPUs(pi.TopologyAwareAssignments)
	klog.Infof("[qosaware-cpu] %v pool size %v", poolName, poolSize)

	return poolSize, nil
}
