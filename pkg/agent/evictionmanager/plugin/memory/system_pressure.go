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
	"math"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/events"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameSystemMemoryPressure = "system-memory-pressure-eviction-plugin"
	EvictionScopeSystemMemory              = "SystemMemory"
	evictionConditionMemoryPressure        = "MemoryPressure"
	syncTolerationTurns                    = 3
	minMemPressure                         = 1
	minPods                                = 3
)

func NewSystemPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	p := &SystemPressureEvictionPlugin{
		pluginName:                EvictionPluginNameSystemMemoryPressure,
		emitter:                   emitter,
		StopControl:               process.NewStopControl(time.Time{}),
		metaServer:                metaServer,
		supportedQosLevels:        sets.NewString(apiconsts.PodAnnotationQoSLevelDedicatedCores, apiconsts.PodAnnotationQoSLevelSharedCores),
		evictionManagerSyncPeriod: conf.EvictionManagerSyncPeriod,
		coolDownPeriod:            conf.SystemPressureCoolDownPeriod,
		syncPeriod:                time.Duration(conf.SystemPressureSyncPeriod) * time.Second,
		dynamicConfig:             conf.DynamicAgentConfiguration,
		qosConf:                   conf.QoSConfiguration,
		reclaimedPodFilter:        conf.CheckReclaimedQoSForPod,
		evictionHelper:            NewEvictionHelper(emitter, metaServer, conf),
		workloadPath:              conf.WorkloadPath,
		memSomeThreshold:          conf.MemPressureSomeThreshold,
		memFullThreshold:          conf.MemPressureFullThreshold,
		memPressureDuration:       conf.MemPressureDuration,
	}
	return p
}

// SystemPressureEvictionPlugin implements the EvictPlugin interface.
// It triggers pod eviction based on the system pressure of memory.
type SystemPressureEvictionPlugin struct {
	*process.StopControl
	sync.Mutex

	emitter                   metrics.MetricEmitter
	reclaimedPodFilter        func(pod *v1.Pod) (bool, error)
	evictionManagerSyncPeriod time.Duration
	pluginName                string
	metaServer                *metaserver.MetaServer
	evictionHelper            *EvictionHelper
	supportedQosLevels        sets.String

	syncPeriod     time.Duration
	coolDownPeriod int
	dynamicConfig  *dynamic.DynamicAgentConfiguration
	qosConf        *generic.QoSConfiguration

	systemAction                   int
	isUnderSystemPressure          bool
	kswapdStealPreviousCycle       float64
	kswapdStealPreviousCycleTime   time.Time
	kswapdStealRateExceedStartTime *time.Time
	lastEvictionTime               time.Time

	// memory pressure protection
	workloadMemHighPressureHitThresAt time.Time
	workloadPath                      string
	memSomeThreshold                  int
	memFullThreshold                  int
	memPressureDuration               int
}

func (s *SystemPressureEvictionPlugin) Name() string {
	if s == nil {
		return ""
	}

	return s.pluginName
}

func (s *SystemPressureEvictionPlugin) Start() {
	general.RegisterHeartbeatCheck(EvictionPluginNameSystemMemoryPressure, syncTolerationTurns*s.syncPeriod,
		general.HealthzCheckStateNotReady, syncTolerationTurns*s.syncPeriod)
	go wait.UntilWithContext(context.TODO(), s.detectSystemPressures, s.syncPeriod)
}

func (s *SystemPressureEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	dynamicConfig := s.dynamicConfig.GetDynamicConfiguration()
	if !dynamicConfig.EnableSystemLevelEviction {
		return resp, nil
	}

	// TODO maybe we should set timeout for this lock operation in case it blocks the entire sync loop
	s.Lock()
	defer s.Unlock()

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

	general.Infof("ThresholdMet result, m.isUnderSystemPressure: %+v, m.systemAction: %+v", s.isUnderSystemPressure, s.systemAction)

	return resp, nil
}

func (s *SystemPressureEvictionPlugin) detectSystemPressures(_ context.Context) {
	s.Lock()
	defer s.Unlock()
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(EvictionPluginNameSystemMemoryPressure, err)
	}()

	s.isUnderSystemPressure = false
	s.systemAction = actionNoop

	err = s.detectSystemWatermarkPressure()
	if common.CheckCgroup2UnifiedMode() {
		err = s.detectWorkloadMemPSI()
	}

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

