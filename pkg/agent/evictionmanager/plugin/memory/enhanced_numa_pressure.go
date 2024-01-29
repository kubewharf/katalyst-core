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

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameEnhancedNumaMemoryPressure = "enhanced-numa-memory-pressure-eviction-plugin"
	EvictionScopeEnhancedNumaMemory              = "EnhancedNumaMemory"
)

// NewEnhancedNumaMemoryPressureEvictionPlugin returns a new MemoryPressureEvictionPlugin
func NewEnhancedNumaMemoryPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) plugin.EvictionPlugin {
	return &EnhancedNumaMemoryPressurePlugin{
		pluginName:               EvictionPluginNameEnhancedNumaMemoryPressure,
		emitter:                  emitter,
		StopControl:              process.NewStopControl(time.Time{}),
		metaServer:               metaServer,
		dynamicConfig:            conf.DynamicAgentConfiguration,
		reclaimedPodFilter:       conf.CheckReclaimedQoSForPod,
		numaActionMap:            make(map[int]int),
		numaPressureStatMap:      make(map[int]float64),
		numaKswapdStealLastCycle: make(map[int]float64),
		evictionHelper:           NewEvictionHelper(emitter, metaServer, conf),
	}
}

// EnhancedNumaMemoryPressurePlugin implements the EvictionPlugin interface
// It triggers pod eviction based on numa node memory pressure
type EnhancedNumaMemoryPressurePlugin struct {
	*process.StopControl

	emitter            metrics.MetricEmitter
	reclaimedPodFilter func(pod *v1.Pod) (bool, error)
	pluginName         string
	metaServer         *metaserver.MetaServer
	evictionHelper     *EvictionHelper

	dynamicConfig *dynamic.DynamicAgentConfiguration

	numaActionMap              map[int]int
	numaPressureStatMap        map[int]float64
	numaKswapdStealLastCycle   map[int]float64
	systemKswapdStealLastCycle float64
	isUnderNumaPressure        bool
}

func (m *EnhancedNumaMemoryPressurePlugin) Start() {
	return
}

func (m *EnhancedNumaMemoryPressurePlugin) Name() string {
	if m == nil {
		return ""
	}

	return m.pluginName
}

func (m *EnhancedNumaMemoryPressurePlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	// TODO: only one of enhanced-numa-level eviction numa-level eviction should be allowed to be enabled
	// TODO: replace numa-level eviction with enhanced-numa-level eviction after testing
	if !m.dynamicConfig.GetDynamicConfiguration().EnableEnhancedNumaLevelEviction {
		return resp, nil
	}

	m.detectNumaPressures()
	if m.isUnderNumaPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: EvictionScopeEnhancedNumaMemory,
		}
	}

	return resp, nil
}

func (m *EnhancedNumaMemoryPressurePlugin) detectNumaPressures() {
	m.isUnderNumaPressure = false
	systemKswapdSteal, err := m.metaServer.GetNodeMetric(consts.MetricMemKswapdstealSystem)
	if err != nil {
		general.Errorf("failed to get kswapd steal for system, err: %v", err)
		return
	}
	for _, numaID := range m.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		m.numaActionMap[numaID] = actionNoop
		numaFree, numaTotal, scaleFactor, err := helper.GetWatermarkMetrics(m.metaServer.MetricsFetcher, m.emitter, numaID)
		if err != nil {
			general.Errorf("failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
			continue
		}

		numaInactiveFile, err := m.metaServer.GetNumaMetric(numaID, consts.MetricMemInactiveFileNuma)
		if err != nil {
			general.Errorf("failed to get inactive file for numa %d, err: %v", numaID, err)
			continue
		}

		// TODO: get kswapd steal for numa
		general.Infof("numa metrics of ID: %d, "+
			"free: %+v, total: %+v, scaleFactor: %+v, inactiveFile: %+v, numaKswapdStealDelta: %+v",
			numaID, numaFree, numaTotal, scaleFactor, numaInactiveFile.Value, 0)

		m.numaPressureStatMap[numaID] = numaFree + numaInactiveFile.Value - 2*numaTotal*scaleFactor/10000
		// TODO: use numa kswapd steal to detect numa pressure instead of system kswapd steal
		if numaFree+numaInactiveFile.Value <= 2*numaTotal*scaleFactor/10000 && systemKswapdSteal.Value-m.systemKswapdStealLastCycle > 0 {
			m.isUnderNumaPressure = true
			m.numaActionMap[numaID] = actionEviction
			_ = m.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyEvictionScope:  EvictionScopeEnhancedNumaMemory,
					metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
					metricsTagKeyNumaID:         strconv.Itoa(numaID),
					metricsTagKeyAction:         metricsTagValueActionEviction,
				})...)
		}
	}
	m.systemKswapdStealLastCycle = systemKswapdSteal.Value
}

func (m *EnhancedNumaMemoryPressurePlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		general.Warningf("GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	dynamicConfig := m.dynamicConfig.GetDynamicConfiguration()
	targetPods := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictList := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictMap := make(map[string]*v1.Pod)

	general.Infof("GetTopEvictionPods condition, isUnderNumaPressure: %+v, m.numaActionMap: %+v",
		m.isUnderNumaPressure,
		m.numaActionMap)

	if dynamicConfig.EnableEnhancedNumaLevelEviction && m.isUnderNumaPressure {
		for numaID, action := range m.numaActionMap {
			candidates := m.getCandidates(request.ActivePods, numaID, dynamicConfig.NumaVictimMinimumUtilizationThreshold)
			m.evictionHelper.selectTopNPodsToEvictByMetrics(candidates, request.TopN, numaID, action,
				dynamicConfig.NumaEvictionRankingMetrics, podToEvictMap)
		}
	}

	for uid := range podToEvictMap {
		podToEvictList = append(podToEvictList, podToEvictMap[uid])
	}
	targetPods = m.getTargets(podToEvictList, targetPods)

	_ = m.emitter.StoreInt64(metricsNameNumberOfTargetPods, int64(len(targetPods)), metrics.MetricTypeNameRaw)
	general.Infof("[enhanced-numa-memory-pressure-eviction-plugin] GetTopEvictionPods result, targetPods: %+v", native.GetNamespacedNameListFromSlice(targetPods))

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

func (m *EnhancedNumaMemoryPressurePlugin) getTargets(podToEvictList []*v1.Pod, targetPods []*v1.Pod) []*v1.Pod {
	predictNotUnderPressure := func(predictNumaPressureStatMap map[int]float64) bool {
		for _, numaAvailMemSubtractWatermark := range predictNumaPressureStatMap {
			if numaAvailMemSubtractWatermark <= 0 {
				return false
			}
		}
		return true
	}
	releaseNumaPressureByEvictPod := func(predictNumaPressureStatMap map[int]float64, pod *v1.Pod) {
		for numaID := range predictNumaPressureStatMap {
			podNumaTotal, err := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, pod, consts.MetricsMemTotalPerNumaContainer, numaID)
			if err != nil {
				general.Infof("Failed to get pod  %+v total mem usage on numa id %+v", pod.UID, numaID)
				continue
			}
			// TODO: it's better to use sum of podNumaAnon and podNumaActiveFile to predict how much the pod can release the pressure
			//  since inactive file is already counted in predictNumaPressureStatMap, while cgroupv1 does not provide numa active/inactive statistics.
			//  So we falls back to use pod numa total.
			predictNumaPressureStatMap[numaID] += podNumaTotal
		}
	}
	getPodPermutations := func(podList []*v1.Pod) [][]*v1.Pod {
		var backtrack func([]*v1.Pod, int, *[][]*v1.Pod)
		backtrack = func(podList []*v1.Pod, start int, result *[][]*v1.Pod) {
			swap := func(podList []*v1.Pod, i, j int) {
				podList[i], podList[j] = podList[j], podList[i]
			}
			if start == len(podList) {
				permutation := make([]*v1.Pod, len(podList))
				copy(permutation, podList)
				*result = append(*result, permutation)
				general.Infof("permutation len %+v", len(permutation))

			} else {
				for i := start; i < len(podList); i++ {
					swap(podList, start, i)
					backtrack(podList, start+1, result)
					swap(podList, start, i)
				}
			}
		}
		var result [][]*v1.Pod
		backtrack(podList, 0, &result)
		return result
	}

	for _, permutation := range getPodPermutations(podToEvictList) {
		tmpPredictNumaPressureStatMap := general.DeepCopyIntFload64Map(m.numaPressureStatMap)
		var tmpTargetPods []*v1.Pod
		for _, pod := range permutation {
			if predictNotUnderPressure(tmpPredictNumaPressureStatMap) {
				break
			}
			releaseNumaPressureByEvictPod(tmpPredictNumaPressureStatMap, pod)
			tmpTargetPods = append(tmpTargetPods, pod)
		}
		if len(targetPods) == 0 || len(tmpTargetPods) < len(targetPods) {
			targetPods = tmpTargetPods
		}
	}
	return targetPods
}

// getCandidates returns pods which use memory more than minimumUsageThreshold.
func (m *EnhancedNumaMemoryPressurePlugin) getCandidates(pods []*v1.Pod, numaID int, minimumUsageThreshold float64) []*v1.Pod {
	result := make([]*v1.Pod, 0, len(pods))
	for i := range pods {
		pod := pods[i]
		totalMem, totalMemErr := helper.GetNumaMetric(m.metaServer.MetricsFetcher, m.emitter,
			consts.MetricMemTotalNuma, numaID)
		usedMem, usedMemErr := helper.GetPodMetric(m.metaServer.MetricsFetcher, m.emitter, pod,
			consts.MetricsMemTotalPerNumaContainer, numaID)
		if totalMemErr != nil || usedMemErr != nil {
			result = append(result, pod)
			continue
		}

		usedMemRatio := usedMem / totalMem
		if usedMemRatio < minimumUsageThreshold {
			general.Infof("pod %v/%v memory usage on numa %v is %v, which is lower than threshold %v, "+
				"ignore it", pod.Namespace, pod.Name, numaID, usedMemRatio, minimumUsageThreshold)
			continue
		}

		result = append(result, pod)
	}

	return result
}

func (m *EnhancedNumaMemoryPressurePlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
