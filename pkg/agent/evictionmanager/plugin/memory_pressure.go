// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugin

import (
	"context"
	"fmt"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

// eviction scope related variables
// actionReclaimedEviction for reclaimed_cores, while actionEviction for all pods
const (
	actionNoop = iota
	actionReclaimedEviction
	actionEviction
)

// control-related variables
const (
	kswapdStealPreviousCycleMissing = -1
	nonExistNumaID                  = -1
)

const (
	EvictionPluginNameMemoryPressure = "memory-pressure-eviction-plugin"
	evictionScopeMemory              = "memory"
	evictionConditionMemoryPressure  = "MemoryPressure"
)

const (
	metricsNameFetchMetricError   = "fetch_metric_error_count"
	metricsNameNumberOfTargetPods = "number_of_target_pods_raw"
	metricsNameThresholdMet       = "threshold_met_count"
	metricsNameNumaMetric         = "numa_metric_raw"
	metricsNameSystemMetric       = "system_metric_raw"
	metricsNameContainerMetric    = "container_metric_raw"
	metricsNamePodMetric          = "pod_metric_raw"

	metricsTagKeyEvictionScope  = "eviction_scope"
	metricsTagKeyDetectionLevel = "detection_level"
	metricsTagKeyNumaID         = "numa_id"
	metricsTagKeyAction         = "action"
	metricsTagKeyMetricName     = "metric_name"
	metricsTagKeyPodUID         = "pod_uid"
	metricsTagKeyContainerName  = "container_name"

	metricsTagValueDetectionLevelNuma          = "numa"
	metricsTagValueDetectionLevelSystem        = "system"
	metricsTagValueActionReclaimedEviction     = "reclaimed_eviction"
	metricsTagValueActionEviction              = "eviction"
	metricsTagValueNumaFreeBelowWatermarkTimes = "numa_free_below_watermark_times"
	metricsTagValueSystemKswapdDiff            = "system_kswapd_diff"
	metricsTagValueSystemKswapdRateExceedTimes = "system_kswapd_rate_exceed_times"
)

const (
	errMsgGetSystemMetrics          = "[memory-pressure-eviction-plugin] failed to get system metric, metric name: %s, err: %v"
	errMsgGetNumaMetrics            = "[memory-pressure-eviction-plugin] failed to get numa metric, metric name: %s, numa id: %d, err: %v"
	errMsgGetContainerNumaMetrics   = "[memory-pressure-eviction-plugin] failed to get container numa metric, metric name: %s, pod uid: %s, container name: %s, numa id: %d, err: %v"
	errMsgGetContainerSystemMetrics = "[memory-pressure-eviction-plugin] failed to get container system metric, metric name: %s, pod uid: %s, container name: %s, err: %v"
	errMsgCheckReclaimedPodFailed   = "[memory-pressure-eviction-plugin] failed to check reclaimed pod, pod: %s/%s, err: %v"
)

// MemoryPressureEvictionPlugin implements the EvictPlugin interface.
// It triggers pod eviction based on the pressure of memory.
type MemoryPressureEvictionPlugin struct {
	emitter                   metrics.MetricEmitter
	reclaimedPodFilter        func(pod *v1.Pod) (bool, error)
	evictionManagerSyncPeriod time.Duration
	pluginName                string
	*process.StopControl
	metaServer *metaserver.MetaServer

	memoryEvictionPluginConfig *evictionconfig.MemoryPressureEvictionPluginConfiguration

	numaActionMap                  map[int]int
	systemAction                   int
	numaFreeBelowWatermarkTimesMap map[int]int
	isUnderNumaPressure            bool
	isUnderSystemPressure          bool
	kswapdStealPreviousCycle       float64
	systemKswapdRateExceedTimes    int
}

// NewMemoryPressureEvictionPlugin returns a new MemoryPressureEvictionPlugin
func NewMemoryPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) EvictionPlugin {
	// use the given threshold to override the default configurations
	plugin := &MemoryPressureEvictionPlugin{
		pluginName:                     EvictionPluginNameMemoryPressure,
		emitter:                        emitter,
		StopControl:                    process.NewStopControl(time.Time{}),
		metaServer:                     metaServer,
		evictionManagerSyncPeriod:      conf.EvictionManagerSyncPeriod,
		memoryEvictionPluginConfig:     conf.MemoryPressureEvictionPluginConfiguration,
		reclaimedPodFilter:             conf.CheckReclaimedQoSForPod,
		numaActionMap:                  make(map[int]int),
		numaFreeBelowWatermarkTimesMap: make(map[int]int),
	}

	return plugin
}

