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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameSystemMemoryPressure = "system-memory-pressure-eviction-plugin"
	EvictionScopeSystemMemory              = "SystemMemory"
	evictionConditionMemoryPressure        = "MemoryPressure"
)

func NewSystemPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) plugin.EvictionPlugin {
	return &SystemPressureEvictionPlugin{
		pluginName:                 EvictionPluginNameSystemMemoryPressure,
		emitter:                    emitter,
		StopControl:                process.NewStopControl(time.Time{}),
		metaServer:                 metaServer,
		evictionManagerSyncPeriod:  conf.EvictionManagerSyncPeriod,
		memoryEvictionPluginConfig: conf.MemoryPressureEvictionPluginConfiguration,
		reclaimedPodFilter:         conf.CheckReclaimedQoSForPod,
		evictionHelper:             NewEvictionHelper(emitter, metaServer, conf),
	}
}

// SystemPressureEvictionPlugin implements the EvictPlugin interface.
// It triggers pod eviction based on the system pressure of memory.
type SystemPressureEvictionPlugin struct {
	*process.StopControl

	emitter                   metrics.MetricEmitter
	reclaimedPodFilter        func(pod *v1.Pod) (bool, error)
	evictionManagerSyncPeriod time.Duration
	pluginName                string
	metaServer                *metaserver.MetaServer
	evictionHelper            *EvictionHelper

	memoryEvictionPluginConfig *evictionconfig.MemoryPressureEvictionPluginConfiguration

	systemAction                int
	isUnderSystemPressure       bool
	kswapdStealPreviousCycle    float64
	systemKswapdRateExceedTimes int
}

func (s *SystemPressureEvictionPlugin) Name() string {
	if s == nil {
		return ""
	}

	return s.pluginName
}

func (s *SystemPressureEvictionPlugin) ThresholdMet(c context.Context) (*pluginapi.ThresholdMetResponse, error) {
	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	if !s.memoryEvictionPluginConfig.DynamicConf.EnableSystemLevelDetection() {
		return resp, nil
	}

	s.detectSystemPressures()
	if s.isUnderSystemPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: EvictionScopeSystemMemory,
			Condition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionMemoryPressure,
				MetCondition:  true,
			},
		}
	}

	klog.Infof("[system-memory-pressure-eviction-plugin] ThresholdMet result, m.isUnderSystemPressure: %+v, m.systemAction: %+v", s.isUnderSystemPressure, s.systemAction)

	return resp, nil
}

func (s *SystemPressureEvictionPlugin) detectSystemPressures() {
	s.isUnderSystemPressure = false
	s.systemAction = actionNoop

	s.detectSystemWatermarkPressure()
	s.detectSystemKswapdStealPressure()

	switch s.systemAction {
	case actionReclaimedEviction:
		_ = s.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  EvictionScopeSystemMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelSystem,
				metricsTagKeyAction:         metricsTagValueActionReclaimedEviction,
			})...)
	case actionEviction:
		_ = s.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  EvictionScopeSystemMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelSystem,
				metricsTagKeyAction:         metricsTagValueActionEviction,
			})...)
	}
}

func (s *SystemPressureEvictionPlugin) detectSystemWatermarkPressure() {
	free, total, scaleFactor, err := s.evictionHelper.getWatermarkMetrics(nonExistNumaID)
	if err != nil {
		_ = s.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
			})...)
		klog.Errorf("[system-memory-pressure-eviction-plugin] failed to getWatermarkMetrics for system, err: %v", err)
		return
	}

	klog.Infof("[system-memory-pressure-eviction-plugin] system watermark metrics, "+
		"free: %+v, total: %+v, scaleFactor: %+v",
		free, total, scaleFactor)

	if free < total*scaleFactor/10000 {
		s.isUnderSystemPressure = true
		s.systemAction = actionReclaimedEviction
	}
}

