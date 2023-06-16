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
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const evictionConditionCPUPressure = "CPUPressure"

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

type CPUPressureLoadEviction struct {
	sync.Mutex
	state       state.State
	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
	qosConf     *generic.QoSConfiguration
	dynamicConf *dynamic.DynamicAgentConfiguration

	metricsHistory map[string]Entries

	syncPeriod       time.Duration
	evictionPoolName string
	lastEvictionTime time.Time

	poolMetricCollectHandlers map[string]PoolMetricCollectHandler
}

func NewCPUPressureLoadEviction(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, state state.State) CPUPressureThresholdEviction {
	plugin := &CPUPressureLoadEviction{
		state:          state,
		emitter:        emitter,
		metaServer:     metaServer,
		metricsHistory: make(map[string]Entries),
		qosConf:        conf.QoSConfiguration,
		dynamicConf:    conf.DynamicAgentConfiguration,
		syncPeriod:     conf.LoadEvictionSyncPeriod,
	}

	plugin.poolMetricCollectHandlers = map[string]PoolMetricCollectHandler{
		consts.MetricLoad1MinContainer: plugin.collectPoolLoad,
	}
	return plugin
}

func (p *CPUPressureLoadEviction) Start(ctx context.Context) (err error) {
	general.Infof("%s", p.Name())
	go wait.UntilWithContext(ctx, p.collectMetrics, p.syncPeriod)
	return
}

func (p *CPUPressureLoadEviction) Name() string { return "pressure-load" }

