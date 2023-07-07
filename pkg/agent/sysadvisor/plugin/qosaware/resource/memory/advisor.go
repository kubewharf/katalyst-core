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

package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/headroompolicy"
	memadvisorplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func init() {
	headroompolicy.RegisterInitializer(types.MemoryHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)

	memadvisorplugin.RegisterInitializer(memadvisorplugin.CacheReaper, memadvisorplugin.NewCacheReaper)
	memadvisorplugin.RegisterInitializer(memadvisorplugin.MemoryGuard, memadvisorplugin.NewMemoryGuard)
	memadvisorplugin.RegisterInitializer(memadvisorplugin.MemsetBinder, memadvisorplugin.NewMemsetBinder)
}

const (
	startUpPeriod    = 30 * time.Second
	nonExistNumaID   = -1
	scaleDenominator = 10000
)

// memoryResourceAdvisor updates memory headroom for reclaimed resource
type memoryResourceAdvisor struct {
	conf            *config.Configuration
	startTime       time.Time
	headroomPolices []headroompolicy.HeadroomPolicy
	plugins         []memadvisorplugin.MemoryAdvisorPlugin
	mutex           sync.RWMutex

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter

	sendChan chan types.InternalMemoryCalculationResult
}

// NewMemoryResourceAdvisor returns a memoryResourceAdvisor instance
func NewMemoryResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *memoryResourceAdvisor {
	ra := &memoryResourceAdvisor{
		startTime: time.Now(),

		headroomPolices: make([]headroompolicy.HeadroomPolicy, 0),

		conf:       conf,
		metaReader: metaCache,
		metaServer: metaServer,
		emitter:    emitter,
		sendChan:   make(chan types.InternalMemoryCalculationResult, 1),
	}

	headroomPolicyInitializers := headroompolicy.GetRegisteredInitializers()
	for _, headroomPolicyName := range conf.MemoryHeadroomPolicies {
		initFunc, ok := headroomPolicyInitializers[headroomPolicyName]
		if !ok {
			klog.Errorf("failed to find registered initializer %v", headroomPolicyName)
			continue
		}
		ra.headroomPolices = append(ra.headroomPolices, initFunc(conf, extraConf, metaCache, metaServer, emitter))
	}

	memoryAdvisorPluginInitializers := memadvisorplugin.GetRegisteredInitializers()
	for _, memadvisorPluginName := range conf.MemoryAdvisorPlugins {
		initFunc, ok := memoryAdvisorPluginInitializers[memadvisorPluginName]
		if !ok {
			klog.Errorf("failed to find registered initializer %v", memadvisorPluginName)
			continue
		}
		ra.plugins = append(ra.plugins, initFunc(conf, extraConf, metaCache, metaServer, emitter))
	}

	return ra
}

func (ra *memoryResourceAdvisor) Run(ctx context.Context) {
	period := ra.conf.SysAdvisorPluginsConfiguration.QoSAwarePluginConfiguration.SyncPeriod

	go wait.Until(ra.update, period, ctx.Done())
}

func (ra *memoryResourceAdvisor) GetChannels() (interface{}, interface{}) {
	return nil, ra.sendChan
}

func (ra *memoryResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	ra.mutex.RLock()
	defer ra.mutex.RUnlock()

	for _, headroomPolicy := range ra.headroomPolices {
		headroom, err := headroomPolicy.GetHeadroom()
		if err != nil {
			klog.Warningf("[qosaware-memory] get headroom with error: %v", err)
			continue
		}
		return headroom, nil
	}

	return resource.Quantity{}, fmt.Errorf("failed to get valid headroom")
}

func (ra *memoryResourceAdvisor) sendAdvices() {
	// send to server
	result := types.InternalMemoryCalculationResult{TimeStamp: time.Now()}
	for _, plugin := range ra.plugins {
		advices := plugin.GetAdvices()
		result.ContainerEntries = append(result.ContainerEntries, advices.ContainerEntries...)
		result.ExtraEntries = append(result.ExtraEntries, advices.ExtraEntries...)
	}

	select {
	case ra.sendChan <- result:
		general.Infof("notify memory server: %+v", result)
	default:
		general.Errorf("channel is full")
	}
}

