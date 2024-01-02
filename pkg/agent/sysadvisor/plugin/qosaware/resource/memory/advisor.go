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
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/headroompolicy"
	memadvisorplugin "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory/plugin/provisioner"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func init() {
	headroompolicy.RegisterInitializer(types.MemoryHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.MemoryHeadroomPolicyNUMAAware, headroompolicy.NewPolicyNUMAAware)

	memadvisorplugin.RegisterInitializer(memadvisorplugin.CacheReaper, memadvisorplugin.NewCacheReaper)
	memadvisorplugin.RegisterInitializer(memadvisorplugin.MemoryGuard, memadvisorplugin.NewMemoryGuard)
	memadvisorplugin.RegisterInitializer(memadvisorplugin.MemsetBinder, memadvisorplugin.NewMemsetBinder)

	memadvisorplugin.RegisterInitializer(provisioner.MemoryProvisioner, provisioner.NewMemoryProvisioner)
}

const (
	nonExistNumaID   = -1
	scaleDenominator = 10000

	metricsNameNodeMemoryPressureState = "node_memory_pressure_state"
	metricsNameNodeMemoryReclaimTarget = "node_memory_reclaim_target"
	metricsNameNumaMemoryPressureState = "numa_memory_pressure_state"
	metricsNameNumaMemoryReclaimTarget = "numa_memory_reclaim_target"
	metricNameMemoryGetHeadroomFailed  = "get_memory_headroom_failed"

	metricsTagKeyNumaID    = "numa_id"
	metricTagKeyPolicyName = "policy_name"

	// multiply the scale by the criticalWaterMark to get the safe watermark
	criticalWaterMarkScaleFactor = 2
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

	recvCh   chan types.TriggerInfo
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
		recvCh:     make(chan types.TriggerInfo, 1),
		sendChan:   make(chan types.InternalMemoryCalculationResult, 1),
	}

	headroomPolicyInitializers := headroompolicy.GetRegisteredInitializers()
	for _, headroomPolicyName := range conf.MemoryHeadroomPolicies {
		initFunc, ok := headroomPolicyInitializers[headroomPolicyName]
		if !ok {
			klog.Errorf("failed to find registered initializer %v", headroomPolicyName)
			continue
		}
		policy := initFunc(conf, extraConf, metaCache, metaServer, emitter)
		general.InfoS("add new memory headroom policy", "policyName", policy.Name())

		ra.headroomPolices = append(ra.headroomPolices, policy)
	}

	memoryAdvisorPluginInitializers := memadvisorplugin.GetRegisteredInitializers()
	for _, memadvisorPluginName := range conf.MemoryAdvisorPlugins {
		initFunc, ok := memoryAdvisorPluginInitializers[memadvisorPluginName]
		if !ok {
			klog.Errorf("failed to find registered initializer %v", memadvisorPluginName)
			continue
		}
		general.InfoS("add new memory advisor policy", "policyName", memadvisorPluginName)
		ra.plugins = append(ra.plugins, initFunc(conf, extraConf, metaCache, metaServer, emitter))
	}

	return ra
}

func (ra *memoryResourceAdvisor) Run(ctx context.Context) {
	period := ra.conf.SysAdvisorPluginsConfiguration.QoSAwarePluginConfiguration.SyncPeriod

	general.InfoS("wait to list containers")
	<-ra.recvCh
	general.InfoS("list containers successfully")

	go wait.Until(ra.update, period, ctx.Done())
}

func (ra *memoryResourceAdvisor) GetChannels() (interface{}, interface{}) {
	return ra.recvCh, ra.sendChan
}

func (ra *memoryResourceAdvisor) GetHeadroom() (resource.Quantity, error) {
	ra.mutex.RLock()
	defer ra.mutex.RUnlock()

	for _, headroomPolicy := range ra.headroomPolices {
		headroom, err := headroomPolicy.GetHeadroom()
		if err != nil {
			klog.ErrorS(err, "get headroom failed", "headroomPolicy", headroomPolicy.Name())
			_ = ra.emitter.StoreInt64(metricNameMemoryGetHeadroomFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: metricTagKeyPolicyName, Val: string(headroomPolicy.Name())})
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

	if !ra.metaReader.HasSynced() {
		general.InfoS("metaReader has not synced, skip updating")
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
			general.ErrorS(err, "[qosaware-memory] update headroom policy failed", "headroomPolicy", headroomPolicy.Name())
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
		_ = plugin.Reconcile(&memoryPressureStatus)
	}

	ra.sendAdvices()
}

func (ra *memoryResourceAdvisor) detectNUMAPressureConditions() (map[int]*types.MemoryPressureCondition, error) {
	pressureConditions := make(map[int]*types.MemoryPressureCondition)

	for _, numaID := range ra.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		pressureCondition, err := ra.detectNUMAPressure(numaID)
		if err != nil {
			general.ErrorS(err, "detect NUMA pressure failed", "numaID", numaID)
			return nil, err
		}
		pressureConditions[numaID] = pressureCondition
	}
	return pressureConditions, nil
}