func (p *CPUPressureLoadEviction) ThresholdMet(_ context.Context,
	_ *pluginapi.Empty) (*pluginapi.ThresholdMetResponse, error) {
	p.Lock()
	defer p.Unlock()

	dynamicConfig := p.dynamicConf.GetDynamicConfiguration()
	if !dynamicConfig.EnableLoadEviction {
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	general.Infof("with "+
		"loadUpperBoundRatio: %.2f, loadThresholdMetPercentage: %.2f, podGracePeriodSeconds: %d",
		dynamicConfig.LoadUpperBoundRatio, dynamicConfig.LoadThresholdMetPercentage,
		dynamicConfig.CPUPressureEvictionConfiguration.GracePeriod)

	var isSoftOver bool
	var softOverRatio float64

	var softThresholdMetPoolName string
	for poolName, entries := range p.metricsHistory[consts.MetricLoad1MinContainer] {
		if !entries.IsPoolEntry() || skipPools.Has(poolName) {
			continue
		}

		metricRing := entries[""]
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
	if softThresholdMetPoolName != "" {
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
	request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
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
			} else if entries[podUID][containerName].OwnerPoolName == evictionPoolName {
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
	if !dynamicConfig.EnableLoadEviction {
		p.metricsHistory = make(map[string]Entries)
		return
	}

	entries := p.state.GetPodEntries()
	p.clearExpiredMetricsHistory(entries)

	// collect metric for pod/container pairs, and store in local (i.e. poolsMetric)
	collectTime := time.Now().UnixNano()
	poolsMetric := make(map[string]map[string]float64)
	for podUID, entry := range entries {
		if entry.IsPoolEntry() {
			continue
		}

		for containerName, containerEntry := range entry {
			if containerEntry == nil {
				continue
			} else if containerEntry.OwnerPoolName == "" || skipPools.Has(containerEntry.OwnerPoolName) {
				general.Infof("skip collecting metric for pod: %s, "+
					"container: %s with owner pool name: %s", podUID, containerName, containerEntry.OwnerPoolName)
				continue
			}

			poolName := containerEntry.OwnerPoolName
			for _, metricName := range handleMetrics.UnsortedList() {
				value, err := p.metaServer.GetContainerMetric(podUID, containerName, metricName)
				if err != nil {
					general.Errorf("GetContainerMetric for pod: %s, "+
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
				p.pushMetric(dynamicConfig, metricName, podUID, containerName, snapshot)

				if poolsMetric[poolName] == nil {
					poolsMetric[poolName] = make(map[string]float64)
				}
				poolsMetric[poolName][metricName] += value
			}
		}
	}

	// push pre-collected local store (i.e. poolsMetric) to metric ring buffer
	for poolName, entry := range entries {
		if entry == nil || !entry.IsPoolEntry() || skipPools.Has(poolName) {
			continue
		}

		poolEntry := entry[advisorapi.FakedContainerName]
		if poolEntry == nil {
			continue
		}

		for _, metricName := range handleMetrics.UnsortedList() {
			if _, found := poolsMetric[poolName][metricName]; !found {
				continue
			}

			handler := p.poolMetricCollectHandlers[metricName]
			if handler == nil {
				general.Warningf("metric: %s hasn't pool metric "+
					"collecting handler, use default handler", metricName)
				handler = p.collectPoolMetricDefault
			}
			handler(dynamicConfig, metricName, poolsMetric[poolName][metricName], poolEntry, collectTime)
		}
	}
}

// collectPoolLoad is specifically used for cpu-load in pool level,
// and its upper-bound and lower-bound are calculated by pool size.
func (p *CPUPressureLoadEviction) collectPoolLoad(dynamicConfig *dynamic.Configuration,
	metricName string, metricValue float64, poolEntry *state.AllocationInfo, collectTime int64) {
	poolSize := poolEntry.AllocationResult.Size()
	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:       metricName,
			Value:      metricValue,
			UpperBound: float64(poolSize) * dynamicConfig.LoadUpperBoundRatio,
			LowerBound: float64(poolSize),
		},
		Time: collectTime,
	}

	p.logPoolSnapShot(snapshot, poolEntry.OwnerPoolName, true)
	p.pushMetric(dynamicConfig, metricName, poolEntry.OwnerPoolName, "", snapshot)
}

// collectPoolMetricDefault is a common collect in pool level,
// and its upper-bound and lower-bound are not defined.
func (p *CPUPressureLoadEviction) collectPoolMetricDefault(dynamicConfig *dynamic.Configuration,
	metricName string, metricValue float64, poolEntry *state.AllocationInfo, collectTime int64) {
	snapshot := &MetricSnapshot{
		Info: MetricInfo{
			Name:  metricName,
			Value: metricValue,
		},
		Time: collectTime,
	}

	p.logPoolSnapShot(snapshot, poolEntry.OwnerPoolName, false)
	p.pushMetric(dynamicConfig, metricName, poolEntry.OwnerPoolName, "", snapshot)
}

func (p *CPUPressureLoadEviction) pushMetric(dynamicConfig *dynamic.Configuration,
	metricName, entryName, subEntryName string, snapshot *MetricSnapshot) {
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

func (p *CPUPressureLoadEviction) clearEvictionPoolName() {
	if p.evictionPoolName != advisorapi.FakedContainerName {
		general.Infof("clear eviction pool name: %s", p.evictionPoolName)
	}
	p.evictionPoolName = advisorapi.FakedContainerName
}

func (p *CPUPressureLoadEviction) setEvictionPoolName(evictionPoolName string) {
	general.Infof("set eviction pool name: %s", evictionPoolName)
	p.evictionPoolName = evictionPoolName
}

func (p *CPUPressureLoadEviction) getEvictionPoolName() (exists bool, evictionPoolName string) {
	evictionPoolName = p.evictionPoolName
	if evictionPoolName == "" {
		exists = false
		return
	}
	exists = true
	return
}

func (p *CPUPressureLoadEviction) clearExpiredMetricsHistory(entries state.PodEntries) {
	for _, metricEntries := range p.metricsHistory {
		for entryName, subMetricEntries := range metricEntries {
			for subEntryName := range subMetricEntries {
				if entries[entryName][subEntryName] == nil {
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
