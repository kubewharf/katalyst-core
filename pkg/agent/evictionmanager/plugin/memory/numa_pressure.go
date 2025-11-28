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
	memLowReservedSize = 4 * 1024 * 1024 * 1024 // 4G
	memGapSize         = 1 * 1024 * 1024 * 1024 // 1G
	memGuardSize       = 256 * 1024 * 1024      // 256M

	minDuration = 2
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

		memLowReservePages: uint64(metaServer.KatalystMachineInfo.MemoryTopology.AlignToPageSize(memLowReservedSize)),
		memGapPages:        uint64(metaServer.KatalystMachineInfo.MemoryTopology.AlignToPageSize(memGapSize)),
		memGuardPages:      uint64(metaServer.KatalystMachineInfo.MemoryTopology.AlignToPageSize(memGuardSize)),
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

	// guard pages for numa.mem.pressure detection
	memLowReservePages uint64 // minimum allowed mem.low
	memGapPages        uint64 // the depth of the probe, used to judge the pressure
	memGuardPages      uint64 // a buffer depth to prevent kswpad ping-pong
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

func (n *NumaMemoryPressurePlugin) ThresholdMet(_ context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
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
			min := zoneinfo[numaID].Min
			fileInactive := zoneinfo[numaID].FileInactive

			// Notice: adding a buffer range for mem.low/mem.min to avoid kswapd ping-pong
			low += n.memGuardPages
			min += n.memGapPages

			// step2, add a compensation mechanism to prevent system thrashing due to insufficient file memory
			fileReserved := general.Max(int(n.memLowReservePages), int(low))
			if fileInactive < uint64(fileReserved) {
				tmp := low + n.memGapPages
				low = uint64(general.Max(int(n.memLowReservePages), int(tmp)))
			}

			// step3, compare mem.free, mem.low, mem.min
			if err := n.detectNumaWatermarkPressure(numaID, int(zoneinfo[numaID].Free), int(min), int(low)); err != nil {
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
	return free < (min+low)/2 || (low > int(n.memGapPages) && free < (low-int(n.memGapPages)))
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
	_ = n.emitter.StoreFloat64(metricsNameNumaMetric, float64(n.numaFreeBelowWatermarkTimesMap[numaID]), metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{
			metricsTagKeyNumaID:     strconv.Itoa(numaID),
			metricsTagKeyMetricName: metricsTagValueNumaFreeBelowWatermarkTimes,
		})...)

	// Notice:
	// numaFreeBelowWatermarkTimes > NumaFreeBelowWatermarkTimesReclaimedThreshold, trigger actionReclaimedEviction
	// numaFreeBelowWatermarkTimes > NumaFreeBelowWatermarkTimesThreshold, trigger actionEviction
	// By default, the reclaimed threshold is half online.
	actionReclaimedThreshold := dynamicConfig.NumaFreeBelowWatermarkTimesReclaimedThreshold
	actionOnlineThreshold := dynamicConfig.NumaFreeBelowWatermarkTimesThreshold

	if free < min {
		// We are under a DANGEROUS situation, NEED A EVICTION NOW! IMMEDIATELY!
		n.isUnderNumaPressure = true
		n.numaActionMap[numaID] = actionReclaimedEviction
		if n.numaFreeBelowWatermarkTimesMap[numaID] < actionOnlineThreshold {
			n.numaFreeBelowWatermarkTimesMap[numaID] = actionOnlineThreshold - dynamicConfig.NumaFreeConstraintFastEvictionWaitCycle
		}
	} else if free < low {
		n.numaFreeBelowWatermarkTimesMap[numaID]++

		// avoid excessive pressure on LRU spinlock in kswapd
		if n.numaFreeBelowWatermarkTimesMap[numaID] > minDuration && n.isUnderAdditionalPressure(free, min, low) {
			n.numaFreeBelowWatermarkTimesMap[numaID]++
		}

		// Currently, the default value for NumaFreeBelowWatermarkTimesThreshold is 20,
		// with a default periodic check interval of 5 seconds.
		// This means if the free memory remains below the low watermark for 50 seconds, actionReclaimedEviction will be triggered.
		// If the condition persists for 100 seconds, actionEviction will be triggered.
		if n.numaFreeBelowWatermarkTimesMap[numaID] > actionReclaimedThreshold {
			n.isUnderNumaPressure = true
			n.numaActionMap[numaID] = actionReclaimedEviction
		}
	} else {
		// added cooling mechanism to avoid kswapd ping-pong
		if n.numaFreeBelowWatermarkTimesMap[numaID] > 0 {
			n.numaFreeBelowWatermarkTimesMap[numaID]--
			if n.numaFreeBelowWatermarkTimesMap[numaID] > actionReclaimedThreshold {
				n.numaFreeBelowWatermarkTimesMap[numaID] = actionReclaimedThreshold
			}
		}
	}
	if n.numaFreeBelowWatermarkTimesMap[numaID] >= actionOnlineThreshold {
		n.numaActionMap[numaID] = actionEviction
	}

	general.Infof("numa ID: %d, "+
		"free pages: %+v, min: %+v, low: %+v, belowTimes: %+v, numaReclaimedThreshold: %+v, numaOnlineThreshold: %+v",
		numaID, free, min, low, n.numaFreeBelowWatermarkTimesMap[numaID],
		actionReclaimedThreshold, actionOnlineThreshold)

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
			candidates := n.evictionHelper.getCandidates(request.ActivePods, numaID, dynamicConfig.NumaVictimMinimumUtilizationThreshold)
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

	// TODO: different grace time for different evictions.
	// Currently, grace time is aligned with actionEviction in the case of mixed evictions.
	gracePeriod := dynamicConfig.MemoryPressureEvictionConfiguration.ReclaimedGracePeriod
	for _, action := range n.numaActionMap {
		if action == actionEviction {
			gracePeriod = dynamicConfig.MemoryPressureEvictionConfiguration.GracePeriod
			break
		}
	}

	if gracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}
	return resp, nil
}

func (n *NumaMemoryPressurePlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
