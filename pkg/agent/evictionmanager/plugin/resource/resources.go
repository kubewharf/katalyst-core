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

package resource

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	MetricsNamePodCount         = "pod_count"
	MetricsNamePodResource      = "pod_resource"
	MetricsNameGetResourceEmpty = "get_resource_empty"

	defaultHealthCheckTimeout = 1 * time.Minute
)

type ResourcesGetter func(ctx context.Context) (v1.ResourceList, error)

type ThresholdGetter func(resourceName v1.ResourceName) *float64

type GracePeriodGetter func() int64

// ResourcesEvictionPlugin implements EvictPlugin interface it trigger
// pod eviction logic based on the tolerance of resources.
type ResourcesEvictionPlugin struct {
	// emitter is used to emit metrics.
	emitter metrics.MetricEmitter

	// thresholdGetter is used to get the threshold of resources.
	thresholdGetter                     ThresholdGetter
	resourcesGetter                     ResourcesGetter
	deletionGracePeriodGetter           GracePeriodGetter
	thresholdMetToleranceDurationGetter GracePeriodGetter

	skipZeroQuantityResourceNames sets.String
	podFilter                     func(pod *v1.Pod) (bool, error)

	pluginName string

	metaServer *metaserver.MetaServer
}

func NewResourcesEvictionPlugin(pluginName string, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, resourcesGetter ResourcesGetter, thresholdGetter ThresholdGetter,
	deletionGracePeriodGetter GracePeriodGetter, thresholdMetToleranceDurationGetter GracePeriodGetter,
	skipZeroQuantityResourceNames sets.String,
	podFilter func(pod *v1.Pod) (bool, error),
) *ResourcesEvictionPlugin {
	// use the given threshold to override the default configurations
	plugin := &ResourcesEvictionPlugin{
		pluginName:                          pluginName,
		emitter:                             emitter,
		metaServer:                          metaServer,
		resourcesGetter:                     resourcesGetter,
		thresholdGetter:                     thresholdGetter,
		deletionGracePeriodGetter:           deletionGracePeriodGetter,
		thresholdMetToleranceDurationGetter: thresholdMetToleranceDurationGetter,
		skipZeroQuantityResourceNames:       skipZeroQuantityResourceNames,
		podFilter:                           podFilter,
	}
	return plugin
}

func (b *ResourcesEvictionPlugin) Name() string {
	if b == nil {
		return ""
	}

	return b.pluginName
}

func (b *ResourcesEvictionPlugin) Start() {
	general.RegisterHeartbeatCheck(ReclaimedResourcesEvictionPluginName, defaultHealthCheckTimeout, general.HealthzCheckStateNotReady, defaultHealthCheckTimeout)
	return
}

