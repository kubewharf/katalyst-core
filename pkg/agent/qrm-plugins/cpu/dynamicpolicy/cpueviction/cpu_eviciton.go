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

package cpueviction

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	statepkg "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	cpuPressureEvictionPluginName = "cpu-pressure-eviction-plugin"
	evictionConditionCPUPressure  = "CPUPressure"

	CPUPressureEvictionPodGracePeriod int64 = 50
)

const (
	metricsNameCollectMetricsCalled = "collect_metrics_called_raw"
	metricsNamePoolMetricValue      = "pool_metric_value_raw"
	metricsNamePoolMetricBound      = "pool_metric_bound_raw"
	metricsNameThresholdMet         = "threshold_met_count"

	metricsTagKeyMetricName       = "metric_name"
	metricsTagKeyPoolName         = "pool_name"
	metricsTagKeyBoundType        = "bound_type"
	metricsTagKeyThresholdMetType = "threshold_type"

	metricsTagValueBoundTypeUpper       = "upper"
	metricsTagValueBoundTypeLower       = "lower"
	metricsTagValueThresholdMetTypeHard = "hard"
	metricsTagValueThresholdMetTypeSoft = "soft"
)

var (
	handleMetrics = sets.NewString(
		consts.MetricLoad1MinContainer,
	)

	skipPools = sets.NewString(
		state.PoolNameReclaim,
		state.PoolNameDedicated,
		state.PoolNameFallback,
		state.PoolNameReserve,
	)
)

func NewCPUPressureEvictionPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state statepkg.State) (*agent.PluginWrapper, error) {

	plugin, err := newCPUPressureEvictionPlugin(emitter, metaServer, conf, state)
	if err != nil {
		return nil, fmt.Errorf("create cpu eviction plugin failed: %s", err)
	}

	return &agent.PluginWrapper{GenericPlugin: plugin}, nil
}

type cpuPressureEvictionPlugin struct {
	ctx                       context.Context
	cancel                    context.CancelFunc
	started                   bool
	state                     statepkg.State
	metricsHistory            map[string]Entries
	emitter                   metrics.MetricEmitter
	metaServer                *metaserver.MetaServer
	poolMetricCollectHandlers map[string]PoolMetricCollectHandler
	evictionPoolName          string

	metricRingSize                           int
	loadUpperBoundRatio                      float64
	loadThresholdMetPercentage               float64
	cpuPressureEvictionPodGracePeriodSeconds int64
	syncPeriod                               time.Duration
	evictionColdPeriod                       time.Duration
	lastEvictionTime                         time.Time

	sync.Mutex
}

func newCPUPressureEvictionPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state statepkg.State) (skeleton.GenericPlugin, error) {
	wrappedEmitter := emitter.WithTags(cpuPressureEvictionPluginName)

	plugin := &cpuPressureEvictionPlugin{
		state:                                    state,
		emitter:                                  wrappedEmitter,
		metaServer:                               metaServer,
		metricsHistory:                           make(map[string]Entries),
		metricRingSize:                           conf.MetricRingSize,
		loadUpperBoundRatio:                      conf.LoadUpperBoundRatio,
		loadThresholdMetPercentage:               conf.LoadThresholdMetPercentage,
		cpuPressureEvictionPodGracePeriodSeconds: conf.CPUPressureEvictionPodGracePeriodSeconds,
		syncPeriod:                               conf.CPUPressureEvictionSyncPeriod,
		evictionColdPeriod:                       conf.CPUPressureEvictionColdPeriod,
	}

	plugin.poolMetricCollectHandlers = map[string]PoolMetricCollectHandler{
		consts.MetricLoad1MinContainer: plugin.collectPoolLoad,
	}

	klog.Infof("[cpu-pressure-eviction-plugin.newCPUPressureEvictionPlugin] newCPUPressureEvictionPlugin: with "+
		"loadUpperBoundRatio: %.2f, loadThresholdMetPercentage: %.2f, cpuPressureEvictionPodGracePeriodSeconds: %d",
		plugin.loadUpperBoundRatio, plugin.loadThresholdMetPercentage, plugin.cpuPressureEvictionPodGracePeriodSeconds)

	return skeleton.NewRegistrationPluginWrapper(plugin, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
		})
}