func (ra *memoryResourceAdvisor) detectNUMAPressure(numaID int) (*types.MemoryPressureCondition, error) {
	free, total, scaleFactor, err := helper.GetWatermarkMetrics(ra.metaServer.MetricsFetcher, ra.emitter, numaID)
	if err != nil && metric.IsMetricDataExpired(err) {
		general.Errorf("failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
		return nil, err
	}

	targetReclaimed := resource.NewQuantity(0, resource.BinarySI)
	pressureState := types.MemoryPressureNoRisk

	criticalWatermark := general.MaxFloat64(float64(ra.conf.MinCriticalWatermark), 2*total*scaleFactor/scaleDenominator)
	if free < criticalWatermark {
		pressureState = types.MemoryPressureDropCache
		targetReclaimed.Set(int64(criticalWaterMarkScaleFactor*criticalWatermark - free))
	} else if free < criticalWaterMarkScaleFactor*criticalWatermark {
		pressureState = types.MemoryPressureTuneMemCg
	}

	general.InfoS("NUMA memory metrics",
		"numaID", numaID,
		"total", general.FormatMemoryQuantity(total),
		"free", general.FormatMemoryQuantity(free),
		"scaleFactor", scaleFactor,
		"criticalWatermark", general.FormatMemoryQuantity(criticalWatermark),
		"targetReclaimed", targetReclaimed.String(),
		"pressureState", pressureState)

	_ = ra.emitter.StoreInt64(metricsNameNumaMemoryPressureState, int64(pressureState), metrics.MetricTypeNameRaw, metrics.MetricTag{Key: metricsTagKeyNumaID, Val: strconv.Itoa(numaID)})
	_ = ra.emitter.StoreInt64(metricsNameNumaMemoryReclaimTarget, targetReclaimed.Value(), metrics.MetricTypeNameRaw, metrics.MetricTag{Key: metricsTagKeyNumaID, Val: strconv.Itoa(numaID)})

	return &types.MemoryPressureCondition{
		TargetReclaimed: targetReclaimed,
		State:           pressureState,
	}, nil
}

func (ra *memoryResourceAdvisor) detectNodePressureCondition() (*types.MemoryPressureCondition, error) {
	free, total, scaleFactor, err := helper.GetWatermarkMetrics(ra.metaServer.MetricsFetcher, ra.emitter, nonExistNumaID)
	if err != nil && !metric.IsMetricDataExpired(err) {
		general.Errorf("failed to getWatermarkMetrics for system, err: %v", err)
		return nil, err
	}

	criticalWatermark := general.MaxFloat64(float64(ra.conf.MinCriticalWatermark*int64(ra.metaServer.NumNUMANodes)), 2*total*scaleFactor/scaleDenominator)
	targetReclaimed := resource.NewQuantity(0, resource.BinarySI)

	pressureState := types.MemoryPressureNoRisk
	if free < criticalWatermark {
		pressureState = types.MemoryPressureDropCache
		targetReclaimed.Set(int64(criticalWaterMarkScaleFactor*criticalWatermark - free))
	} else if free < criticalWaterMarkScaleFactor*criticalWatermark {
		pressureState = types.MemoryPressureTuneMemCg
		targetReclaimed.Set(int64(criticalWaterMarkScaleFactor*criticalWatermark - free))
	}

	general.InfoS("system watermark metrics",
		"free", general.FormatMemoryQuantity(free),
		"total", general.FormatMemoryQuantity(total),
		"criticalWatermark", general.FormatMemoryQuantity(criticalWatermark),
		"targetReclaimed", targetReclaimed.Value(),
		"scaleFactor", scaleFactor)

	_ = ra.emitter.StoreInt64(metricsNameNodeMemoryPressureState, int64(pressureState), metrics.MetricTypeNameRaw)
	_ = ra.emitter.StoreInt64(metricsNameNodeMemoryReclaimTarget, targetReclaimed.Value(), metrics.MetricTypeNameRaw)

	return &types.MemoryPressureCondition{
		TargetReclaimed: targetReclaimed,
		State:           pressureState,
	}, nil
}
