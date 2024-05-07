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
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	evictionapi "k8s.io/kubernetes/pkg/kubelet/eviction/api"
	"k8s.io/kubernetes/pkg/kubelet/util/format"

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
	EvictionPluginNamePodRootfsPressure = "rootfs-pressure-eviction-plugin"
	EvictionScopeSystemRootfs           = "SystemRootfs"
	evictionConditionSystemRootfs       = "SystemRootfs"
	metricsNameReclaimPriorityCount     = "rootfs_reclaimed_pod_usage_priority_count"
)

type PodRootfsPressureEvictionPlugin struct {
	*process.StopControl
	pluginName    string
	dynamicConfig *dynamic.DynamicAgentConfiguration
	metaServer    *metaserver.MetaServer
	qosConf       *generic.QoSConfiguration
	emitter       metrics.MetricEmitter

	sync.RWMutex
	isMinimumFreeThresholdMet       bool
	isMinimumInodesFreeThresholdMet bool
}

func NewPodRootfsPressureEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	return &PodRootfsPressureEvictionPlugin{
		pluginName:    EvictionPluginNamePodRootfsPressure,
		metaServer:    metaServer,
		StopControl:   process.NewStopControl(time.Time{}),
		dynamicConfig: conf.DynamicAgentConfiguration,
		qosConf:       conf.GenericConfiguration.QoSConfiguration,
		emitter:       emitter,
	}
}

func (r *PodRootfsPressureEvictionPlugin) Name() string {
	if r == nil {
		return ""
	}
	return r.pluginName
}

func (r *PodRootfsPressureEvictionPlugin) Start() {
	return
}

func (r *PodRootfsPressureEvictionPlugin) ThresholdMet(_ context.Context) (*pluginapi.ThresholdMetResponse, error) {
	resp := &pluginapi.ThresholdMetResponse{
		MetType:       pluginapi.ThresholdMetType_NOT_MET,
		EvictionScope: EvictionScopeSystemRootfs,
	}

	rootfsEvictionConfig := r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration
	if !rootfsEvictionConfig.EnableRootfsPressureEviction {
		return resp, nil
	}

	isMinimumFreeThresholdMet := r.minimumFreeThresholdMet(rootfsEvictionConfig)
	isMinimumInodesFreeThresholdMet := r.minimumInodesFreeThresholdMet(rootfsEvictionConfig)
	r.Lock()
	r.isMinimumFreeThresholdMet = isMinimumFreeThresholdMet
	r.isMinimumInodesFreeThresholdMet = isMinimumInodesFreeThresholdMet
	r.Unlock()

	if isMinimumFreeThresholdMet || isMinimumInodesFreeThresholdMet {
		return &pluginapi.ThresholdMetResponse{
			MetType:       pluginapi.ThresholdMetType_HARD_MET,
			EvictionScope: EvictionScopeSystemRootfs,
			Condition: &pluginapi.Condition{
				ConditionType: pluginapi.ConditionType_NODE_CONDITION,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: evictionConditionSystemRootfs,
				MetCondition:  true,
			},
		}, nil
	}

	return resp, nil
}

func (r *PodRootfsPressureEvictionPlugin) minimumFreeThresholdMet(rootfsEvictionConfig *eviction.RootfsPressureEvictionConfiguration) bool {
	if rootfsEvictionConfig == nil || rootfsEvictionConfig.MinimumImageFsFreeThreshold == nil {
		return false
	}

	imageFsFreeBytes, errAvailable := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsAvailable)
	if errAvailable != nil {
		general.Warningf("Failed to get MetricsImageFsAvailable: %q", errAvailable)
		return false
	}
	imageFsCapacityBytes, errCapacity := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsCapacity)
	if errCapacity != nil {
		general.Warningf("Failed to get MetricsImageFsCapacity: %q", errCapacity)
		return false
	}

	if rootfsEvictionConfig.MinimumImageFsDiskCapacityThreshold != nil && int64(imageFsCapacityBytes) < rootfsEvictionConfig.MinimumImageFsDiskCapacityThreshold.Value() {
		general.Warningf("Ignore this node for MinimumImageFsDiskCapacityThreshold (size: %d, threshold: %d)", int64(imageFsCapacityBytes), rootfsEvictionConfig.MinimumImageFsDiskCapacityThreshold.Value())
		return false
	}

	if rootfsEvictionConfig.MinimumImageFsFreeThreshold.Quantity != nil {
		// free <  rootfsEvictionConfig.MinimumFreeInBytesThreshold -> met
		if int64(imageFsFreeBytes) < rootfsEvictionConfig.MinimumImageFsFreeThreshold.Quantity.Value() {
			general.Infof("ThresholdMet result, Reason: MinimumImageFsFreeInBytesThreshold (Available: %d, Threshold: %d)", int64(imageFsFreeBytes), rootfsEvictionConfig.MinimumImageFsFreeThreshold.Quantity.Value())
			return true
		}
	} else {
		// free/capacity < rootfsEvictionConfig.MinimumFreeRateThreshold -> met
		if imageFsFreeBytes > imageFsCapacityBytes || imageFsCapacityBytes == 0 {
			general.Warningf("Invalid system rootfs metrics: %d/%d", int64(imageFsFreeBytes), int64(imageFsCapacityBytes))
			return false
		}
		ratio := imageFsFreeBytes / imageFsCapacityBytes
		if ratio < float64(rootfsEvictionConfig.MinimumImageFsFreeThreshold.Percentage) {
			general.Infof("ThresholdMet result, Reason: MinimumImageFsFreeRateThreshold (Rate: %04f, Threshold: %04f)", ratio, rootfsEvictionConfig.MinimumImageFsFreeThreshold.Percentage)
			return true
		}
	}

	return false
}

