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
	"k8s.io/apimachinery/pkg/util/errors"
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
	memadvisorplugin.RegisterInitializer(memadvisorplugin.NumaMemoryBalancer, memadvisorplugin.NewMemoryBalancer)
	memadvisorplugin.RegisterInitializer(memadvisorplugin.TransparentMemoryOffloading, memadvisorplugin.NewTransparentMemoryOffloading)
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

	memoryAdvisorHealthCheckName  = "memory_advisor_update"
	healthCheckTolerationDuration = 30 * time.Second
)

// memoryResourceAdvisor updates memory headroom for reclaimed resource
type memoryResourceAdvisor struct {
	conf            *config.Configuration
	headroomPolices []headroompolicy.HeadroomPolicy
	plugins         []memadvisorplugin.MemoryAdvisorPlugin
	mutex           sync.RWMutex

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewMemoryResourceAdvisor returns a memoryResourceAdvisor instance
func NewMemoryResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) *memoryResourceAdvisor {
	ra := &memoryResourceAdvisor{
		headroomPolices: make([]headroompolicy.HeadroomPolicy, 0),

		conf:       conf,
		metaReader: metaCache,
		metaServer: metaServer,
		emitter:    emitter,
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
		general.InfoS("add new memory advisor plugin", "pluginName", memadvisorPluginName)
		ra.plugins = append(ra.plugins, initFunc(conf, extraConf, metaCache, metaServer, emitter))
	}

	return ra
}

func RegisterMemoryAdvisorHealthCheck() {
	general.Infof("register memory advisor health check")
	general.RegisterHeartbeatCheck(memoryAdvisorHealthCheckName, healthCheckTolerationDuration, general.HealthzCheckStateNotReady, healthCheckTolerationDuration)
}

func (ra *memoryResourceAdvisor) Run(ctx context.Context) {
	<-ctx.Done()
}

func (ra *memoryResourceAdvisor) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	startTime := time.Now()
	ra.mutex.RLock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer ra.mutex.RUnlock()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	for _, headroomPolicy := range ra.headroomPolices {
		headroom, numaHeadroom, err := headroomPolicy.GetHeadroom()
		if err != nil {
			klog.ErrorS(err, "get headroom failed", "headroomPolicy", headroomPolicy.Name())
			_ = ra.emitter.StoreInt64(metricNameMemoryGetHeadroomFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: metricTagKeyPolicyName, Val: string(headroomPolicy.Name())})
			continue
		}
		return headroom, numaHeadroom, nil
	}

	return resource.Quantity{}, nil, fmt.Errorf("failed to get valid headroom")
}

func (ra *memoryResourceAdvisor) UpdateAndGetAdvice(ctx context.Context) (interface{}, error) {
	startTime := time.Now()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()
	result, err := ra.update()
	_ = general.UpdateHealthzStateByError(memoryAdvisorHealthCheckName, err)
	if result != nil {
		return result, nil
	} else {
		return nil, err
	}
}

// update updates memory headroom and plugin advices.
// If the returned result is not nil, it is valid even if an error is returned.
func (ra *memoryResourceAdvisor) update() (*types.InternalMemoryCalculationResult, error) {
	startTime := time.Now()
	ra.mutex.Lock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer ra.mutex.Unlock()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	if !ra.metaReader.HasSynced() {
		general.InfoS("metaReader has not synced, skip updating")
		return nil, fmt.Errorf("meta reader has not synced")
	}

	reservedForAllocate := ra.conf.GetDynamicConfiguration().
		ReservedResourceForAllocate[v1.ResourceMemory]

	var nonFatalErrors []error
	for _, headroomPolicy := range ra.headroomPolices {
		// capacity and reserved can both be adjusted dynamically during running process
		headroomPolicy.SetEssentials(types.ResourceEssentials{
			EnableReclaim:       ra.conf.GetDynamicConfiguration().EnableReclaim,
			ResourceUpperBound:  float64(ra.metaServer.MemoryCapacity),
			ReservedForAllocate: reservedForAllocate.AsApproximateFloat64(),
		})

		if err := headroomPolicy.Update(); err != nil {
			general.ErrorS(err, "update headroom policy failed", "headroomPolicy", headroomPolicy.Name())
			nonFatalErrors = append(nonFatalErrors, fmt.Errorf("update headroom policy failed for %s: %v", headroomPolicy.Name(), err))
		}
	}

	nodeCondition, err := ra.detectNodePressureCondition()
	if err != nil {
		general.Errorf("detect node memory pressure err %v", err)
		return nil, fmt.Errorf("failed to detect node memory pressure: %q", err)
	}
	NUMAConditions, err := ra.detectNUMAPressureConditions()
	if err != nil {
		general.Errorf("detect NUMA pressures err %v", err)
		return nil, fmt.Errorf("failed to detete NUMA pressure: %q", err)
	}

	memoryPressureStatus := types.MemoryPressureStatus{
		NodeCondition:  nodeCondition,
		NUMAConditions: NUMAConditions,
	}

	result := types.InternalMemoryCalculationResult{TimeStamp: time.Now()}
	for _, plugin := range ra.plugins {
		if err := plugin.Reconcile(&memoryPressureStatus); err != nil {
			general.Errorf("plugin %T reconcile failed: %v", plugin, err)
			nonFatalErrors = append(nonFatalErrors, fmt.Errorf("plugin %T reconcile failed: %v", plugin, err))
			continue
		}

		advices := plugin.GetAdvices()
		result.ContainerEntries = append(result.ContainerEntries, advices.ContainerEntries...)
		result.ExtraEntries = append(result.ExtraEntries, advices.ExtraEntries...)
	}

	return &result, errors.NewAggregate(nonFatalErrors)
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
