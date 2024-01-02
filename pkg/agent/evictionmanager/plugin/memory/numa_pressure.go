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
	EvictionPluginNameNumaMemoryPressure = "numa-memory-pressure-eviction-plugin"
	EvictionScopeNumaMemory              = "NumaMemory"
)

// NewNumaMemoryPressureEvictionPlugin returns a new MemoryPressureEvictionPlugin
func NewNumaMemoryPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) plugin.EvictionPlugin {
	return &NumaMemoryPressurePlugin{
		pluginName:                     EvictionPluginNameNumaMemoryPressure,
		emitter:                        emitter,
		StopControl:                    process.NewStopControl(time.Time{}),
		metaServer:                     metaServer,
		dynamicConfig:                  conf.DynamicAgentConfiguration,
		reclaimedPodFilter:             conf.CheckReclaimedQoSForPod,
		numaActionMap:                  make(map[int]int),
		numaFreeBelowWatermarkTimesMap: make(map[int]int),
		evictionHelper:                 NewEvictionHelper(emitter, metaServer, conf),
	}
}

// NumaMemoryPressurePlugin implements the EvictionPlugin interface
// It triggers pod eviction based on the numa node pressure
type NumaMemoryPressurePlugin struct {
	*process.StopControl

	emitter            metrics.MetricEmitter
	reclaimedPodFilter func(pod *v1.Pod) (bool, error)
	pluginName         string
	metaServer         *metaserver.MetaServer
	evictionHelper     *EvictionHelper

	dynamicConfig *dynamic.DynamicAgentConfiguration

	numaActionMap                  map[int]int
	numaFreeBelowWatermarkTimesMap map[int]int
	isUnderNumaPressure            bool
}

func (n *NumaMemoryPressurePlugin) Start() {
	return
}

func (n *NumaMemoryPressurePlugin) Name() string {
	if n == nil {
		return ""
	}

	return n.pluginName
}

func (n *NumaMemoryPressurePlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	if !n.dynamicConfig.GetDynamicConfiguration().EnableNumaLevelEviction {
		return resp, nil
	}

	n.detectNumaPressures()
	if n.isUnderNumaPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: EvictionScopeNumaMemory,
		}
	}

	return resp, nil
}

func (n *NumaMemoryPressurePlugin) detectNumaPressures() {
	n.isUnderNumaPressure = false
	for _, numaID := range n.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		n.numaActionMap[numaID] = actionNoop
		if _, ok := n.numaFreeBelowWatermarkTimesMap[numaID]; !ok {
			n.numaFreeBelowWatermarkTimesMap[numaID] = 0
		}

		if err := n.detectNumaWatermarkPressure(numaID); err != nil {
			continue
		}
	}
}

func (n *NumaMemoryPressurePlugin) detectNumaWatermarkPressure(numaID int) error {
	free, total, scaleFactor, err := helper.GetWatermarkMetrics(n.metaServer.MetricsFetcher, n.emitter, numaID)
	if err != nil {
		general.Errorf("failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
		_ = n.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyNumaID: strconv.Itoa(numaID),
			})...)
		return err
	}

	dynamicConfig := n.dynamicConfig.GetDynamicConfiguration()
	general.Infof("numa watermark metrics of ID: %d, "+
		"free: %+v, total: %+v, scaleFactor: %+v, numaFreeBelowWatermarkTimes: %+v, numaFreeBelowWatermarkTimesThreshold: %+v",
		numaID, free, total, scaleFactor, n.numaFreeBelowWatermarkTimesMap[numaID],
		dynamicConfig.NumaFreeBelowWatermarkTimesThreshold)
	_ = n.emitter.StoreFloat64(metricsNameNumaMetric, float64(n.numaFreeBelowWatermarkTimesMap[numaID]), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricsTagValueNumaFreeBelowWatermarkTimes,
		})...)

	if free < total*scaleFactor/10000 {
		n.isUnderNumaPressure = true
		n.numaActionMap[numaID] = actionReclaimedEviction
		n.numaFreeBelowWatermarkTimesMap[numaID]++
	} else {
		n.numaFreeBelowWatermarkTimesMap[numaID] = 0
	}

	if n.numaFreeBelowWatermarkTimesMap[numaID] >= dynamicConfig.NumaFreeBelowWatermarkTimesThreshold {
		n.numaActionMap[numaID] = actionEviction
	}

	switch n.numaActionMap[numaID] {
	case actionReclaimedEviction:
		_ = n.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  EvictionScopeNumaMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
				metricsTagKeyNumaID:         strconv.Itoa(numaID),
				metricsTagKeyAction:         metricsTagValueActionReclaimedEviction,
			})...)
	case actionEviction:
		_ = n.emitter.StoreInt64(metricsNameThresholdMet, 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				metricsTagKeyEvictionScope:  EvictionScopeNumaMemory,
				metricsTagKeyDetectionLevel: metricsTagValueDetectionLevelNuma,
				metricsTagKeyNumaID:         strconv.Itoa(numaID),
				metricsTagKeyAction:         metricsTagValueActionEviction,
			})...)
	}

	return nil
}

func (n *NumaMemoryPressurePlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		general.Warningf("GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	dynamicConfig := n.dynamicConfig.GetDynamicConfiguration()
	targetPods := make([]*v1.Pod, 0, len(request.ActivePods))
	podToEvictMap := make(map[string]*v1.Pod)

	general.Infof("GetTopEvictionPods condition, isUnderNumaPressure: %+v, n.numaActionMap: %+v",
		n.isUnderNumaPressure,
		n.numaActionMap)

	if dynamicConfig.EnableNumaLevelEviction && n.isUnderNumaPressure {
		for numaID, action := range n.numaActionMap {
			candidates := n.getCandidates(request.ActivePods, numaID, dynamicConfig.NumaVictimMinimumUtilizationThreshold)
			n.evictionHelper.selectTopNPodsToEvictByMetrics(candidates, request.TopN, numaID, action,
				dynamicConfig.NumaEvictionRankingMetrics, podToEvictMap)
		}
	}

	for uid := range podToEvictMap {
		targetPods = append(targetPods, podToEvictMap[uid])
	}

	_ = n.emitter.StoreInt64(metricsNameNumberOfTargetPods, int64(len(targetPods)), metrics.MetricTypeNameRaw)
	general.Infof("[numa-memory-pressure-eviction-plugin] GetTopEvictionPods result, targetPods: %+v", native.GetNamespacedNameListFromSlice(targetPods))

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

// getCandidates returns pods which use memory more than minimumUsageThreshold.
func (n *NumaMemoryPressurePlugin) getCandidates(pods []*v1.Pod, numaID int, minimumUsageThreshold float64) []*v1.Pod {
	result := make([]*v1.Pod, 0, len(pods))
	for i := range pods {
		pod := pods[i]
		totalMem, totalMemErr := helper.GetNumaMetric(n.metaServer.MetricsFetcher, n.emitter,
			consts.MetricMemTotalNuma, numaID)
		usedMem, usedMemErr := helper.GetPodMetric(n.metaServer.MetricsFetcher, n.emitter, pod,
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

func (n *NumaMemoryPressurePlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