// Name returns the name of MemoryPressureEvictionPlugin
func (m *MemoryPressureEvictionPlugin) Name() string {
	if m == nil {
		return ""
	}

	return m.pluginName
}

// ThresholdMet determines whether to evict pods based on memory pressure
func (m *MemoryPressureEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	if m.memoryEvictionPluginConfig.DynamicConf.EnableNumaLevelDetection() {
		m.detectNumaPressures()
	}

	if m.memoryEvictionPluginConfig.DynamicConf.EnableSystemLevelDetection() {
		m.detectSystemPressures()
	}

	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	if m.memoryEvictionPluginConfig.DynamicConf.EnableNumaLevelDetection() && m.isUnderNumaPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: evictionScopeMemory,
		}
	}

	if m.memoryEvictionPluginConfig.DynamicConf.EnableNumaLevelDetection() && m.isUnderSystemPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: evictionScopeMemory,
			Condition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
		}
	}

	klog.Infof("[memory-pressure-eviction-plugin] ThresholdMet result, m.isUnderNumaPressure: %+v, m.isUnderSystemPressure: %+v, "+
		"m.numaActionMap: %+v, m.systemAction: %+v", m.isUnderNumaPressure, m.isUnderSystemPressure,
		m.numaActionMap, m.systemAction)

	return resp, nil
}

// GetTopEvictionPods gets topN pods to evict
func (m *MemoryPressureEvictionPlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		klog.Warningf("[memory-pressure-eviction-plugin] GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	targetPods := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictMap := make(map[string]*v1.Pod)

	klog.Infof("[memory-pressure-eviction-plugin] GetTopEvictionPods condition, isUnderNumaPressure: %+v, m.isUnderSystemPressure: %+v, "+
		"m.numaActionMap: %+v, m.systemAction: %+v", m.isUnderNumaPressure, m.isUnderSystemPressure,
		m.numaActionMap, m.systemAction)

	if m.memoryEvictionPluginConfig.DynamicConf.EnableNumaLevelDetection() && m.isUnderNumaPressure {
		for numaID, action := range m.numaActionMap {
			m.selectPodsToEvict(request.ActivePods, request.TopN, numaID, action,
				m.memoryEvictionPluginConfig.DynamicConf.NumaEvictionRankingMetrics(), podToEvictMap)
		}
	}

	if m.memoryEvictionPluginConfig.DynamicConf.EnableSystemLevelDetection() && m.isUnderSystemPressure {
		m.selectPodsToEvict(request.ActivePods, request.TopN, nonExistNumaID, m.systemAction,
			m.memoryEvictionPluginConfig.DynamicConf.SystemEvictionRankingMetrics(), podToEvictMap)
	}

	for uid := range podToEvictMap {
		targetPods = append(targetPods, podToEvictMap[uid])
	}

	_ = m.emitter.StoreInt64(metricsNameNumberOfTargetPods, int64(len(targetPods)), metrics.MetricTypeNameRaw)
	klog.Infof("[memory-pressure-eviction-plugin] GetTopEvictionPods result, targetPods: %+v", native.GetNamespacedNameListFromSlice(targetPods))

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: targetPods,
	}
	if gracePeriod := m.memoryEvictionPluginConfig.DynamicConf.GracePeriod(); gracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return resp, nil
}

func (m *MemoryPressureEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}

func (m *MemoryPressureEvictionPlugin) detectNumaPressures() {
	m.isUnderNumaPressure = false
	for _, numaID := range m.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		m.numaActionMap[numaID] = actionNoop
		if _, ok := m.numaFreeBelowWatermarkTimesMap[numaID]; !ok {
			m.numaFreeBelowWatermarkTimesMap[numaID] = 0
		}

		if err := m.detectNumaWatermarkPressure(numaID); err != nil {
			continue
		}
	}
}