func (s *SystemPressureEvictionPlugin) detectSystemWatermarkPressure() error {
	free, total, scaleFactor, err := helper.GetWatermarkMetrics(s.metaServer.MetricsFetcher, s.emitter, nonExistNumaID)
	if err != nil {
		_ = s.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
			})...)
		general.Errorf("failed to getWatermarkMetrics for system, err: %v", err)
		return err
	}

	thresholdMinimum := float64(s.dynamicConfig.GetDynamicConfiguration().SystemFreeMemoryThresholdMinimum)
	threshold := math.Max(thresholdMinimum, total*scaleFactor/10000)

	general.Infof("system watermark metrics, "+
		"free: %+v, total: %+v, scaleFactor: %+v, configuration minimum: %+v, final threshold: %+v",
		free, total, scaleFactor, thresholdMinimum, threshold)

	if free < threshold {
		s.isUnderSystemPressure = true
		s.systemAction = actionReclaimedEviction
	}
	return nil
}

func (s *SystemPressureEvictionPlugin) detectWorkloadMemPSI() error {
	workloadPath := s.workloadPath
	memSomeThreshold := s.memSomeThreshold
	memFullThreshold := s.memFullThreshold
	duration := s.memPressureDuration

	pressure, err := cgroupmgr.GetMemoryPressureWithAbsolutePath(workloadPath, common.SOME)
	if err != nil {
		general.Errorf("detectWorkloadMemPSI error: %v", err)
		s.workloadMemHighPressureHitThresAt = time.Time{}
		return err
	}

	now := time.Now()
	if pressure.Avg10 >= uint64(memSomeThreshold) {
		// calculate the mem.pressure of high qos jobs.
		ctx := context.Background()
		podList, err := s.metaServer.GetPodList(ctx, native.PodIsActive)
		if err != nil {
			general.Errorf("get pod list failed: %v", err)
			return err
		}

		var totalAvg10, avgAvg10, count, pressureCount uint64
		for _, pod := range podList {
			if pod == nil {
				continue
			}

			qosLevel, err := s.qosConf.GetQoSLevelForPod(pod)
			if err != nil {
				general.Errorf("get qos level failed for pod %+v/%+v, skip check rss overuse, err: %v", pod.Namespace, pod.Name, err)
				continue
			}

			if !s.supportedQosLevels.Has(qosLevel) {
				continue
			}

			podUID := string(pod.UID)
			absPath, err := common.GetPodAbsCgroupPath(common.CgroupSubsysMemory, podUID)
			if err != nil {
				continue
			}
			memPressure, err := cgroupmgr.GetMemoryPressureWithAbsolutePath(absPath, common.SOME)
			if err != nil {
				continue
			}
			if memPressure.Avg10 > minMemPressure {
				pressureCount++
			}
			totalAvg10 += memPressure.Avg10
			count++
		}

		if pressureCount < minPods {
			return nil
		}
		if count > 0 {
			avgAvg10 = totalAvg10 / count
		}

		if avgAvg10 >= uint64(memFullThreshold) {
			if s.workloadMemHighPressureHitThresAt.IsZero() {
				s.workloadMemHighPressureHitThresAt = now
			}
			diff := now.Sub(s.workloadMemHighPressureHitThresAt).Seconds()
			general.Infof("Detect high mem.pressure: psi=%v, time=%v, dur=%v.", avgAvg10, s.workloadMemHighPressureHitThresAt, uint64(diff))
			if uint64(diff) >= uint64(duration) {
				s.isUnderSystemPressure = true
				s.systemAction = actionReclaimedEviction
			}
			if uint64(diff) >= uint64(duration*3) {
				s.systemAction = actionEviction
			}
			general.Infof("Trigger action:%v, thresholdMet: psi=%+v, duration=%+v", s.systemAction, avgAvg10, uint64(diff))
		}
	} else {
		s.workloadMemHighPressureHitThresAt = time.Time{}
	}
	return nil
}

