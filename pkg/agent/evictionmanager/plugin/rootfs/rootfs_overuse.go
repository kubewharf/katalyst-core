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

package rootfs

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	evictionapi "k8s.io/kubernetes/pkg/kubelet/eviction/api"

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
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	EvictionPluginNamePodRootfsOveruse = "rootfs-overuse-eviction-plugin"

	minRootfsOveruseThreshold                   = 20 * 1024 * 1024 * 1024 // 20GB
	minRootfsOverusePercentageThreshold float32 = 0.1
)

// PodRootfsOveruseEvictionPlugin implements the EvictPlugin interface.
type PodRootfsOveruseEvictionPlugin struct {
	*process.StopControl
	pluginName    string
	dynamicConfig *dynamic.DynamicAgentConfiguration
	metaServer    *metaserver.MetaServer
	qosConf       *generic.QoSConfiguration
	emitter       metrics.MetricEmitter
}

func NewPodRootfsOveruseEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	return &PodRootfsOveruseEvictionPlugin{
		pluginName:    EvictionPluginNamePodRootfsOveruse,
		metaServer:    metaServer,
		StopControl:   process.NewStopControl(time.Time{}),
		dynamicConfig: conf.DynamicAgentConfiguration,
		qosConf:       conf.GenericConfiguration.QoSConfiguration,
		emitter:       emitter,
	}
}

func (r *PodRootfsOveruseEvictionPlugin) Name() string {
	if r == nil {
		return ""
	}

	return r.pluginName
}

func (r *PodRootfsOveruseEvictionPlugin) Start() {}

func (r *PodRootfsOveruseEvictionPlugin) ThresholdMet(_ context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (r *PodRootfsOveruseEvictionPlugin) GetTopEvictionPods(_ context.Context, _ *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	return &pluginapi.GetTopEvictionPodsResponse{}, nil
}

func (r *PodRootfsOveruseEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	rootfsEvictionConfig := r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration
	if !rootfsEvictionConfig.EnableRootfsOveruseEviction {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	if len(request.ActivePods) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	filterPods := r.filterPods(request.ActivePods)

	var usageItemList podUsageList
	for _, pod := range filterPods {
		threshold := r.getRootfsOveruseThreshold(pod)
		if threshold == nil {
			continue
		}

		used, capacity, err := r.getPodRootfsUsed(pod)
		if err != nil {
			general.Warningf("Failed to get pod rootfs usage for %s: %q", pod.UID, err)
			continue
		}

		if rootfsOveruseThresholdMet(used, capacity, threshold) {
			podUsageItem := podUsageItem{
				pod:       pod,
				usage:     used,
				capacity:  capacity,
				threshold: getThresholdValue(threshold, capacity),
			}
			usageItemList = append(usageItemList, podUsageItem)
		}
	}

	if len(usageItemList) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	sort.Sort(usageItemList)
	result := make([]*pluginapi.EvictPod, 0)
	deletionOptions := &pluginapi.DeletionOptions{
		GracePeriodSeconds: rootfsEvictionConfig.GracePeriod,
	}
	for i := 0; i < rootfsEvictionConfig.RootfsOveruseEvictionCount && i < len(usageItemList); i++ {
		item := usageItemList[i]
		evictPod := &pluginapi.EvictPod{
			Pod:    item.pod,
			Reason: fmt.Sprintf("rootfs overuse threshold met, used: %d, threshold: %d", item.usage, item.threshold),
		}
		if deletionOptions.GracePeriodSeconds > 0 {
			evictPod.DeletionOptions = deletionOptions
		}
		result = append(result, evictPod)
		general.InfoS("rootfs overuse threshold met", "pod", item.pod.Name, "used", item.usage, "threshold", item.threshold)
	}
	return &pluginapi.GetEvictPodsResponse{EvictPods: result}, nil
}

func (r *PodRootfsOveruseEvictionPlugin) getPodRootfsUsed(pod *v1.Pod) (int64, int64, error) {
	podRootfsUsed, err := helper.GetPodMetric(r.metaServer.MetricsFetcher, r.emitter, pod, consts.MetricsContainerRootfsUsed, -1)
	if err != nil {
		return 0, 0, err
	}

	rootfsCapacity, err := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsCapacity)
	if err != nil {
		return 0, 0, err
	}

	if rootfsCapacity < 1 {
		return 0, 0, errors.New("invalid rootfs capacity")
	}

	return int64(podRootfsUsed), int64(rootfsCapacity), nil
}

func (r *PodRootfsOveruseEvictionPlugin) getRootfsOveruseThreshold(pod *v1.Pod) *evictionapi.ThresholdValue {
	qosLevel, err := r.qosConf.GetQoSLevelForPod(pod)
	if err != nil {
		return nil
	}

	switch qosLevel {
	case string(apiconsts.QoSLevelSharedCores):
		return getRootfsOveruseThreshold(r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration.SharedQoSRootfsOveruseThreshold)
	case string(apiconsts.QoSLevelReclaimedCores):
		return getRootfsOveruseThreshold(r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration.ReclaimedQoSRootfsOveruseThreshold)
	default:
	}
	return nil
}

func (r *PodRootfsOveruseEvictionPlugin) filterPods(pods []*v1.Pod) []*v1.Pod {
	supportedQoSLevels := sets.NewString(r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration.RootfsOveruseEvictionSupportedQoSLevels...)
	sharedQoSNamespaceFilter := sets.NewString(r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration.SharedQoSNamespaceFilter...)

	filteredPods := make([]*v1.Pod, 0, len(pods))
	for _, pod := range pods {
		qosLevel, err := r.qosConf.GetQoSLevelForPod(pod)
		if err != nil {
			continue
		}
		if !supportedQoSLevels.Has(qosLevel) {
			continue
		}
		if qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores && !sharedQoSNamespaceFilter.Has(pod.Namespace) {
			continue
		}
		filteredPods = append(filteredPods, pod)
	}
	return filteredPods
}

func rootfsOveruseThresholdMet(used, capacity int64, threshold *evictionapi.ThresholdValue) bool {
	if threshold == nil {
		return false
	}
	if thresholdValue := getThresholdValue(threshold, capacity); used > thresholdValue {
		return true
	}
	return false
}

func getThresholdValue(threshold *evictionapi.ThresholdValue, capacity int64) int64 {
	if threshold == nil {
		return 0
	}
	if threshold.Quantity != nil {
		return threshold.Quantity.Value()
	}
	if threshold.Percentage > 0 && threshold.Percentage < 1 {
		return int64(float32(capacity) * threshold.Percentage)
	}
	return 0
}

func getRootfsOveruseThreshold(threshold *evictionapi.ThresholdValue) *evictionapi.ThresholdValue {
	if threshold == nil {
		return nil
	}
	if threshold.Quantity != nil && threshold.Quantity.Value() < minRootfsOveruseThreshold {
		return &evictionapi.ThresholdValue{
			Quantity: resource.NewQuantity(minRootfsOveruseThreshold, resource.BinarySI),
		}
	}
	if threshold.Percentage > 0 && threshold.Percentage < 1 && threshold.Percentage < minRootfsOverusePercentageThreshold {
		return &evictionapi.ThresholdValue{
			Percentage: minRootfsOverusePercentageThreshold,
		}
	}
	return threshold
}
