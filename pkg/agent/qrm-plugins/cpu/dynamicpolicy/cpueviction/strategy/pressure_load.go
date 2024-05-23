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

package strategy

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const EvictionNameLoad = "cpu-pressure-load-plugin"

const evictionConditionCPUPressure = "CPUPressure"

const (
	metricsNameCollectMetricsCalled = "collect_metrics_called_raw"
	metricsNamePoolMetricValue      = "pool_metric_value_raw"
	metricsNamePoolMetricBound      = "pool_metric_bound_raw"
	metricsNameThresholdMet         = "load_pressure_threshold_met_count"
	metricNameCollectPoolLoadCalled = "collect_pool_load_called"

	metricsTagKeyMetricName         = "metric_name"
	metricsTagKeyPoolName           = "pool_name"
	metricsTagKeyBoundType          = "bound_type"
	metricsTagKeyThresholdMetType   = "threshold_type"
	metricsTagKeyAdvisedThreshold   = "advised_threshold"
	metricsTagKeyPressureByPoolSize = "pool_sized_pressure"

	metricsTagValueBoundTypeUpper       = "upper"
	metricsTagValueBoundTypeLower       = "lower"
	metricsTagValueThresholdMetTypeHard = "hard"
	metricsTagValueThresholdMetTypeSoft = "soft"
)

var handleMetrics = sets.NewString(
	consts.MetricLoad1MinContainer,
)

type CPUPressureLoadEviction struct {
	sync.Mutex
	state       state.ReadonlyState
	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	qosConf     *generic.QoSConfiguration
	dynamicConf *dynamic.DynamicAgentConfiguration
	skipPools   sets.String

	metricsHistory map[string]Entries

	syncPeriod       time.Duration
	evictionPoolName string
	lastEvictionTime time.Time

	poolMetricCollectHandlers map[string]PoolMetricCollectHandler

	systemReservedCPUs machine.CPUSet
	configTranslator   *general.CommonSuffixTranslator
}

func NewCPUPressureLoadEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.ReadonlyState,
) (CPUPressureEviction, error) {
	plugin := &CPUPressureLoadEviction{
		state:            state,
		emitter:          emitter,
		metaServer:       metaServer,
		metricsHistory:   make(map[string]Entries),
		qosConf:          conf.QoSConfiguration,
		dynamicConf:      conf.DynamicAgentConfiguration,
		skipPools:        sets.NewString(conf.LoadPressureEvictionSkipPools...),
		syncPeriod:       conf.LoadEvictionSyncPeriod,
		configTranslator: general.NewCommonSuffixTranslator("-NUMA"),
	}

	systemReservedCores, reserveErr := cpuutil.GetCoresReservedForSystem(conf, metaServer, metaServer.KatalystMachineInfo, metaServer.CPUDetails.CPUs().Clone())
	if reserveErr != nil {
		general.Errorf("GetCoresReservedForSystem for reservedCPUsNum: %d failed with error: %v",
			conf.ReservedCPUCores, reserveErr)
		return plugin, reserveErr
	}
	plugin.systemReservedCPUs = systemReservedCores

	plugin.poolMetricCollectHandlers = map[string]PoolMetricCollectHandler{
		consts.MetricLoad1MinContainer: plugin.collectPoolLoad,
	}
	return plugin, nil
}

func (p *CPUPressureLoadEviction) Start(ctx context.Context) (err error) {
	general.Infof("%s", p.Name())
	go wait.UntilWithContext(ctx, p.collectMetrics, p.syncPeriod)
	return
}

func (p *CPUPressureLoadEviction) Name() string { return EvictionNameLoad }
func (p *CPUPressureLoadEviction) GetEvictPods(_ context.Context, _ *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	return &pluginapi.GetEvictPodsResponse{}, nil
}