func (s *SystemPressureEvictionPlugin) detectSystemKswapdStealPressure() error {
	kswapdSteal, err := helper.GetNodeMetricWithTime(s.metaServer.MetricsFetcher, s.emitter, consts.MetricMemKswapdstealSystem)
	if err != nil {
		s.kswapdStealPreviousCycle = kswapdStealPreviousCycleMissing
		s.kswapdStealPreviousCycleTime = time.Now()
		_ = s.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(nonExistNumaID),
			})...)
		general.Errorf("failed to getSystemKswapdStealMetrics, err: %v", err)
		return err
	}

	if kswapdSteal.Time.Equal(s.kswapdStealPreviousCycleTime) {
		general.Warningf("getSystemKswapdStealMetrics get same result as last round,skip current round")
		return nil
	}

	dynamicConfig := s.dynamicConfig.GetDynamicConfiguration()
	general.Infof("system kswapd metrics, "+
		"kswapdSteal: %+v, kswapdStealPreviousCycle: %+v, kswapdStealPreviousCycleTime: %+v, systemKswapdRateThreshold: %+v, evictionManagerSyncPeriod: %+v, "+
		"kswapdStealRateExceedStartTime: %+v, SystemKswapdRateExceedDurationThreshold: %+v",
		kswapdSteal, s.kswapdStealPreviousCycle, s.kswapdStealPreviousCycleTime, dynamicConfig.SystemKswapdRateThreshold,
		s.evictionManagerSyncPeriod.Seconds(), s.kswapdStealRateExceedStartTime,
		dynamicConfig.SystemKswapdRateExceedDurationThreshold)
	if s.kswapdStealRateExceedStartTime != nil && !s.kswapdStealRateExceedStartTime.IsZero() {
		duration := kswapdSteal.Time.Sub(*s.kswapdStealRateExceedStartTime)
		_ = s.emitter.StoreFloat64(metricsNameSystemMetric, duration.Seconds(), metrics.MetricTypeNameRaw,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyMetricName: metricsTagValueSystemKswapdRateExceedDuration,
			})...)
	}
	_ = s.emitter.StoreFloat64(metricsNameSystemMetric, kswapdSteal.Value-s.kswapdStealPreviousCycle, metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyMetricName: metricsTagValueSystemKswapdDiff,
		})...)

	kswapdStealPreviousCycle := s.kswapdStealPreviousCycle
	kswapdStealPreviousCycleTime := s.kswapdStealPreviousCycleTime
	s.kswapdStealPreviousCycle = kswapdSteal.Value
	s.kswapdStealPreviousCycleTime = *(kswapdSteal.Time)
	if kswapdStealPreviousCycle == kswapdStealPreviousCycleMissing {
		general.Warningf("kswapd steal of the previous cycle is missing")
		return nil
	}

	if (kswapdSteal.Value-kswapdStealPreviousCycle)/(kswapdSteal.Time.Sub(kswapdStealPreviousCycleTime)).Seconds() >= float64(dynamicConfig.SystemKswapdRateThreshold) {
		// the pressure continues,if there is no recorded start time,we record the previous cycle time as the pressure start time
		if s.kswapdStealRateExceedStartTime == nil || s.kswapdStealRateExceedStartTime.IsZero() {
			s.kswapdStealRateExceedStartTime = &kswapdStealPreviousCycleTime
		}
	} else {
		// there is no pressure anymore, clear the start time
		s.kswapdStealRateExceedStartTime = nil
	}

	if s.kswapdStealRateExceedStartTime != nil && !s.kswapdStealRateExceedStartTime.IsZero() {
		pressureDuration := kswapdSteal.Time.Sub(*(s.kswapdStealRateExceedStartTime)).Seconds()
		if int(pressureDuration) >= dynamicConfig.SystemKswapdRateExceedDurationThreshold {
			s.isUnderSystemPressure = true
			s.systemAction = actionEviction
		}
	}
	return nil
}

func (s *SystemPressureEvictionPlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		general.Warningf("GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	now := time.Now()
	if !s.lastEvictionTime.IsZero() && now.Sub(s.lastEvictionTime) < time.Duration(s.coolDownPeriod)*time.Second {
		general.Infof("in eviction cool-down time, skip eviction. now: %s, lastEvictionTime: %s",
			now.String(), s.lastEvictionTime.String())
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}
	s.lastEvictionTime = now

	dynamicConfig := s.dynamicConfig.GetDynamicConfiguration()
	targetPods := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictMap := make(map[string]*v1.Pod)

	targetNumaID := nonExistNumaID
	var minFree uint64 = ^uint64(0)
	zoneinfo := machine.GetNormalZoneInfo(hostZoneInfoFile)
	for _, numaID := range s.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		if free := zoneinfo[numaID].Free; free < minFree {
			minFree = free
			targetNumaID = numaID
		}
	}

	general.Infof("GetTopEvictionPods condition,numa=%+v,  m.isUnderSystemPressure: %+v, "+
		"m.systemAction: %+v", targetNumaID, s.isUnderSystemPressure, s.systemAction)

	if dynamicConfig.EnableSystemLevelEviction && s.isUnderSystemPressure {
		s.evictionHelper.selectTopNPodsToEvictByMetrics(request.ActivePods, request.TopN, targetNumaID, s.systemAction,
			dynamicConfig.SystemEvictionRankingMetrics, podToEvictMap)
	}

	for uid := range podToEvictMap {
		targetPods = append(targetPods, podToEvictMap[uid])
	}

	_ = s.emitter.StoreInt64(metricsNameNumberOfTargetPods, int64(len(targetPods)), metrics.MetricTypeNameRaw)
	general.Infof("GetTopEvictionPods result, targetPods: %+v", native.GetNamespacedNameListFromSlice(targetPods))

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: targetPods,
	}
	if gracePeriod := dynamicConfig.MemoryPressureEvictionConfiguration.GracePeriod; gracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return resp, nil
}

func (s *SystemPressureEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
