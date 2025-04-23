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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
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

	rootfsOveruseEvictionConfig        *eviction.RootfsPressureEvictionConfiguration
	sharedQosRootfsOveruseThreshold    *evictionapi.ThresholdValue
	reclaimedQosRootfsOveruseThreshold *evictionapi.ThresholdValue
	supportedQosLevels                 sets.String
	sharedQoSNamespaceFilter           sets.String
}

func NewPodRootfsOveruseEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	rootfsOveruseEvictionConfig := conf.GetDynamicConfiguration().RootfsPressureEvictionConfiguration
	general.InfoS("[rootfsOveruseEvictionConfig]:",
		"EnableRootfsOveruseEviction", rootfsOveruseEvictionConfig.EnableRootfsOveruseEviction,
		"RootfsOveruseEvictionSupportedQoSLevels", rootfsOveruseEvictionConfig.RootfsOveruseEvictionSupportedQoSLevels,
		"SharedQoSNamespaceFilter", rootfsOveruseEvictionConfig.SharedQoSNamespaceFilter,
		"SharedQoSRootfsOveruseThreshold", rootfsOveruseEvictionConfig.SharedQoSRootfsOveruseThreshold,
		"ReclaimedQoSRootfsOveruseThreshold", rootfsOveruseEvictionConfig.ReclaimedQoSRootfsOveruseThreshold,
		"RootfsOveruseEvictionCount", rootfsOveruseEvictionConfig.RootfsOveruseEvictionCount)

	return &PodRootfsOveruseEvictionPlugin{
		pluginName:                         EvictionPluginNamePodRootfsOveruse,
		metaServer:                         metaServer,
		StopControl:                        process.NewStopControl(time.Time{}),
		dynamicConfig:                      conf.DynamicAgentConfiguration,
		qosConf:                            conf.GenericConfiguration.QoSConfiguration,
		emitter:                            emitter,
		rootfsOveruseEvictionConfig:        rootfsOveruseEvictionConfig,
		sharedQosRootfsOveruseThreshold:    getRootfsOveruseThreshold(rootfsOveruseEvictionConfig.SharedQoSRootfsOveruseThreshold),
		reclaimedQosRootfsOveruseThreshold: getRootfsOveruseThreshold(rootfsOveruseEvictionConfig.ReclaimedQoSRootfsOveruseThreshold),
		supportedQosLevels:                 sets.NewString(rootfsOveruseEvictionConfig.RootfsOveruseEvictionSupportedQoSLevels...),
		sharedQoSNamespaceFilter:           sets.NewString(rootfsOveruseEvictionConfig.SharedQoSNamespaceFilter...),
	}
}

func (r *PodRootfsOveruseEvictionPlugin) Name() string {
	if r == nil {
		return ""
	}

	return r.pluginName
}

func (r *PodRootfsOveruseEvictionPlugin) Start() {}

func (r *PodRootfsOveruseEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
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

	rootfsEvictionConfig := r.rootfsOveruseEvictionConfig
	if !rootfsEvictionConfig.EnableRootfsOveruseEviction {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	if len(request.ActivePods) == 0 {
		return &pluginapi.GetEvictPodsResponse{}, nil
	}

	filterPods := make([]*v1.Pod, 0, len(request.ActivePods))
	for _, pod := range request.ActivePods {
		qosLevel, err := r.qosConf.GetQoSLevelForPod(pod)
		if err != nil {
			continue
		}
		if !r.supportedQosLevels.Has(qosLevel) {
			continue
		}
		filter := r.sharedQoSNamespaceFilter
		if qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores && len(filter) > 0 && !filter.Has(pod.Namespace) {
			continue
		}
		filterPods = append(filterPods, pod)
	}

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
	for i := 0; i < rootfsEvictionConfig.RootfsOveruseEvictionCount && i < len(usageItemList); i++ {
		item := usageItemList[i]
		result = append(result, &pluginapi.EvictPod{
			Pod:    item.pod,
			Reason: fmt.Sprintf("rootfs overuse threshold met, used: %d, threshold: %d", item.usage, item.threshold),
			DeletionOptions: &pluginapi.DeletionOptions{
				GracePeriodSeconds: rootfsEvictionConfig.GracePeriod,
			},
		})
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
		return r.sharedQosRootfsOveruseThreshold
	case string(apiconsts.QoSLevelReclaimedCores):
		return r.reclaimedQosRootfsOveruseThreshold
	default:
	}
	return nil
}

func rootfsOveruseThresholdMet(used, capacity int64, threshold *evictionapi.ThresholdValue) bool {
	if threshold == nil {
		return false
	}
	if threshold.Quantity != nil {
		return used > threshold.Quantity.Value()
	}
	if threshold.Percentage > 0 && threshold.Percentage < 1 {
		return float32(used) > float32(capacity)*threshold.Percentage
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
