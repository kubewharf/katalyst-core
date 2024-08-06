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
	"k8s.io/apimachinery/pkg/util/errors"
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
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNameNumaMemoryPressure = "numa-memory-pressure-eviction-plugin"
	EvictionScopeNumaMemory              = "NumaMemory"
)

const (
	memLowReservePages = 1048576 // 4G
	memGapPages        = 262144  // 1G
)

var hostZoneInfoFile = "/proc/zoneinfo"

// NewNumaMemoryPressureEvictionPlugin returns a new MemoryPressureEvictionPlugin
func NewNumaMemoryPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
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
	general.RegisterHeartbeatCheck(EvictionPluginNameNumaMemoryPressure, healthCheckTimeout, general.HealthzCheckStateNotReady, healthCheckTimeout)
	return
}

func (n *NumaMemoryPressurePlugin) Name() string {
	if n == nil {
		return ""
	}

	return n.pluginName
}

func (n *NumaMemoryPressurePlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	var err error
	defer func() {
		_ = general.UpdateHealthzStateByError(EvictionPluginNameNumaMemoryPressure, err)
	}()

	resp := &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}

	if !n.dynamicConfig.GetDynamicConfiguration().EnableNumaLevelEviction {
		return resp, nil
	}

	err = n.detectNumaPressures()
	if n.isUnderNumaPressure {
		resp = &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: EvictionScopeNumaMemory,
		}
	}

	return resp, nil
}

func (n *NumaMemoryPressurePlugin) detectNumaPressures() error {
	var errList []error
	n.isUnderNumaPressure = false

	// step1, check zoneinfo
	zoneinfo := machine.GetNormalZoneInfo(hostZoneInfoFile)
	for _, numaID := range n.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		n.numaActionMap[numaID] = actionNoop
		if _, ok := n.numaFreeBelowWatermarkTimesMap[numaID]; !ok {
			n.numaFreeBelowWatermarkTimesMap[numaID] = 0
		}

		// Notice: the unit of free/min/low from zoneinfo is pages.
		if numaID < len(zoneinfo) && zoneinfo[numaID].Node == int64(numaID) {
			low := zoneinfo[numaID].Low
			fileInactive := zoneinfo[numaID].FileInactive

			// step2, add a compensation mechanism to prevent system thrashing due to insufficient file memory
			fileReserved := general.Max(memLowReservePages, int(low))
			if fileInactive < uint64(fileReserved) {
				tmp := low + memGapPages
				low = uint64(general.Max(memLowReservePages, int(tmp)))
			}

			// step3, compare mem.free, mem.low, mem.min
			if err := n.detectNumaWatermarkPressure(numaID, int(zoneinfo[numaID].Free), int(zoneinfo[numaID].Min), int(low)); err != nil {
				errList = append(errList, err)
				continue
			}
		} else {
			// If there is no watermark info in zoneinfo, fall back to applying info from metaServer
			if err := n.detectNumaWatermarkPressure(numaID, 0, 0, 0); err != nil {
				errList = append(errList, err)
				continue
			}
		}
	}

	return errors.NewAggregate(errList)
}

func (n *NumaMemoryPressurePlugin) isUnderAdditionalPressure(free, min, low int) bool {
	return free < (min+low)/2 || (low > memGapPages && free < (low-memGapPages))
}

func (n *NumaMemoryPressurePlugin) detectNumaWatermarkPressure(numaID, free, min, low int) error {
	if low <= 0 {
		// If there is no watermark info in zoneinfo, fall back to applying info from metaServer
		// Notice: the unit of freeMem here is bytes.
		freeBytes, totalBytes, scaleFactor, err := helper.GetWatermarkMetrics(n.metaServer.MetricsFetcher, n.emitter, numaID)
		if err != nil {
			general.Errorf("failed to getWatermarkMetrics for numa %d, err: %v", numaID, err)
			_ = n.emitter.StoreInt64(metricsNameFetchMetricError, 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					metricsTagKeyNumaID: strconv.Itoa(numaID),
				})...)
			return err
		}
		free = general.ConvertBytesToPages(int(freeBytes))
		low = general.ConvertBytesToPages(int(totalBytes * scaleFactor / 10000))
	}

	dynamicConfig := n.dynamicConfig.GetDynamicConfiguration()
	general.Infof("numa watermark metrics of ID: %d, "+
		"free pages: %+v, min pages: %+v, low pages: %+v, numaFreeBelowWatermarkTimes: %+v, numaFreeBelowWatermarkTimesThreshold: %+v",
		numaID, free, min, low, n.numaFreeBelowWatermarkTimesMap[numaID],
		dynamicConfig.NumaFreeBelowWatermarkTimesThreshold)
	_ = n.emitter.StoreFloat64(metricsNameNumaMetric, float64(n.numaFreeBelowWatermarkTimesMap[numaID]), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricsTagValueNumaFreeBelowWatermarkTimes,
		})...)

	if free < low {
		n.numaFreeBelowWatermarkTimesMap[numaID]++
		if n.numaFreeBelowWatermarkTimesMap[numaID] > (dynamicConfig.NumaFreeBelowWatermarkTimesThreshold / 2) {
			n.isUnderNumaPressure = true
			n.numaActionMap[numaID] = actionReclaimedEviction
		}

		// avoid excessive pressure on LRU spinlock in kswapd
		if n.numaFreeBelowWatermarkTimesMap[numaID] > 3 && n.isUnderAdditionalPressure(free, min, low) {
			n.numaFreeBelowWatermarkTimesMap[numaID]++
		}
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
		if totalMemErr != nil {
			continue
		}
		usedMem, usedMemErr := helper.GetPodMetric(n.metaServer.MetricsFetcher, n.emitter, pod,
			consts.MetricsMemTotalPerNumaContainer, numaID)
		if usedMemErr != nil {
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