func (p *CPUPressureLoadEviction) ThresholdMet(_ context.Context,
	_ *pluginapi.Empty,
) (*pluginapi.ThresholdMetResponse, error) {
	p.Lock()
	defer p.Unlock()

	dynamicConfig := p.dynamicConf.GetDynamicConfiguration()
	if !dynamicConfig.EnableLoadEviction {
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	general.Infof("with loadUpperBoundRatio: %.2f, loadThresholdMetPercentage: %.2f, podGracePeriodSeconds: %d",
		dynamicConfig.LoadUpperBoundRatio, dynamicConfig.LoadThresholdMetPercentage, dynamicConfig.CPUPressureEvictionConfiguration.GracePeriod)

	var isSoftOver bool
	var softOverRatio float64

	var softThresholdMetPoolName string
	for poolName, entries := range p.metricsHistory[consts.MetricLoad1MinContainer] {
		if !entries.IsPoolEntry() || p.skipPools.Has(p.configTranslator.Translate(poolName)) || state.IsIsolationPool(poolName) {
			continue
		}

		metricRing := entries[advisorapi.FakedContainerName]
		if metricRing == nil {
			general.Warningf("pool: %s hasn't metric: %s metricsRing", poolName, consts.MetricLoad1MinContainer)
			continue
		}

		softOverCount, hardOverCount := metricRing.Count()

		softOverRatio = float64(softOverCount) / float64(metricRing.MaxLen)
		hardOverRatio := float64(hardOverCount) / float64(metricRing.MaxLen)

		isSoftOver = softOverRatio >= dynamicConfig.LoadThresholdMetPercentage
		isHardOver := hardOverRatio >= dynamicConfig.LoadThresholdMetPercentage

		general.Infof("pool: %s, metric: %s, softOverCount: %d,"+
			" hardOverCount: %d, softOverRatio: %.2f, hardOverRatio: %.2f, isSoftOver: %v, hardOverRatio: %v",
			poolName, consts.MetricLoad1MinContainer, softOverCount,
			hardOverCount, softOverRatio, hardOverRatio,
			isSoftOver, isHardOver)

		if isHardOver {
			_ = p.emitter.StoreFloat64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyPoolName:         poolName,
					metricsTagKeyMetricName:       consts.MetricLoad1MinContainer,
					metricsTagKeyThresholdMetType: metricsTagValueThresholdMetTypeHard,
				})...)

			p.setEvictionPoolName(poolName)
			return &pluginapi.ThresholdMetResponse{
				ThresholdValue:    hardOverRatio,
				ObservedValue:     dynamicConfig.LoadThresholdMetPercentage,
				ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
				MetType:           pluginapi.ThresholdMetType_HARD_MET,
				EvictionScope:     consts.MetricLoad1MinContainer,
				Condition: &pluginapi.Condition{
					ConditionType: pluginapi.ConditionType_NODE_CONDITION,
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
					ConditionName: evictionConditionCPUPressure,
					MetCondition:  true,
				},
			}, nil
		} else if isSoftOver {
			softThresholdMetPoolName = poolName
		}
	}

	p.clearEvictionPoolName()
	if softThresholdMetPoolName != advisorapi.EmptyOwnerPoolName {
		_ = p.emitter.StoreFloat64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyPoolName:         softThresholdMetPoolName,
				metricsTagKeyMetricName:       consts.MetricLoad1MinContainer,
				metricsTagKeyThresholdMetType: metricsTagValueThresholdMetTypeSoft,
			})...)
		return &pluginapi.ThresholdMetResponse{
			ThresholdValue:    softOverRatio,
			ObservedValue:     dynamicConfig.LoadThresholdMetPercentage,
			ThresholdOperator: pluginapi.ThresholdOperator_GREATER_THAN,
			MetType:           pluginapi.ThresholdMetType_SOFT_MET,
			EvictionScope:     consts.MetricLoad1MinContainer,
			Condition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionCPUPressure,
				MetCondition:  true,
			},
		}, nil
	}

	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (p *CPUPressureLoadEviction) GetTopEvictionPods(_ context.Context,
	request *pluginapi.GetTopEvictionPodsRequest,
) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	} else if len(request.ActivePods) == 0 {
		general.Warningf("got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	p.Lock()
	defer p.Unlock()

	dynamicConfig := p.dynamicConf.GetDynamicConfiguration()
	if !dynamicConfig.EnableLoadEviction {
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	exists, evictionPoolName := p.getEvictionPoolName()
	if !exists {
		general.Warningf("evictionPoolName doesn't exist, skip eviction")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	podPoolMap := getPodPoolMapFunc(p.metaServer.MetaAgent, p.state)
	candidatePods := native.FilterPods(request.ActivePods, func(pod *v1.Pod) (bool, error) {
		if pod == nil {
			return false, fmt.Errorf("FilterPods got nil pod")
		}

		podUID := string(pod.GetUID())
		for i := range pod.Spec.Containers {
			containerName := pod.Spec.Containers[i].Name
			if podPoolMap[podUID][containerName] == nil {
				return false, nil
			} else if podPoolMap[podUID][containerName].OwnerPool == evictionPoolName {
				return true, nil
			}
		}

		return false, nil
	})
	if len(candidatePods) == 0 {
		general.Warningf("got empty candidate pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	general.Infof("evictionPool: %s has %d candidates", evictionPoolName, len(candidatePods))

	now := time.Now()
	if !(p.lastEvictionTime.IsZero() || now.Sub(p.lastEvictionTime) >= dynamicConfig.LoadEvictionCoolDownTime) {
		general.Infof("in eviction cool-down time, skip eviction. now: %s, lastEvictionTime: %s",
			now.String(), p.lastEvictionTime.String())
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}
	p.lastEvictionTime = now

	sort.Slice(candidatePods, func(i, j int) bool {
		return p.getMetricHistorySumForPod(consts.MetricLoad1MinContainer, candidatePods[i]) >
			p.getMetricHistorySumForPod(consts.MetricLoad1MinContainer, candidatePods[j])
	})

	retLen := general.MinUInt64(request.TopN, uint64(len(candidatePods)))
	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: candidatePods[:retLen],
	}

	if gracePeriod := dynamicConfig.CPUPressureEvictionConfiguration.GracePeriod; gracePeriod >= 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	// clear eviction pool after sending eviction candidates,
	// to avoid stacked in evicting pods in the pool when collectMetrics isn't executed normally
	p.clearEvictionPoolName()

	return resp, nil
}

func (p *CPUPressureLoadEviction) collectMetrics(_ context.Context) {
	general.Infof("execute")
	_ = p.emitter.StoreInt64(metricsNameCollectMetricsCalled, 1, metrics.MetricTypeNameRaw)

	p.Lock()
	defer p.Unlock()

	dynamicConfig := p.dynamicConf.GetDynamicConfiguration()
	// always reset in-memory metric histories if load-eviction is disabled
	if !dynamicConfig.EnableLoadEviction {
		p.metricsHistory = make(map[string]Entries)
		return
	}

	pod2Pool := getPodPoolMapFunc(p.metaServer.MetaAgent, p.state)
	p.clearExpiredMetricsHistory(pod2Pool)

	// collect metric for pod/container pairs, and store in local (i.e. poolsMetric)
	collectTime := time.Now().UnixNano()
	poolsMetric := make(map[string]map[string]float64)
	for podUID, entry := range pod2Pool {
		for containerName, containerEntry := range entry {
			if containerEntry == nil || containerEntry.IsPool {
				continue
			} else if containerEntry.OwnerPool == advisorapi.EmptyOwnerPoolName || p.skipPools.Has(p.configTranslator.Translate(containerEntry.OwnerPool)) {
				general.Infof("skip collecting metric for pod: %s, container: %s with owner pool name: %s",
					podUID, containerName, containerEntry.OwnerPool)
				continue
			}

			poolName := containerEntry.OwnerPool
			for _, metricName := range handleMetrics.UnsortedList() {
				m, err := p.metaServer.GetContainerMetric(podUID, containerName, metricName)
				if err != nil {
					general.Errorf("GetContainerMetric for pod: %s, container: %s failed with error: %v", podUID, containerName, err)
					continue
				}

				snapshot := &MetricSnapshot{
					Info: MetricInfo{
						Name:  metricName,
						Value: m.Value,
					},
					Time: collectTime,
				}
				p.pushMetric(dynamicConfig, metricName, podUID, containerName, snapshot)

				if poolsMetric[poolName] == nil {
					poolsMetric[poolName] = make(map[string]float64)
				}
				poolsMetric[poolName][metricName] += m.Value
			}
		}
	}

	// push pre-collected local store (i.e. poolsMetric) to metric ring buffer
	underPressure := p.checkSharedPressureByPoolSize(pod2Pool)
	for poolName, entry := range pod2Pool {
		if entry == nil {
			continue
		}

		for _, poolEntry := range entry {
			if poolEntry == nil || !poolEntry.IsPool || p.skipPools.Has(p.configTranslator.Translate(poolName)) {
				continue
			}

			for _, metricName := range handleMetrics.UnsortedList() {
				if _, found := poolsMetric[poolName][metricName]; !found {
					continue
				}

				handler := p.poolMetricCollectHandlers[metricName]
				if handler == nil {
					general.Warningf("metric: %s hasn't pool metric collecting handler, use default handler", metricName)
					handler = p.collectPoolMetricDefault
				}
				handler(dynamicConfig, underPressure, metricName, poolsMetric[poolName][metricName], poolName, poolEntry.PoolSize, collectTime)
			}
		}
	}
}

// checkSharedPressureByPoolSize checks if the sum of all the shared pool size has reached the maximum
func (p *CPUPressureLoadEviction) checkSharedPressureByPoolSize(pod2Pool PodPoolMap) bool {
	poolSizeSum := 0
	for poolName, entry := range pod2Pool {
		if entry == nil {
			continue
		}

		for _, containerEntry := range entry {
			if !containerEntry.IsPool || p.skipPools.Has(p.configTranslator.Translate(poolName)) || entry[advisorapi.FakedContainerName] == nil {
				continue
			}
			poolSizeSum += containerEntry.PoolSize
		}
	}

	sharedPoolsLimit := p.accumulateSharedPoolsLimit()
	pressureByPoolSize := poolSizeSum >= sharedPoolsLimit
	general.Infof("shared pools under pressure: %v, poolSizeSum: %v, limit: %v", pressureByPoolSize, poolSizeSum, sharedPoolsLimit)

	return pressureByPoolSize
}

// accumulateSharedPoolsLimit calculates the cpu core limit used by shared core pool,
// and it equals: machine-core - cores-for-dedicated-pods - reserved-cores-reclaim-pods - reserved-cores-system-pods.
func (p *CPUPressureLoadEviction) accumulateSharedPoolsLimit() int {
	availableCPUSet := p.state.GetMachineState().GetFilteredAvailableCPUSet(p.systemReservedCPUs, nil, state.CheckDedicatedNUMABinding)

	coreNumReservedForReclaim := p.dynamicConf.GetDynamicConfiguration().MinReclaimedResourceForAllocate[v1.ResourceCPU]
	reservedForReclaim := machine.GetCoreNumReservedForReclaim(int(coreNumReservedForReclaim.Value()), p.metaServer.NumNUMANodes)

	reservedForReclaimInSharedNuma := 0
	sharedCoresNUMAs := p.state.GetMachineState().GetFilteredNUMASet(state.CheckNUMABinding)
	for _, numaID := range sharedCoresNUMAs.ToSliceInt() {
		reservedForReclaimInSharedNuma += reservedForReclaim[numaID]
	}

	result := availableCPUSet.Size() - reservedForReclaimInSharedNuma
	general.Infof("get shared pools limit: %v, availableCPUSet: %v, sharedCoresNUMAs:%v, reservedForReclaim: %v",
		result, availableCPUSet.String(), sharedCoresNUMAs.String(), reservedForReclaim)
	return result
}

// collectPoolLoad is specifically used for cpu-load in pool level,
// and its upper-bound and lower-bound are calculated by pool size.
func (p *CPUPressureLoadEviction) collectPoolLoad(dynamicConfig *dynamic.Configuration, pressureByPoolSize bool,
	metricName string, metricValue float64, poolName string, poolSize int, collectTime int64,
) {
	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:       metricName,
			Value:      metricValue,
			UpperBound: float64(poolSize) * dynamicConfig.LoadUpperBoundRatio,
			LowerBound: float64(poolSize),
		},
		Time: collectTime,
	}

	useAdvisedThreshold := p.checkPressureWithAdvisedThreshold()
	if useAdvisedThreshold {
		// it must not be triggered when it's healthy
		lowerBound := metricValue + 1
		upperBound := lowerBound * dynamicConfig.LoadUpperBoundRatio

		if pressureByPoolSize {
			// soft over must be triggered when it's under pressure
			lowerBound = 1
			upperBound = float64(poolSize) * dynamicConfig.LoadUpperBoundRatio
		}
		snapshot.Info.LowerBound = lowerBound
		snapshot.Info.UpperBound = upperBound
	}

	general.Infof("collect pool load, pool name: %v, useAdvisedThreshold:%v, pressureByPoolSize:%v", poolName, useAdvisedThreshold, pressureByPoolSize)
	_ = p.emitter.StoreInt64(metricNameCollectPoolLoadCalled, 1, metrics.MetricTypeNameCount,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPoolName:           poolName,
			metricsTagKeyMetricName:         snapshot.Info.Name,
			metricsTagKeyPressureByPoolSize: strconv.FormatBool(pressureByPoolSize),
			metricsTagKeyAdvisedThreshold:   strconv.FormatBool(useAdvisedThreshold),
		})...)

	p.logPoolSnapShot(snapshot, poolName, true)
	p.pushMetric(dynamicConfig, metricName, poolName, advisorapi.FakedContainerName, snapshot)
}