func (p *cpuPressureEvictionPlugin) Name() string {
	return cpuPressureEvictionPluginName
}

func (p *cpuPressureEvictionPlugin) Start() (err error) {
	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		return
	}

	klog.Infof("[cpu-pressure-eviction-plugin] start %s", p.Name())

	p.ctx, p.cancel = context.WithCancel(context.Background())
	go wait.UntilWithContext(p.ctx, p.collectMetrics, p.syncPeriod)
	return
}

func (p *cpuPressureEvictionPlugin) clearExpiredMetricsHistory(entries state.PodEntries) {
	for _, metricEntries := range p.metricsHistory {
		for entryName, subMetricEntries := range metricEntries {
			for subEntryName := range subMetricEntries {
				if entries[entryName][subEntryName] == nil {
					klog.Infof("[cpu-pressure-eviction-plugin.clearExpiredMetricsHistory] entryName: %s subEntryName: %s metric entry is expired, clear it",
						entryName, subEntryName)
					delete(subMetricEntries, subEntryName)
				}
			}

			if len(subMetricEntries) == 0 {
				klog.Infof("[cpu-pressure-eviction-plugin.clearExpiredMetricsHistory] there is no subEntry in entryName: %s, clear it", entryName)
				delete(metricEntries, entryName)
			}
		}
	}
}

func (p *cpuPressureEvictionPlugin) collectMetrics(ctx context.Context) {
	klog.Infof("[cpu-pressure-eviction-plugin] execute collectMetrics")
	_ = p.emitter.StoreInt64(metricsNameCollectMetricsCalled, 1, metrics.MetricTypeNameRaw)

	p.Lock()
	defer p.Unlock()

	entries := p.state.GetPodEntries()

	p.clearExpiredMetricsHistory(entries)

	collectTime := time.Now().UnixNano()

	// handle containers
	poolsMetric := make(map[string]map[string]float64)
	for podUID, entry := range entries {
		if entry.IsPoolEntry() {
			continue
		}

		for containerName, containerEntry := range entry {
			if containerEntry == nil {
				continue
			} else if containerEntry.OwnerPoolName == "" || skipPools.Has(containerEntry.OwnerPoolName) {
				klog.Infof("[cpu-pressure-eviction-plugin.collectMetrics] skip collecting metric for pod: %s, "+
					"container: %s with owner pool name: %s", podUID, containerName, containerEntry.OwnerPoolName)
				continue
			}

			poolName := containerEntry.OwnerPoolName

			for _, metricName := range handleMetrics.UnsortedList() {
				value, err := p.metaServer.GetContainerMetric(podUID, containerName, metricName)
				if err != nil {
					klog.Errorf("[cpu-pressure-eviction-plugin.collectMetrics] GetContainerMetric for pod: %s, "+
						"container: %s failed with error: %v", podUID, containerName, err)
					continue
				}

				snapshot := &MetricSnapshot{
					Info: MetricInfo{
						Name:  metricName,
						Value: value,
					},
					Time: collectTime,
				}
				p.PushMetric(metricName, podUID, containerName, snapshot)

				if poolsMetric[poolName] == nil {
					poolsMetric[poolName] = make(map[string]float64)
				}
				poolsMetric[poolName][metricName] += value
			}
		}
	}

	// handle pools
	for poolName, entry := range entries {
		if entry == nil || !entry.IsPoolEntry() || skipPools.Has(poolName) {
			continue
		}

		poolEntry := entry[""]
		if poolEntry == nil {
			continue
		}

		for _, metricName := range handleMetrics.UnsortedList() {
			handler := p.poolMetricCollectHandlers[metricName]
			if handler == nil {
				klog.Warningf("[cpu-pressure-eviction-plugin.collectMetrics] metric: %s hasn't pool metric "+
					"collecting handler, use default handler", metricName)
				handler = p.collectPoolMetricDefault
			}

			if _, found := poolsMetric[poolName][metricName]; !found {
				continue
			}

			handler(metricName, poolsMetric[poolName][metricName], poolEntry, collectTime)
		}
	}
}