func (ra *memoryResourceAdvisor) update() {
	ra.mutex.Lock()
	defer ra.mutex.Unlock()

	// Skip update during startup
	if time.Now().Before(ra.startTime.Add(startUpPeriod)) {
		klog.Infof("[qosaware-memory] skip update: starting up")
		return
	}

	// Check if essential pool info exists. Skip update if not in which case sysadvisor
	// is ignorant of pools and containers
	reservePoolInfo, ok := ra.metaReader.GetPoolInfo(state.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Warningf("[qosaware-memory] skip update: reserve pool not exist")
		return
	}

	reservedForAllocate := ra.conf.GetDynamicConfiguration().
		ReservedResourceForAllocate[v1.ResourceMemory]

	for _, headroomPolicy := range ra.headroomPolices {
		// capacity and reserved can both be adjusted dynamically during running process
		headroomPolicy.SetEssentials(types.ResourceEssentials{
			EnableReclaim:       ra.conf.GetDynamicConfiguration().EnableReclaim,
			ResourceUpperBound:  float64(ra.metaServer.MemoryCapacity),
			ReservedForAllocate: reservedForAllocate.AsApproximateFloat64(),
		})

		if err := headroomPolicy.Update(); err != nil {
			klog.Errorf("[qosaware-memory] update headroom policy failed: %v", err)
		}
	}

	nodeCondition, err := ra.detectNodePressureCondition()
	if err != nil {
		general.Errorf("detect node memory pressure err %v", err)
		return
	}
	NUMAConditions, err := ra.detectNUMAPressureConditions()
	if err != nil {
		general.Errorf("detect NUMA pressures err %v", err)
		return
	}

	memoryPressureStatus := types.MemoryPressureStatus{
		NodeCondition:  nodeCondition,
		NUMAConditions: NUMAConditions,
	}

	for _, plugin := range ra.plugins {
		plugin.Reconcile(&memoryPressureStatus)
	}

	ra.sendAdvices()
}

func (ra *memoryResourceAdvisor) detectNUMAPressureConditions() (map[int]*types.MemoryPressureCondition, error) {
	pressureConditions := make(map[int]*types.MemoryPressureCondition)
	for _, numaID := range ra.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		pressureCondition, err := ra.detectNUMAPressure(numaID)
		if err != nil {
			general.Errorf("detect NUMA(%v) pressure err %v", numaID, err)
			return nil, err
		}
		pressureConditions[numaID] = pressureCondition
	}
	return pressureConditions, nil
}

func (ra *memoryResourceAdvisor) detectNUMAPressure(numaID int) (*types.MemoryPressureCondition, error) {
	free, total, scaleFactor, err := helper.GetWatermarkMetrics(ra.metaServer.MetricsFetcher, ra.emitter, numaID)
	if err != nil {
		general.Errorf("failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
		return nil, err
	}

	general.Infof("numa %v metrics, free: %+v, total: %+v, scaleFactor: %+v", numaID,
		resource.NewQuantity(int64(free), resource.BinarySI).String(),
		resource.NewQuantity(int64(total), resource.BinarySI).String(), scaleFactor)

	targetReclaimed := resource.NewQuantity(0, resource.BinarySI)

	pressureState := types.MemoryPressureNoRisk
	if free < 2*total*scaleFactor/scaleDenominator {
		pressureState = types.MemoryPressureDropCache
		targetReclaimed.Set(int64(4*total*scaleFactor/scaleDenominator - free))
	}

	return &types.MemoryPressureCondition{
		TargetReclaimed: targetReclaimed,
		State:           pressureState,
	}, nil
}

func (ra *memoryResourceAdvisor) detectNodePressureCondition() (*types.MemoryPressureCondition, error) {
	free, total, scaleFactor, err := helper.GetWatermarkMetrics(ra.metaServer.MetricsFetcher, ra.emitter, nonExistNumaID)
	if err != nil {
		general.Errorf("failed to getWatermarkMetrics for system, err: %v", err)
		return nil, err
	}

	general.Infof("system watermark metrics, free: %+v, total: %+v, scaleFactor: %+v",
		resource.NewQuantity(int64(free), resource.BinarySI).String(),
		resource.NewQuantity(int64(total), resource.BinarySI).String(), scaleFactor)

	targetReclaimed := resource.NewQuantity(0, resource.BinarySI)

	pressureState := types.MemoryPressureNoRisk
	if free < 2*total*scaleFactor/scaleDenominator {
		pressureState = types.MemoryPressureDropCache
		targetReclaimed.Set(int64(4*total*scaleFactor/scaleDenominator - free))
	} else if free < 4*total*scaleFactor/scaleDenominator {
		pressureState = types.MemoryPressureTuneMemCg
		targetReclaimed.Set(int64(4*total*scaleFactor/10000 - free))
	}
	return &types.MemoryPressureCondition{
		TargetReclaimed: targetReclaimed,
		State:           pressureState,
	}, nil
}