func (s *SystemPressureEvictionPlugin) detectSystemKswapdStealPressure() {
	kswapdSteal, err := s.evictionHelper.getSystemKswapdStealMetrics()
	if err != nil {
		s.kswapdStealPreviousCycle = kswapdStealPreviousCycleMissing
		_ = s.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
			})...)
		klog.Errorf("[system-memory-pressure-eviction-plugin] failed to getSystemKswapdStealMetrics, err: %v", err)
		return
	}

	klog.Infof("[system-memory-pressure-eviction-plugin] system kswapd metrics, "+
		"kswapdSteal: %+v, kswapdStealPreviousCycle: %+v, systemKswapdRateThreshold: %+v, evictionManagerSyncPeriod: %+v, "+
		"systemKswapdRateExceedTimes: %+v, systemKswapdRateExceedTimesThreshold: %+v",
		kswapdSteal, s.kswapdStealPreviousCycle, s.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateThreshold(),
		s.evictionManagerSyncPeriod.Seconds(), s.systemKswapdRateExceedTimes,
		s.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateExceedTimesThreshold())
	_ = s.emitter.StoreFloat64(metricsNameSystemMetric, float64(s.systemKswapdRateExceedTimes), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: metricsTagValueSystemKswapdRateExceedTimes,
		})...)
	_ = s.emitter.StoreFloat64(metricsNameSystemMetric, kswapdSteal-s.kswapdStealPreviousCycle, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: metricsTagValueSystemKswapdDiff,
		})...)

	kswapdStealPreviousCycle := s.kswapdStealPreviousCycle
	s.kswapdStealPreviousCycle = kswapdSteal
	if kswapdStealPreviousCycle == kswapdStealPreviousCycleMissing {
		klog.Warning("[system-memory-pressure-eviction-plugin] kswapd steal of the previous cycle is missing")
		return
	}

	if kswapdSteal-kswapdStealPreviousCycle >= float64(s.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateThreshold())*s.evictionManagerSyncPeriod.Seconds() {
		s.systemKswapdRateExceedTimes++
	} else {
		s.systemKswapdRateExceedTimes = 0
	}

	if s.systemKswapdRateExceedTimes >= s.memoryEvictionPluginConfig.DynamicConf.SystemKswapdRateExceedTimesThreshold() {
		s.isUnderSystemPressure = true
		s.systemAction = actionEviction
	}
}

func (s *SystemPressureEvictionPlugin) GetTopEvictionPods(c context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		klog.Warningf("[system-memory-pressure-eviction-plugin] GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	targetPods := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictMap := make(map[string]*v1.Pod)

	klog.Infof("[system-memory-pressure-eviction-plugin] GetTopEvictionPods condition, m.isUnderSystemPressure: %+v, "+
		"m.systemAction: %+v", s.isUnderSystemPressure, s.systemAction)

	if s.memoryEvictionPluginConfig.DynamicConf.EnableSystemLevelDetection() && s.isUnderSystemPressure {
		s.evictionHelper.selectPodsToEvict(request.ActivePods, request.TopN, nonExistNumaID, s.systemAction,
			s.memoryEvictionPluginConfig.DynamicConf.SystemEvictionRankingMetrics(), podToEvictMap)
	}

	for uid := range podToEvictMap {
		targetPods = append(targetPods, podToEvictMap[uid])
	}

	_ = s.emitter.StoreInt64(metricsNameNumberOfTargetPods, int64(len(targetPods)), metrics.MetricTypeNameRaw)
	klog.Infof("[system-memory-pressure-eviction-plugin] GetTopEvictionPods result, targetPods: %+v", native.GetNamespacedNameListFromSlice(targetPods))

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: targetPods,
	}
	if gracePeriod := s.memoryEvictionPluginConfig.DynamicConf.GracePeriod(); gracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return resp, nil
}

func (s *SystemPressureEvictionPlugin) GetEvictPods(c context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