func (p *cpuPressureEvictionPlugin) logPoolSnapShot(snapshot *MetricSnapshot, poolName string, withBound bool) {
	if snapshot == nil {
		return
	}

	_ = p.emitter.StoreFloat64(metricsNamePoolMetricValue, snapshot.Info.Value, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPoolName:   poolName,
			metricsTagKeyMetricName: snapshot.Info.Name,
		})...)

	if withBound {
		klog.Infof("[cpu-pressure-eviction-plugin.logPoolSnapShot] push metric: %s, value: %.f, UpperBound: %.2f, LowerBound: %.2f of pool: %s to ring",
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
		klog.Infof("[cpu-pressure-eviction-plugin.logPoolSnapShot] push metric: %s, value: %.2f of pool: %s to ring",
			snapshot.Info.Name, snapshot.Info.Value, poolName)
	}
}

// collectPoolMetricDefault is a common collect in pool level,
// and its upper-bound and lower-bound are not defined.
func (p *cpuPressureEvictionPlugin) collectPoolMetricDefault(metricName string, metricValue float64, poolEntry *state.AllocationInfo, collectTime int64) {
	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:  metricName,
			Value: metricValue,
		},
		Time: collectTime,
	}

	p.logPoolSnapShot(snapshot, poolEntry.OwnerPoolName, false)
	p.PushMetric(metricName, poolEntry.OwnerPoolName, "", snapshot)
}

// collectPoolLoad is specifically used for cpu-load in pool level,
// and its upper-bound and lower-bound are calculated by pool size.
func (p *cpuPressureEvictionPlugin) collectPoolLoad(metricName string, metricValue float64, poolEntry *state.AllocationInfo, collectTime int64) {
	poolSize := poolEntry.AllocationResult.Size()

	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:       metricName,
			Value:      metricValue,
			UpperBound: float64(poolSize) * p.loadUpperBoundRatio,
			LowerBound: float64(poolSize),
		},
		Time: collectTime,
	}

	p.logPoolSnapShot(snapshot, poolEntry.OwnerPoolName, true)
	p.PushMetric(metricName, poolEntry.OwnerPoolName, "", snapshot)
}

func (p *cpuPressureEvictionPlugin) PushMetric(metricName, entryName, subEntryName string, snapshot *MetricSnapshot) {
	if p.metricsHistory[metricName] == nil {
		p.metricsHistory[metricName] = make(Entries)
	}

	if p.metricsHistory[metricName][entryName] == nil {
		p.metricsHistory[metricName][entryName] = make(SubEntries)
	}

	if p.metricsHistory[metricName][entryName][subEntryName] == nil {
		p.metricsHistory[metricName][entryName][subEntryName] = CreateMetricRing(p.metricRingSize)
	}

	p.metricsHistory[metricName][entryName][subEntryName].Push(snapshot)
}

func (p *cpuPressureEvictionPlugin) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exist
	if !p.started {
		return nil
	}

	klog.Infof("[cpu-pressure-eviction-plugin] stop %s", p.Name())

	p.cancel()
	return nil
}

func (p *cpuPressureEvictionPlugin) GetEvictPods(ctx context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}

func (p *cpuPressureEvictionPlugin) clearEvictionPoolName() {
	if p.evictionPoolName != "" {
		klog.Infof("[cpu-pressure-eviction-plugin] clear eviction pool name: %s", p.evictionPoolName)
	}
	p.evictionPoolName = ""
}

func (p *cpuPressureEvictionPlugin) setEvictionPoolName(evictionPoolName string) {
	klog.Infof("[cpu-pressure-eviction-plugin] set eviction pool name: %s", evictionPoolName)
	p.evictionPoolName = evictionPoolName
}

func (p *cpuPressureEvictionPlugin) getEvictionPoolName() (exists bool, evictionPoolName string) {
	evictionPoolName = p.evictionPoolName
	if evictionPoolName == "" {
		exists = false
		return
	}

	exists = true
	return
}