// ThresholdMet evict pods when the beset effort resources usage is greater than
// the supply (after considering toleration).
func (b *ResourcesEvictionPlugin) ThresholdMet(ctx context.Context) (*pluginapi.ThresholdMetResponse, error) {
	activePods, err := b.metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		errMsg := fmt.Sprintf("failed to list pods from metaServer: %v", err)
		klog.Errorf("[%s] %s", b.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	filteredPods := native.FilterPods(activePods, b.podFilter)

	klog.Infof("[%s] total %d filtered pods out-of %d running pods", b.pluginName, len(filteredPods), len(activePods))

	_ = b.emitter.StoreInt64(MetricsNamePodCount, int64(len(filteredPods)), metrics.MetricTypeNameRaw)
	if len(filteredPods) == 0 {
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	allocatable, err := b.resourcesGetter(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get resources: %v", err)
		klog.Errorf("[%s] %s", b.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	native.EmitResourceMetrics(MetricsNamePodResource, allocatable, map[string]string{
		"type": "allocatable",
	}, b.emitter)

	// allocatable resources in cnr status is empty and it's a abnormal case.
	// to avoid evict mistakenly, we return threshold not met, and emit metrics.
	// [TODO] we should consider refining it when actually finding cases.
	if allocatable == nil || len(allocatable) == 0 {
		_ = b.emitter.StoreInt64(MetricsNameGetResourceEmpty, 1, metrics.MetricTypeNameCount, metrics.MetricTag{
			Key: "pluginName", Val: b.pluginName,
		})
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	// use requests (rather than limits) as used resource
	usedResources := make(v1.ResourceList)
	for _, pod := range filteredPods {
		resources := native.SumUpPodRequestResources(pod)
		usedResources = native.AddResources(usedResources, resources)

		native.EmitResourceMetrics(MetricsNamePodResource, resources, map[string]string{
			"pluginName": b.pluginName,
			"namespace":  pod.Namespace,
			"name":       pod.Name,
			"type":       "pod",
		}, b.emitter)
	}
	klog.Infof("[%s] resources: allocatable %+v usedResources %+v", b.pluginName, allocatable, usedResources)

	native.EmitResourceMetrics(MetricsNamePodResource, usedResources, map[string]string{
		"pluginName": b.pluginName,
		"type":       "used",
	}, b.emitter)

	for resourceName, usedQuantity := range usedResources {
		totalQuantity, ok := allocatable[resourceName]
		if !ok {
			klog.Warningf("[%s] used resource: %s doesn't exist in allocatable", b.pluginName, resourceName)
			continue
		}

		total := float64((&totalQuantity).Value())

		if total <= 0 && b.skipZeroQuantityResourceNames.Has(string(resourceName)) {
			klog.Warningf("[%s] skip resource: %s with total: %.2f", b.pluginName, total)
			continue
		}

		used := float64((&usedQuantity).Value())

		// get resource threshold (i.e. tolerance) for each resource
		// if nil, eviction will not be triggered.
		thresholdRate := b.thresholdGetter(resourceName)
		if thresholdRate == nil {
			continue
		}

		thresholdValue := *thresholdRate * total
		klog.Infof("[%s] resources %v: total %v, used %v, thresholdRate %v, thresholdValue: %v", b.pluginName,
			resourceName, total, used, *thresholdRate, thresholdValue)

		exceededValue := thresholdValue - used
		if exceededValue < 0 {
			klog.Infof("[%s] resources %v exceeded: total %v, used %v, thresholdRate %v, thresholdValue: %v", b.pluginName,
				resourceName, total, used, *thresholdRate, thresholdValue)

			return &pluginapi.ThresholdMetResponse{
				ThresholdValue:     thresholdValue,
				ObservedValue:      used,
				ThresholdOperator:  pluginapi.ThresholdOperator_GREATER_THAN,
				MetType:            pluginapi.ThresholdMetType_HARD_MET,
				EvictionScope:      string(resourceName),
				GracePeriodSeconds: b.thresholdMetToleranceDurationGetter(),
			}, nil
		}
	}

	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (b *ResourcesEvictionPlugin) GetTopEvictionPods(_ context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		klog.Warningf("[%s] GetTopEvictionPods got empty active pods list", b.pluginName)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}

	activeFilteredPods := native.FilterPods(request.ActivePods, b.podFilter)

	sort.Slice(activeFilteredPods, func(i, j int) bool {
		valueI, valueJ := int64(0), int64(0)

		resourceI, resourceJ := native.SumUpPodRequestResources(activeFilteredPods[i]), native.SumUpPodRequestResources(activeFilteredPods[j])
		if quantity, ok := resourceI[v1.ResourceName(request.EvictionScope)]; ok {
			valueI = (&quantity).Value()
		}
		if quantity, ok := resourceJ[v1.ResourceName(request.EvictionScope)]; ok {
			valueJ = (&quantity).Value()
		}
		return valueI > valueJ
	})

	retLen := general.MinUInt64(request.TopN, uint64(len(activeFilteredPods)))

	var deletionOptions *pluginapi.DeletionOptions
	if gracePeriod := b.deletionGracePeriodGetter(); gracePeriod > 0 {
		deletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return &pluginapi.GetTopEvictionPodsResponse{
		TargetPods:      activeFilteredPods[:retLen],
		DeletionOptions: deletionOptions,
	}, nil
}

func (b *ResourcesEvictionPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}