// collectPoolMetricDefault is a common collect in pool level,
// and its upper-bound and lower-bound are not defined.
func (p *CPUPressureLoadEviction) collectPoolMetricDefault(dynamicConfig *dynamic.Configuration, _ bool,
	metricName string, metricValue float64, poolName string, _ int, collectTime int64,
) {
	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:  metricName,
			Value: metricValue,
		},
		Time: collectTime,
	}

	p.logPoolSnapShot(snapshot, poolName, false)
	p.pushMetric(dynamicConfig, metricName, poolName, advisorapi.FakedContainerName, snapshot)
}

// pushMetric stores and push-in metric for the given pod
func (p *CPUPressureLoadEviction) pushMetric(dynamicConfig *dynamic.Configuration,
	metricName, entryName, subEntryName string, snapshot *MetricSnapshot,
) {
	if p.metricsHistory[metricName] == nil {
		p.metricsHistory[metricName] = make(Entries)
	}

	if p.metricsHistory[metricName][entryName] == nil {
		p.metricsHistory[metricName][entryName] = make(SubEntries)
	}

	if p.metricsHistory[metricName][entryName][subEntryName] == nil {
		p.metricsHistory[metricName][entryName][subEntryName] = CreateMetricRing(dynamicConfig.LoadMetricRingSize)
	}

	p.metricsHistory[metricName][entryName][subEntryName].Push(snapshot)
}