func (p *cpuPressureEvictionPlugin) ThresholdMet(ctx context.Context, empty *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	p.Lock()
	defer p.Unlock()

	var isSoftOver bool
	var softOverRatio float64

	var softThresholdMetPoolName string
	for poolName, entries := range p.metricsHistory[consts.MetricLoad1MinContainer] {
		if !entries.IsPoolEntry() || skipPools.Has(poolName) {
			continue
		}

		metricRing := entries[""]
		if metricRing == nil {
			klog.Warningf("[cpu-pressure-eviction-plugin.ThresholdMet] pool: %s hasn't metric: %s metricsRing", poolName, consts.MetricLoad1MinContainer)
			continue
		}

		softOverCount, hardOverCount := metricRing.Count()

		softOverRatio = float64(softOverCount) / float64(metricRing.MaxLen)
		hardOverRatio := float64(hardOverCount) / float64(metricRing.MaxLen)

		isSoftOver = softOverRatio >= p.loadThresholdMetPercentage
		isHardOver := hardOverRatio >= p.loadThresholdMetPercentage

		klog.Infof("[cpu-pressure-eviction-plugin.ThresholdMet] pool: %s, metric: %s, softOverCount: %d,"+
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
				ObservedValue:     p.loadThresholdMetPercentage,
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

	if softThresholdMetPoolName != "" {
		_ = p.emitter.StoreFloat64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyPoolName:         softThresholdMetPoolName,
				metricsTagKeyMetricName:       consts.MetricLoad1MinContainer,
				metricsTagKeyThresholdMetType: metricsTagValueThresholdMetTypeSoft,
			})...)

		return &pluginapi.ThresholdMetResponse{
			ThresholdValue:    softOverRatio,
			ObservedValue:     p.loadThresholdMetPercentage,
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

func (p *cpuPressureEvictionPlugin) getMetricHistorySumForPod(metricName string, pod *v1.Pod) float64 {
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

func (p *cpuPressureEvictionPlugin) GetTopEvictionPods(ctx context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		klog.Warningf("[cpu-pressure-eviction-plugin] GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	p.Lock()
	defer p.Unlock()

	exists, evictionPoolName := p.getEvictionPoolName()
	if !exists {
		klog.Warningf("[cpu-pressure-eviction-plugin.GetTopEvictionPods] evictionPoolName doesn't exist, skip eviction")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	entries := p.state.GetPodEntries()
	candidatePods := native.FilterPods(request.ActivePods, func(pod *v1.Pod) (bool, error) {
		if pod == nil {
			return false, fmt.Errorf("FilterPods got nil pod")
		}

		podUID := string(pod.GetUID())
		for i := range pod.Spec.Containers {
			containerName := pod.Spec.Containers[i].Name

			if entries[podUID][containerName] == nil {
				return false, nil
			}

			if entries[podUID][containerName].OwnerPoolName == evictionPoolName {
				return true, nil
			}
		}

		return false, nil
	})
	if len(candidatePods) == 0 {
		klog.Warningf("[cpu-pressure-eviction-plugin.GetTopEvictionPods] GetTopEvictionPods got empty candidate pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	klog.Infof("[cpu-pressure-eviction-plugin.GetTopEvictionPods] evictionPool: %s has %d candidates", evictionPoolName, len(candidatePods))

	now := time.Now()
	if !(p.lastEvictionTime.IsZero() || now.Sub(p.lastEvictionTime) >= p.evictionColdPeriod) {
		klog.Infof("[cpu-pressure-eviction-plugin.GetTopEvictionPods] in eviction colding time, skip eviction. now: %s, lastEvictionTime: %s",
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

	if p.cpuPressureEvictionPodGracePeriodSeconds >= 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: p.cpuPressureEvictionPodGracePeriodSeconds,
		}
	}

	// clear eviction pool after sending eviction candidates,
	// to avoid stucking in evicting pods in the pool when collectMetrics isn't executed normally
	p.clearEvictionPoolName()

	return resp, nil
}
