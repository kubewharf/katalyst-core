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
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	evictionScopeSplitter = "|"
)

// ZoneAllocation is the resource status of a pod, where the key is zoneID
type ZoneAllocation map[string]v1.ResourceList

type PodZoneRequestResourcesGetter func(pod *v1.Pod, zoneID string, podZoneAllocations map[string]ZoneAllocation) v1.ResourceList

type ZoneResourcesPlugin struct {
	// emitter is used to emit metrics.
	emitter metrics.MetricEmitter
	// metaServer is used to get pod and cnr info.
	metaServer *metaserver.MetaServer

	podZoneRequestResourcesGetter PodZoneRequestResourcesGetter
	// thresholdGetter is used to get the threshold of resources.
	thresholdGetter                     ThresholdGetter
	deletionGracePeriodGetter           GracePeriodGetter
	thresholdMetToleranceDurationGetter GracePeriodGetter

	zoneType   v1alpha1.TopologyType
	pluginName string

	skipZeroQuantityResourceNames sets.String
	podFilter                     func(pod *v1.Pod) (bool, error)
}

func NewZoneResourcesPlugin(
	pluginName string,
	zoneType v1alpha1.TopologyType,
	metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter,
	podZoneRequestResourcesGetter PodZoneRequestResourcesGetter,
	thresholdGetter ThresholdGetter,
	deletionGracePeriodGetter GracePeriodGetter,
	thresholdMetToleranceDurationGetter GracePeriodGetter,
	skipZeroQuantityResourceNames sets.String,
	podFilter func(pod *v1.Pod) (bool, error),
) *ZoneResourcesPlugin {
	if podZoneRequestResourcesGetter == nil {
		podZoneRequestResourcesGetter = GenericPodZoneRequestResourcesGetter
	}

	return &ZoneResourcesPlugin{
		emitter:                             emitter,
		metaServer:                          metaServer,
		podZoneRequestResourcesGetter:       podZoneRequestResourcesGetter,
		thresholdGetter:                     thresholdGetter,
		deletionGracePeriodGetter:           deletionGracePeriodGetter,
		thresholdMetToleranceDurationGetter: thresholdMetToleranceDurationGetter,
		zoneType:                            zoneType,
		pluginName:                          pluginName,
		skipZeroQuantityResourceNames:       skipZeroQuantityResourceNames,
		podFilter:                           podFilter,
	}
}

func (p *ZoneResourcesPlugin) Name() string {
	if p == nil {
		return ""
	}

	return p.pluginName
}

func (p *ZoneResourcesPlugin) GetEvictPods(_ context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetEvictPods got nil request")
	}

	return &pluginapi.GetEvictPodsResponse{}, nil
}

func (p *ZoneResourcesPlugin) Start() {
	general.RegisterHeartbeatCheck(p.pluginName, defaultHealthCheckTimeout, general.HealthzCheckStateNotReady, defaultHealthCheckTimeout)
	return
}