func (r *PodRootfsPressureEvictionPlugin) minimumInodesFreeThresholdMet(rootfsEvictionConfig *eviction.RootfsPressureEvictionConfiguration) bool {
	if rootfsEvictionConfig == nil || rootfsEvictionConfig.MinimumImageFsInodesFreeThreshold == nil {
		return false
	}

	if rootfsEvictionConfig.MinimumImageFsInodesFreeThreshold.Quantity != nil {
		systemInodesFree, err := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsInodesFree)
		if err != nil {
			general.Warningf("Failed to get MetricsImageFsInodesFree: %q", err)
		} else {
			if int64(systemInodesFree) < rootfsEvictionConfig.MinimumImageFsInodesFreeThreshold.Quantity.Value() {
				general.Infof("ThresholdMet result, Reason: MinimumImageFsInodesFreeThreshold (Free: %d, Threshold: %d)", int64(systemInodesFree), rootfsEvictionConfig.MinimumImageFsInodesFreeThreshold.Quantity.Value())
				return true
			}
		}
	} else {
		systemInodesFree, errInodesFree := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsInodesFree)
		systemInodes, errInodes := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsInodes)
		switch {
		case errInodesFree != nil:
			general.Warningf("Failed to get MetricsImageFsInodesFree: %q", errInodesFree)
		case errInodes != nil:
			general.Warningf("Failed to get MetricsImageFsInodes: %q", errInodes)
		case systemInodesFree > systemInodes || systemInodes == 0:
			general.Warningf("Invalid system rootfs inodes metric: %d/%d", int64(systemInodesFree), int64(systemInodes))
		default:
			rate := systemInodesFree / systemInodes
			if rate < float64(rootfsEvictionConfig.MinimumImageFsInodesFreeThreshold.Percentage) {
				general.Infof("ThresholdMet result, Reason: MinimumImageFsInodesFreeRateThreshold (Rate: %04f, Threshold: %04f)", rate, rootfsEvictionConfig.MinimumImageFsInodesFreeThreshold.Percentage)
				return true
			}
		}
	}

	return false
}

func (r *PodRootfsPressureEvictionPlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		general.Warningf("GetTopEvictionPods got empty active pods list")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	rootfsEvictionConfig := r.dynamicConfig.GetDynamicConfiguration().RootfsPressureEvictionConfiguration
	if !rootfsEvictionConfig.EnableRootfsPressureEviction {
		general.Warningf("GetTopEvictionPods RootfsPressureEviction is disabled")
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	r.RLock()
	isMinimumFreeThresholdMet := r.isMinimumFreeThresholdMet
	isMinimumInodesFreeThresholdMet := r.isMinimumInodesFreeThresholdMet
	r.RUnlock()

	var pods []*v1.Pod
	var err error
	if isMinimumFreeThresholdMet {
		pods, err = r.getTopNPods(request.ActivePods, request.TopN, rootfsEvictionConfig.PodMinimumUsedThreshold, rootfsEvictionConfig.ReclaimedQoSPodUsedPriorityThreshold, r.getPodRootfsUsed)
	} else if isMinimumInodesFreeThresholdMet {
		pods, err = r.getTopNPods(request.ActivePods, request.TopN, rootfsEvictionConfig.PodMinimumInodesUsedThreshold, rootfsEvictionConfig.ReclaimedQoSPodInodesUsedPriorityThreshold, r.getPodRootfsInodesUsed)
	}
	if err != nil {
		general.Warningf("GetTopEvictionPods get TopN pods failed: %q", err)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	if len(pods) == 0 {
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	resp := &pluginapi.GetTopEvictionPodsResponse{
		TargetPods: pods,
	}
	if gracePeriod := rootfsEvictionConfig.GracePeriod; gracePeriod > 0 {
		resp.DeletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return resp, nil
}

func (r *PodRootfsPressureEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}

type podUsageItem struct {
	usage    int64
	capacity int64
	priority bool
	pod      *v1.Pod
}

type podUsageList []podUsageItem

func (l podUsageList) Less(i, j int) bool {
	if l[i].priority && !l[j].priority {
		return true
	}
	if !l[i].priority && l[j].priority {
		return false
	}
	return l[i].usage > l[j].usage
}

func (l podUsageList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

func (l podUsageList) Len() int {
	return len(l)
}

func (r *PodRootfsPressureEvictionPlugin) podMinimumUsageProtectionMet(usage int64, percentage float64, minUsedThreshold *evictionapi.ThresholdValue) bool {
	if minUsedThreshold == nil {
		return false
	}
	if minUsedThreshold.Quantity != nil {
		return usage < minUsedThreshold.Quantity.Value()
	} else {
		return percentage < float64(minUsedThreshold.Percentage)
	}
}

func (r *PodRootfsPressureEvictionPlugin) reclaimedPodPriorityEvictionMet(pod *v1.Pod, used int64, percentage float64, reclaimedPodPriorityUsedThreshold *evictionapi.ThresholdValue) bool {
	if reclaimedPodPriorityUsedThreshold == nil {
		return false
	}
	isReclaimedPod, err := r.qosConf.CheckReclaimedQoSForPod(pod)
	if err != nil {
		general.Warningf("isReclaimedPod: pod UID: %s, error: %q", pod.UID, err)
		return false
	}
	if !isReclaimedPod {
		return false
	}
	if reclaimedPodPriorityUsedThreshold.Quantity != nil {
		return used > reclaimedPodPriorityUsedThreshold.Quantity.Value()
	} else {
		return percentage > float64(reclaimedPodPriorityUsedThreshold.Percentage)
	}
}

type getPodRootfsUsageFunc func(pod *v1.Pod) (int64, int64, error)

func (r *PodRootfsPressureEvictionPlugin) getTopNPods(pods []*v1.Pod, n uint64, minUsedThreshold, reclaimedPodPriorityUsedThreshold *evictionapi.ThresholdValue, getPodRootfsUsageFunc getPodRootfsUsageFunc) ([]*v1.Pod, error) {
	var usageItemList podUsageList

	for i := range pods {
		usageItem := podUsageItem{
			pod: pods[i],
		}

		used, capacity, err := getPodRootfsUsageFunc(pods[i])
		if err != nil {
			general.Warningf("Failed to get pod rootfs usage for %s: %q", pods[i].UID, err)
		} else {
			percentage := float64(used) / float64(capacity)
			usageItem.usage = used
			usageItem.capacity = capacity
			usageItem.priority = r.reclaimedPodPriorityEvictionMet(pods[i], used, percentage, reclaimedPodPriorityUsedThreshold)

			if !usageItem.priority {
				if r.podMinimumUsageProtectionMet(used, percentage, minUsedThreshold) {
					continue
				}
			}
			usageItemList = append(usageItemList, usageItem)
		}
	}

	if uint64(len(usageItemList)) > n {
		sort.Sort(usageItemList)
		usageItemList = usageItemList[:n]
	}

	var results []*v1.Pod
	for _, item := range usageItemList {
		general.Infof("Rootfs Eviction Request(Pod: %s, Used: %d, Capacity: %d, Priority: %v)", format.Pod(item.pod), item.usage, item.capacity, item.priority)
		if item.priority {
			_ = r.emitter.StoreInt64(metricsNameReclaimPriorityCount, 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					"uid":       string(item.pod.UID),
					"namespace": item.pod.Namespace,
					"name":      item.pod.Name,
					"used":      fmt.Sprintf("%d", item.usage),
					"capacity":  fmt.Sprintf("%d", item.capacity),
				})...)
		}
		results = append(results, item.pod)
	}
	return results, nil
}

func (r *PodRootfsPressureEvictionPlugin) getPodRootfsUsed(pod *v1.Pod) (int64, int64, error) {
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

func (r *PodRootfsPressureEvictionPlugin) getPodRootfsInodesUsed(pod *v1.Pod) (int64, int64, error) {
	podRootfsInodesUsed, err := helper.GetPodMetric(r.metaServer.MetricsFetcher, r.emitter, pod, consts.MetricsContainerRootfsInodesUsed, -1)
	if err != nil {
		return 0, 0, err
	}

	rootfsInodes, err := helper.GetNodeMetric(r.metaServer.MetricsFetcher, r.emitter, consts.MetricsImageFsInodes)
	if err != nil {
		return 0, 0, err
	}
	if rootfsInodes < 1 {
		return 0, 0, errors.New("invalid rootfs inodes")
	}

	return int64(podRootfsInodesUsed), int64(rootfsInodes), nil
}
