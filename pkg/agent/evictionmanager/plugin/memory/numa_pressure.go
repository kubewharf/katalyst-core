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
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupcmutils "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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
	p := &NumaMemoryPressurePlugin{
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
	p.numaIDPodFilter = p.filterByNumaId
	return p
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

	// numaIDPodFilter field make it can be replaced in unit test
	numaIDPodFilter func([]*v1.Pod, int) []*v1.Pod
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
	free, total, scaleFactor, err := n.evictionHelper.getWatermarkMetrics(numaID)
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

func (n *NumaMemoryPressurePlugin) filterByNumaId(pods []*v1.Pod, numaID int) []*v1.Pod {
	result := make([]*v1.Pod, 0, len(pods))
	for i, pod := range pods {
		if len(pod.Spec.Containers) <= 0 {
			continue
		}

		for _, container := range pod.Spec.Containers {

			containerID, err := n.metaServer.GetContainerID(string(pod.UID), container.Name)
			if err != nil {
				general.Warningf("get container id for container %v/%v/%v failed, err: %v", pod.Namespace, pod.Name, container.Name, err)
				continue
			}

			cpusetStatus, cpuSetErr := cgroupcmutils.GetCPUSetForContainer(string(pod.UID), containerID)
			if cpuSetErr != nil {
				general.Warningf("get cpuset for container %v/%v/%v failed, err: %v", pod.Namespace, pod.Name, container.Name, err)
				continue
			}

			memNodes, parseErr := machine.Parse(cpusetStatus.Mems)
			if parseErr != nil {
				general.Warningf("parse cpuset.mems for container %v/%v/%v failed, err: %v", pod.Namespace, pod.Name, container.Name, err)
				continue
			}

			if memNodes.Contains(numaID) {
				result = append(result, pods[i])
			}
		}

	}

	return result
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
			pods := n.numaIDPodFilter(request.ActivePods, numaID)
			general.Infof("get %v pod related to numa %v from %v pods", len(pods), numaID, len(request.ActivePods))
			n.evictionHelper.selectTopNPodsToEvictByMetrics(pods, request.TopN, numaID, action,
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

func (n *NumaMemoryPressurePlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