func (p *ZoneResourcesPlugin) ThresholdMet(ctx context.Context, _ *pluginapi.GetThresholdMetRequest) (*pluginapi.ThresholdMetResponse, error) {
	activePods, err := p.metaServer.GetPodList(ctx, native.PodIsActive)
	if err != nil {
		errMsg := fmt.Sprintf("failed to list pods from metaServer: %v", err)
		klog.Errorf("[%s] %s", p.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	filteredPods := native.FilterPods(activePods, p.podFilter)
	klog.Infof("[%s] total %d filtered pods out-of %d running pods", p.pluginName, len(filteredPods), len(activePods))

	_ = p.emitter.StoreInt64(MetricsNamePodCount, int64(len(filteredPods)), metrics.MetricTypeNameRaw)
	if len(filteredPods) == 0 {
		return &pluginapi.ThresholdMetResponse{
			MetType: pluginapi.ThresholdMetType_NOT_MET,
		}, nil
	}

	cnr, err := p.metaServer.GetCNR(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get cnr from metaServer: %v", err)
	}

	allocatable, err := p.getZoneAllocatable(cnr)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get resources: %v", err)
		klog.Errorf("[%s] %s", p.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	zoneAllocations, err := p.getZoneAllocation(cnr)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get resources: %v", err)
		klog.Errorf("[%s] %s", p.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	emitZoneResourceMetrics(MetricsNamePodResource, allocatable, map[string]string{
		"type": "allocatable",
	}, p.emitter, p.zoneType)

	for zoneID, resources := range allocatable {
		tags := []metrics.MetricTag{
			{Key: "pluginName", Val: p.pluginName},
			{Key: "zoneID", Val: zoneID},
		}
		_ = p.emitter.StoreInt64(MetricsNameGetResourceEmpty, 1, metrics.MetricTypeNameCount, tags...)
		if len(resources) == 0 {
			return &pluginapi.ThresholdMetResponse{
				MetType: pluginapi.ThresholdMetType_NOT_MET,
			}, nil
		}
	}

	// use requests (rather than limits) as used resource
	usedZoneResources := make(map[string]v1.ResourceList)
	for zoneID := range allocatable {
		for _, pod := range filteredPods {
			resources := p.podZoneRequestResourcesGetter(pod, zoneID, zoneAllocations)
			usedResources := native.AddResources(usedZoneResources[zoneID], resources)
			usedZoneResources[zoneID] = usedResources

			native.EmitResourceMetrics(MetricsNamePodResource, resources, map[string]string{
				"pluginName": p.pluginName,
				"namespace":  pod.Namespace,
				"name":       pod.Name,
				"type":       "pod",
			}, p.emitter)
		}
	}

	klog.Infof("[%s] resources: allocatable %+v usedResources %+v", p.pluginName, allocatable, usedZoneResources)

	emitZoneResourceMetrics(MetricsNamePodResource, usedZoneResources, map[string]string{
		"pluginName": p.pluginName,
		"type":       "used",
	}, p.emitter, p.zoneType)

	for zoneID, usedResources := range usedZoneResources {
		for resourceName, usedQuantity := range usedResources {
			totalQuantity, ok := allocatable[zoneID][resourceName]
			if !ok {
				klog.Warningf("[%s] used resource: %s doesn't exist in allocatable", p.pluginName, resourceName)
				continue
			}

			total := float64((&totalQuantity).Value())

			if total <= 0 && p.skipZeroQuantityResourceNames.Has(string(resourceName)) {
				klog.Warningf("[%s] skip resource: %s with total: %s", p.pluginName, resourceName, totalQuantity.String())
				continue
			}

			used := float64((&usedQuantity).Value())
			// get resource threshold (i.e. tolerance) for each resource
			// if nil, eviction will not be triggered.
			thresholdRate := p.thresholdGetter(resourceName)
			if thresholdRate == nil {
				klog.Warningf("[%s] skip %s resource eviction because its resource threshold is empty", p.pluginName, resourceName)
				continue
			}

			thresholdValue := *thresholdRate * total
			klog.Infof("[%s] numa %s resources %v: total %v, used %v, thresholdRate %v, thresholdValue: %v", p.pluginName, zoneID,
				resourceName, total, used, *thresholdRate, thresholdValue)

			exceededValue := thresholdValue - used
			if exceededValue < 0 {
				klog.Infof("[%s] numa %s resources %v exceeded: total %v, used %v, thresholdRate %v, thresholdValue: %v", p.pluginName, zoneID,
					resourceName, total, used, *thresholdRate, thresholdValue)

				return &pluginapi.ThresholdMetResponse{
					ThresholdValue:     thresholdValue,
					ObservedValue:      used,
					ThresholdOperator:  pluginapi.ThresholdOperator_GREATER_THAN,
					MetType:            pluginapi.ThresholdMetType_HARD_MET,
					EvictionScope:      fmt.Sprintf("zone%s%s%s", zoneID, evictionScopeSplitter, string(resourceName)),
					GracePeriodSeconds: p.thresholdMetToleranceDurationGetter(),
				}, nil
			}
		}
	}

	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (p *ZoneResourcesPlugin) GetTopEvictionPods(ctx context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		klog.Warningf("[%s] GetTopEvictionPods got empty active pods list", p.pluginName)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}
	activeFilteredPods := native.FilterPods(request.ActivePods, p.podFilter)

	cnr, err := p.metaServer.GetCNR(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get cnr from metaServer: %v", err)
	}

	zoneAllocations, err := p.getZoneAllocation(cnr)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get resources: %v", err)
		klog.Errorf("[%s] %s", p.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	parseZoneScope := func(evictionScope string) (string, string, error) {
		fields := strings.Split(evictionScope, evictionScopeSplitter)
		if len(fields) < 1 {
			return "", "", fmt.Errorf("invalid eviction scope: %s", evictionScope)
		}

		zoneID := strings.TrimPrefix(fields[0], "zone")
		return zoneID, fields[1], nil
	}

	evictionZoneID, resourceName, err := parseZoneScope(request.EvictionScope)
	klog.Infof("[%s]GetTopEvictionPods parse evictionZoneID: %+v", p.pluginName, evictionZoneID)
	if err != nil {
		klog.Errorf("[%s] failed to parse eviction scope: %v", p.pluginName, err)
		return nil, err
	}

	candidateEvictionPods := make([]*v1.Pod, 0)
	for _, pod := range activeFilteredPods {
		resources := p.podZoneRequestResourcesGetter(pod, evictionZoneID, zoneAllocations)
		if _, ok := resources[v1.ResourceName(resourceName)]; ok {
			candidateEvictionPods = append(candidateEvictionPods, pod)
		}
	}

	sort.Slice(candidateEvictionPods, func(i, j int) bool {
		valueI, valueJ := int64(0), int64(0)

		resourceI, resourceJ := p.podZoneRequestResourcesGetter(candidateEvictionPods[i], evictionZoneID, zoneAllocations), p.podZoneRequestResourcesGetter(candidateEvictionPods[j], evictionZoneID, zoneAllocations)
		if quantity, ok := resourceI[v1.ResourceName(resourceName)]; ok {
			valueI = (&quantity).Value()
		}
		if quantity, ok := resourceJ[v1.ResourceName(resourceName)]; ok {
			valueJ = (&quantity).Value()
		}
		return valueI > valueJ
	})

	retLen := general.MinUInt64(request.TopN, uint64(len(candidateEvictionPods)))

	var deletionOptions *pluginapi.DeletionOptions
	if gracePeriod := p.deletionGracePeriodGetter(); gracePeriod >= 0 {
		deletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return &pluginapi.GetTopEvictionPodsResponse{
		TargetPods:      candidateEvictionPods[:retLen],
		DeletionOptions: deletionOptions,
	}, nil
}

func (p *ZoneResourcesPlugin) getZoneAllocatable(
	cnr *v1alpha1.CustomNodeResource,
) (map[string]v1.ResourceList, error) {
	if cnr == nil {
		return nil, fmt.Errorf("cnr is nil")
	}

	allocatable := make(map[string]v1.ResourceList)
	for _, zone := range cnr.Status.TopologyZone {
		p.getZoneResourceAllocatableFromTopologyZone(zone, allocatable)
	}

	return allocatable, nil
}

func (p *ZoneResourcesPlugin) getZoneResourceAllocatableFromTopologyZone(
	zone *v1alpha1.TopologyZone,
	allocatable map[string]v1.ResourceList,
) {
	if zone == nil {
		return
	}

	if zone.Type == p.zoneType {
		allocatable[zone.Name] = *zone.Resources.Allocatable
		return
	}

	for _, children := range zone.Children {
		p.getZoneResourceAllocatableFromTopologyZone(children, allocatable)
	}
}

func (p *ZoneResourcesPlugin) getZoneAllocation(
	cnr *v1alpha1.CustomNodeResource,
) (map[string]ZoneAllocation, error) {
	if cnr == nil {
		return nil, fmt.Errorf("cnr is nil")
	}

	zoneAllocations := make(map[string]ZoneAllocation)
	for _, zone := range cnr.Status.TopologyZone {
		p.getZoneAllocationFromTopologyZone(zone, zoneAllocations)
	}

	return zoneAllocations, nil
}

func (p *ZoneResourcesPlugin) getZoneAllocationFromTopologyZone(
	zone *v1alpha1.TopologyZone,
	zoneAllocations map[string]ZoneAllocation,
) {
	if zone == nil {
		return
	}

	if zone.Type == p.zoneType {
		for _, allocation := range zone.Allocations {
			if allocation == nil || allocation.Requests == nil {
				continue
			}

			_, _, uid, err := native.ParseNamespaceNameUIDKey(allocation.Consumer)
			if err != nil {
				klog.Errorf("unexpected CNR zone consumer: %v", err)
				continue
			}

			if zoneAllocations[uid] == nil {
				zoneAllocations[uid] = make(map[string]v1.ResourceList)
			}

			zoneAllocations[uid][zone.Name] = *allocation.Requests
		}
		return
	}

	for _, children := range zone.Children {
		p.getZoneAllocationFromTopologyZone(children, zoneAllocations)
	}
}

func GenericPodZoneRequestResourcesGetter(
	pod *v1.Pod, zoneID string, podZoneAllocations map[string]ZoneAllocation,
) v1.ResourceList {
	if pod == nil {
		return nil
	}
	if zoneAllocations, ok := podZoneAllocations[string(pod.UID)]; ok {
		return zoneAllocations[zoneID]
	}
	return nil
}

func emitZoneResourceMetrics(name string, zoneResourceList map[string]v1.ResourceList,
	tags map[string]string, emitter metrics.MetricEmitter, zoneType v1alpha1.TopologyType,
) {
	for zoneID, resourceList := range zoneResourceList {
		tags["zone"] = zoneID
		tags["zoneType"] = string(zoneType)
		native.EmitResourceMetrics(name, resourceList, tags, emitter)
	}
}