func (p *CPUPressureLoadEviction) logPoolSnapShot(snapshot *MetricSnapshot, poolName string, withBound bool) {
	if snapshot == nil {
		return
	}

	_ = p.emitter.StoreFloat64(metricsNamePoolMetricValue, snapshot.Info.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPoolName:   poolName,
			metricsTagKeyMetricName: snapshot.Info.Name,
		})...)

	if withBound {
		general.Infof("push metric: %s, value: %.f, UpperBound: %.2f, LowerBound: %.2f of pool: %s to ring",
			snapshot.Info.Name, snapshot.Info.Value, snapshot.Info.UpperBound, snapshot.Info.LowerBound, poolName)

		_ = p.emitter.StoreFloat64(metricsNamePoolMetricBound, snapshot.Info.UpperBound, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyPoolName:   poolName,
				metricsTagKeyMetricName: snapshot.Info.Name,
				metricsTagKeyBoundType:  metricsTagValueBoundTypeUpper,
			})...)

		_ = p.emitter.StoreFloat64(metricsNamePoolMetricBound, snapshot.Info.LowerBound, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyPoolName:   poolName,
				metricsTagKeyMetricName: snapshot.Info.Name,
				metricsTagKeyBoundType:  metricsTagValueBoundTypeLower,
			})...)

	} else {
		general.Infof("push metric: %s, value: %.2f of pool: %s to ring",
			snapshot.Info.Name, snapshot.Info.Value, poolName)
	}
}