func (m *MemoryPressureEvictionPlugin) detectSystemPressures() {
	m.isUnderSystemPressure = false
	m.systemAction = actionNoop

	m.detectSystemWatermarkPressure()
	m.detectSystemKswapdStealPressure()

	switch m.systemAction {
	case actionReclaimedEviction:
		_ = m.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  evictionScopeMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelSystem,
				metricsTagKeyAction:         metricsTagValueActionReclaimedEviction,
			})...)
	case actionEviction:
		_ = m.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  evictionScopeMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelSystem,
				metricsTagKeyAction:         metricsTagValueActionEviction,
			})...)
	}
}

func (m *MemoryPressureEvictionPlugin) detectNumaWatermarkPressure(numaID int) error {
	free, total, scaleFactor, err := m.getWatermarkMetrics(numaID)
	if err != nil {
		klog.Errorf("[memory-pressure-eviction-plugin] failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
		_ = m.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(numaID),
			})...)
		return err
	}

	klog.Infof("[memory-pressure-eviction-plugin] numa watermark metrics of ID: %d, "+
		"free: %+v, total: %+v, scaleFactor: %+v, numaFreeBelowWatermarkTimes: %+v, numaFreeBelowWatermarkTimesThreshold: %+v",
		numaID, free, total, scaleFactor, m.numaFreeBelowWatermarkTimesMap[numaID],
		m.memoryEvictionPluginConfig.DynamicConf.NumaFreeBelowWatermarkTimesThreshold())
	_ = m.emitter.StoreFloat64(metricsNameNumaMetric, float64(m.numaFreeBelowWatermarkTimesMap[numaID]), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricsTagValueNumaFreeBelowWatermarkTimes,
		})...)

	if free < total*scaleFactor/10000 {
		m.isUnderNumaPressure = true
		m.numaActionMap[numaID] = actionReclaimedEviction
		m.numaFreeBelowWatermarkTimesMap[numaID]++
	} else {
		m.numaFreeBelowWatermarkTimesMap[numaID] = 0
	}

	if m.numaFreeBelowWatermarkTimesMap[numaID] >= m.memoryEvictionPluginConfig.DynamicConf.NumaFreeBelowWatermarkTimesThreshold() {
		m.numaActionMap[numaID] = actionEviction
	}

	switch m.numaActionMap[numaID] {
	case actionReclaimedEviction:
		_ = m.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  evictionScopeMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
				metricsTagKeyNumaID:         strconv.Itoa(numaID),
				metricsTagKeyAction:         metricsTagValueActionReclaimedEviction,
			})...)
	case actionEviction:
		_ = m.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  evictionScopeMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
				metricsTagKeyNumaID:         strconv.Itoa(numaID),
				metricsTagKeyAction:         metricsTagValueActionEviction,
			})...)
	}

	return nil
}

func (m *MemoryPressureEvictionPlugin) detectSystemWatermarkPressure() {
	free, total, scaleFactor, err := m.getWatermarkMetrics(nonExistNumaID)
	if err != nil {
		_ = m.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
			})...)
		klog.Errorf("[memory-pressure-eviction-plugin] failed to getWatermarkMetrics for system, err: %v", err)
		return
	}

	klog.Infof("[memory-pressure-eviction-plugin] system watermark metrics, "+
		"free: %+v, total: %+v, scaleFactor: %+v",
		free, total, scaleFactor)

	if free < total*scaleFactor/10000 {
		m.isUnderSystemPressure = true
		m.systemAction = actionReclaimedEviction
	}
}

func (m *MemoryPressureEvictionPlugin) detectSystemKswapdStealPressure() {
	kswapdSteal, err := m.getSystemKswapdStealMetrics()
	if err != nil {
		m.kswapdStealPreviousCycle = kswapdStealPreviousCycleMissing
		_ = m.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
			})...)
		klog.Errorf("[memory-pressure-eviction-plugin] failed to getSystemKswapdStealMetrics, err: %v", err)
		return
	}

	klog.Infof("[memory-pressure-eviction-plugin] system kswapd metrics, "+
		"kswapdSteal: %+v, kswapdStealPreviousCycle: %+v, systemKswapdRateThreshold: %+v, evictionManagerSyncPeriod: %+v, "+
		"systemKswapdRateExceedTimes: %+v, systemKswapdRateExceedTimesThreshold: %+v",
		kswapdSteal, m.kswapdStealPreviousCycle, m.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateThreshold(),
		m.evictionManagerSyncPeriod.Seconds(), m.systemKswapdRateExceedTimes,
		m.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateExceedTimesThreshold())
	_ = m.emitter.StoreFloat64(metricsNameSystemMetric, float64(m.systemKswapdRateExceedTimes), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: metricsTagValueSystemKswapdRateExceedTimes,
		})...)
	_ = m.emitter.StoreFloat64(metricsNameSystemMetric, kswapdSteal-m.kswapdStealPreviousCycle, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: metricsTagValueSystemKswapdDiff,
		})...)

	kswapdStealPreviousCycle := m.kswapdStealPreviousCycle
	m.kswapdStealPreviousCycle = kswapdSteal
	if kswapdStealPreviousCycle == kswapdStealPreviousCycleMissing {
		klog.Warning("[memory-pressure-eviction-plugin] kswapd steal of the previous cycle is missing")
		return
	}

	if kswapdSteal-kswapdStealPreviousCycle >= float64(m.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateThreshold())*m.evictionManagerSyncPeriod.Seconds() {
		m.systemKswapdRateExceedTimes++
	} else {
		m.systemKswapdRateExceedTimes = 0
	}

	if m.systemKswapdRateExceedTimes >= m.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateExceedTimesThreshold() {
		m.isUnderSystemPressure = true
		m.systemAction = actionEviction
	}
}

// getWatermarkMetrics returns system-water mark related metrics (config)
// if numa node is specified, return config in this numa; otherwise return system-level config
func (m *MemoryPressureEvictionPlugin) getWatermarkMetrics(numaID int) (free, total, scaleFactor float64, err error) {
	if numaID >= 0 {
		free, err = m.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemFreeNuma, numaID, err)
		}
		_ = m.emitter.StoreFloat64(metricsNameNumaMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID:     strconv.Itoa(numaID),
				metricsTagKeyMetricName: consts.MetricMemFreeNuma,
			})...)

		total, err = m.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetNumaMetrics, consts.MetricMemTotalNuma, numaID, err)
		}
		_ = m.emitter.StoreFloat64(metricsNameNumaMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID:     strconv.Itoa(numaID),
				metricsTagKeyMetricName: consts.MetricMemTotalNuma,
			})...)
	} else {
		free, err = m.metaServer.GetNodeMetric(consts.MetricMemFreeSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemFreeSystem, err)
		}
		_ = m.emitter.StoreFloat64(metricsNameSystemMetric, free, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: consts.MetricMemFreeSystem,
			})...)

		total, err = m.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
		if err != nil {
			return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemTotalSystem, err)
		}
		_ = m.emitter.StoreFloat64(metricsNameSystemMetric, total, metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: consts.MetricMemTotalSystem,
			})...)
	}

	scaleFactor, err = m.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return 0, 0, 0, fmt.Errorf(errMsgGetSystemMetrics, consts.MetricMemScaleFactorSystem, err)
	}
	_ = m.emitter.StoreFloat64(metricsNameSystemMetric, scaleFactor, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: consts.MetricMemScaleFactorSystem,
		})...)

	return free, total, scaleFactor, nil
}

func (m *MemoryPressureEvictionPlugin) getSystemKswapdStealMetrics() (float64, error) {
	kswapdSteal, err := m.metaServer.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	if err != nil {
		return 0, fmt.Errorf("[memory-pressure-eviction-plugin] failed to get mem.kswapdsteal.system, err: %v", err)
	}
	_ = m.emitter.StoreFloat64(metricsNameSystemMetric, kswapdSteal, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: consts.MetricMemKswapdstealSystem,
		})...)

	return kswapdSteal, nil
}

func (m *MemoryPressureEvictionPlugin) selectPodsToEvict(activePods []*v1.Pod, topN uint64, numaID,
	action int, rankingMetrics []string, podToEvictMap map[string]*v1.Pod) {
	filteredPods := m.filterPods(activePods, action)
	if filteredPods != nil {
		general.NewMultiSorter(m.getEvictionCmpFuncs(rankingMetrics, numaID)...).Sort(native.NewPodSourceImpList(filteredPods))
		for i := 0; uint64(i) < general.MinUInt64(topN, uint64(len(filteredPods))); i++ {
			podToEvictMap[string(filteredPods[i].UID)] = filteredPods[i]
		}
	}
}

func (m *MemoryPressureEvictionPlugin) filterPods(pods []*v1.Pod, action int) []*v1.Pod {
	switch action {
	case actionReclaimedEviction:
		return native.FilterPods(pods, m.reclaimedPodFilter)
	case actionEviction:
		return pods
	default:
		return nil
	}
}