// clearEvictionPoolName resets pool in local memory
func (p *CPUPressureLoadEviction) clearEvictionPoolName() {
	if p.evictionPoolName != advisorapi.FakedContainerName {
		general.Infof("clear eviction pool name: %s", p.evictionPoolName)
	}
	p.evictionPoolName = advisorapi.FakedContainerName
}

// setEvictionPoolName sets pool in local memory
func (p *CPUPressureLoadEviction) setEvictionPoolName(evictionPoolName string) {
	general.Infof("set eviction pool name: %s", evictionPoolName)
	p.evictionPoolName = evictionPoolName
}

// getEvictionPoolName returns the previously-set pool
func (p *CPUPressureLoadEviction) getEvictionPoolName() (exists bool, evictionPoolName string) {
	evictionPoolName = p.evictionPoolName
	if evictionPoolName == advisorapi.FakedContainerName {
		exists = false
		return
	}
	exists = true
	return
}

// clearExpiredMetricsHistory deletes the expired metric in local memory
func (p *CPUPressureLoadEviction) clearExpiredMetricsHistory(podPoolMap PodPoolMap) {
	for _, metricEntries := range p.metricsHistory {
		for entryName, subMetricEntries := range metricEntries {
			for subEntryName := range subMetricEntries {
				if podPoolMap[entryName][subEntryName] == nil {
					general.Infof("entryName: %s subEntryName: %s metric entry is expired, clear it",
						entryName, subEntryName)
					delete(subMetricEntries, subEntryName)
				}
			}

			if len(subMetricEntries) == 0 {
				general.Infof("there is no subEntry in entryName: %s, clear it", entryName)
				delete(metricEntries, entryName)
			}
		}
	}
}

// getMetricHistorySumForPod returns the accumulated value for the given pod
func (p *CPUPressureLoadEviction) getMetricHistorySumForPod(metricName string, pod *v1.Pod) float64 {
	if pod == nil {
		return 0
	}

	ret := 0.0
	podUID := string(pod.GetUID())
	for i := range pod.Spec.Containers {
		containerName := pod.Spec.Containers[i].Name
		if p.metricsHistory[metricName][podUID][containerName] != nil {
			ret += p.metricsHistory[consts.MetricLoad1MinContainer][podUID][containerName].Sum()
		}
	}
	return ret
}

// checkPressureWithAdvisedThreshold returns if we should check pressure according to advisor.
// When enabled, plugin must make sure soft over is triggered when shared_core pools can't be expanded anymore.
func (p *CPUPressureLoadEviction) checkPressureWithAdvisedThreshold() bool {
	// for now, we consider ReservedResourceForAllocate as downgrading or manual intervention configuration,
	// when it's set to a value greater than zero, fall back to static threshold
	dynamicConfiguration := p.dynamicConf.GetDynamicConfiguration()
	return dynamicConfiguration.EnableReclaim && dynamicConfiguration.ReservedResourceForAllocate.Cpu().Value() == 0
}