// getEvictionCmpFuncs returns a comparison function list to judge the eviction order of different pods
func (m *MemoryPressureEvictionPlugin) getEvictionCmpFuncs(rankingMetrics []string, numaID int) []general.CmpFunc {
	cmpFuncs := make([]general.CmpFunc, 0, len(rankingMetrics))

	for _, metric := range rankingMetrics {
		currentMetric := metric
		cmpFuncs = append(cmpFuncs, func(s1, s2 interface{}) int {
			p1, p2 := s1.(*v1.Pod), s2.(*v1.Pod)
			switch currentMetric {
			case evictionconfig.FakeMetricQoSLevel:
				isReclaimedPod1, err1 := m.reclaimedPodFilter(p1)
				if err1 != nil {
					klog.Errorf(errMsgCheckReclaimedPodFailed, p1.Namespace, p1.Name, err1)
				}

				isReclaimedPod2, err2 := m.reclaimedPodFilter(p2)
				if err2 != nil {
					klog.Errorf(errMsgCheckReclaimedPodFailed, p2.Namespace, p2.Name, err2)
				}

				if err1 != nil || err2 != nil {
					// prioritize evicting the pod for which no error is returned
					return general.CmpError(err1, err2)
				}

				// prioritize evicting the pod whose QoS level is reclaimed_cores
				return general.CmpBool(isReclaimedPod1, isReclaimedPod2)
			case evictionconfig.FakeMetricPriority:
				// prioritize evicting the pod whose priority is lower
				return general.ReverseCmpFunc(native.PodPriorityCmpFunc)(p1, p2)
			default:
				p1Metric, p1Found := m.getPodMetric(p1, currentMetric, numaID)
				p2Metric, p2Found := m.getPodMetric(p2, currentMetric, numaID)
				if !p1Found || !p2Found {
					_ = m.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
						metrics.ConvertMapToTags(map[string]string{
							metricsTagKeyNumaID: strconv.Itoa(numaID),
						})...)
					// prioritize evicting the pod for which no stats were found
					return general.CmpBool(!p1Found, !p2Found)
				}

				// prioritize evicting the pod whose metric value is greater
				return general.CmpFloat64(p1Metric, p2Metric)
			}
		})
	}

	return cmpFuncs

}

// getPodMetric returns the value of a pod-level metric.
// And the value of a pod-level metric is calculated by summing the metric values for all containers in that pod.
func (m *MemoryPressureEvictionPlugin) getPodMetric(pod *v1.Pod, metricName string, numaID int) (float64, bool) {
	if pod == nil {
		return 0, false
	}

	var podMetricValue float64
	for _, container := range pod.Spec.Containers {
		var containerMetricValue float64
		var err error
		if numaID >= 0 {
			containerMetricValue, err = m.metaServer.GetContainerNumaMetric(string(pod.UID), container.Name, strconv.Itoa(numaID), metricName)
			if err != nil {
				klog.Errorf(errMsgGetContainerNumaMetrics, metricName, pod.UID, container.Name, numaID, err)
				return 0, false
			}
			_ = m.emitter.StoreFloat64(metricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyPodUID:        string(pod.UID),
					metricsTagKeyContainerName: container.Name,
					metricsTagKeyNumaID:        strconv.Itoa(numaID),
					metricsTagKeyMetricName:    metricName,
				})...)
		} else {
			containerMetricValue, err = m.metaServer.GetContainerMetric(string(pod.UID), container.Name, metricName)
			if err != nil {
				klog.Errorf(errMsgGetContainerSystemMetrics, metricName, pod.UID, container.Name, err)
				return 0, false
			}
			_ = m.emitter.StoreFloat64(metricsNameContainerMetric, containerMetricValue, metrics.MetricTypeNameRaw,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyPodUID:        string(pod.UID),
					metricsTagKeyContainerName: container.Name,
					metricsTagKeyNumaID:        strconv.Itoa(numaID),
					metricsTagKeyMetricName:    metricName,
				})...)
		}

		podMetricValue += containerMetricValue
	}

	_ = m.emitter.StoreFloat64(metricsNamePodMetric, podMetricValue, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyPodUID:     string(pod.UID),
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricName,
		})...)

	return podMetricValue, true
}
