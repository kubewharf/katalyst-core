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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const ReclaimedNumaResourcesEvictionPluginName = "reclaimed-numa-resource-pressure-eviction-plugin"

// NumaResourcesGetter return the resource status of numa granularity, where the key is numaID.
type NumaResourcesGetter func(ctx context.Context) (map[string]v1.ResourceList, error)

type ReclaimedNumaResourcesPlugin struct {
	*process.StopControl
	*ResourcesEvictionPlugin

	NumaResourcesGetter NumaResourcesGetter
}

func NewReclaimedNumaResourcesEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	reclaimedResourcesGetter := func(ctx context.Context) (v1.ResourceList, error) {
		return nil, nil
	}
	numaResourcesGetter := func(ctx context.Context) (map[string]v1.ResourceList, error) {
		return GetNumaResourceAllocatable(ctx, metaServer)
	}

	podRequestResourcesGetter := native.SumUpPodRequestResources
	reclaimedThresholdGetter := func(resourceName v1.ResourceName) *float64 {
		if threshold, ok := conf.GetDynamicConfiguration().EvictionThreshold[resourceName]; !ok {
			return nil
		} else {
			return &threshold
		}
	}

	deletionGracePeriodGetter := func() int64 {
		return conf.GetDynamicConfiguration().ReclaimedResourcesEvictionConfiguration.DeletionGracePeriod
	}
	thresholdMetToleranceDurationGetter := func() int64 {
		return conf.GetDynamicConfiguration().ThresholdMetToleranceDuration
	}

	p := NewResourcesEvictionPlugin(
		ReclaimedNumaResourcesEvictionPluginName,
		metaServer,
		emitter,
		reclaimedResourcesGetter,
		podRequestResourcesGetter,
		reclaimedThresholdGetter,
		deletionGracePeriodGetter,
		thresholdMetToleranceDurationGetter,
		conf.SkipZeroQuantityResourceNames,
		conf.CheckReclaimedQoSForPod,
	)

	return &ReclaimedNumaResourcesPlugin{
		StopControl:             process.NewStopControl(time.Time{}),
		ResourcesEvictionPlugin: p,
		NumaResourcesGetter:     numaResourcesGetter,
	}
}

func (p *ReclaimedNumaResourcesPlugin) Start() {
	general.RegisterHeartbeatCheck(ReclaimedNumaResourcesEvictionPluginName, defaultHealthCheckTimeout, general.HealthzCheckStateNotReady, defaultHealthCheckTimeout)
	return
}

func (p *ReclaimedNumaResourcesPlugin) ThresholdMet(ctx context.Context) (*pluginapi.ThresholdMetResponse, error) {
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

	allocatable, err := p.NumaResourcesGetter(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get resources: %v", err)
		klog.Errorf("[%s] %s", p.pluginName, errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	emitNumaResourceMetrics(MetricsNamePodResource, allocatable, map[string]string{
		"type": "allocatable",
	}, p.emitter)

	for numaID, resources := range allocatable {
		tags := []metrics.MetricTag{
			{Key: "pluginName", Val: p.pluginName},
			{Key: "numaID", Val: numaID},
		}
		_ = p.emitter.StoreInt64(MetricsNameGetResourceEmpty, 1, metrics.MetricTypeNameCount, tags...)
		if len(resources) == 0 {
			return &pluginapi.ThresholdMetResponse{
				MetType: pluginapi.ThresholdMetType_NOT_MET,
			}, nil
		}
	}

	// use requests (rather than limits) as used resource
	usedNumaResources := make(map[string]v1.ResourceList)
	for _, pod := range filteredPods {
		numaID, err := parseNumaIDFormPod(pod)
		klog.Infof("[%s]ThresholdMet parse pod %v numaID %s", p.pluginName, pod.Name, numaID)
		if err != nil || numaID == "" {
			errMsg := fmt.Sprintf("failed to parse pod numaID: %v", err)
			klog.Errorf("[%s] %s", p.pluginName, errMsg)
			return nil, fmt.Errorf(errMsg)
		}
		if _, ok := allocatable[numaID]; !ok {
			klog.Warningf("[%s]the parsed numa id %s is invalid", p.pluginName, numaID)
			continue
		}

		resources := p.podRequestResourcesGetter(pod)
		usedResources := native.AddResources(usedNumaResources[numaID], resources)
		usedNumaResources[numaID] = usedResources

		native.EmitResourceMetrics(MetricsNamePodResource, resources, map[string]string{
			"pluginName": p.pluginName,
			"namespace":  pod.Namespace,
			"name":       pod.Name,
			"type":       "pod",
		}, p.emitter)
	}
	klog.Infof("[%s] resources: allocatable %+v usedResources %+v", p.pluginName, allocatable, usedNumaResources)

	emitNumaResourceMetrics(MetricsNamePodResource, usedNumaResources, map[string]string{
		"pluginName": p.pluginName,
		"type":       "used",
	}, p.emitter)

	for numaID, usedResources := range usedNumaResources {
		for resourceName, usedQuantity := range usedResources {
			totalQuantity, ok := allocatable[numaID][resourceName]
			if !ok {
				klog.Warningf("[%s] used resource: %s doesn't exist in allocatable", p.pluginName, resourceName)
				continue
			}

			total := float64((&totalQuantity).Value())

			if total <= 0 && p.skipZeroQuantityResourceNames.Has(string(resourceName)) {
				klog.Warningf("[%s] skip resource: %s with total: %.2f", p.pluginName, total)
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
			klog.Infof("[%s] numa %s resources %v: total %v, used %v, thresholdRate %v, thresholdValue: %v", p.pluginName, numaID,
				resourceName, total, used, *thresholdRate, thresholdValue)

			exceededValue := thresholdValue - used
			if exceededValue < 0 {
				klog.Infof("[%s] numa %s resources %v exceeded: total %v, used %v, thresholdRate %v, thresholdValue: %v", p.pluginName, numaID,
					resourceName, total, used, *thresholdRate, thresholdValue)

				return &pluginapi.ThresholdMetResponse{
					ThresholdValue:     thresholdValue,
					ObservedValue:      used,
					ThresholdOperator:  pluginapi.ThresholdOperator_GREATER_THAN,
					MetType:            pluginapi.ThresholdMetType_HARD_MET,
					EvictionScope:      fmt.Sprintf("numa%s_%s", numaID, string(resourceName)),
					GracePeriodSeconds: p.thresholdMetToleranceDurationGetter(),
				}, nil
			}
		}
	}

	return &pluginapi.ThresholdMetResponse{
		MetType: pluginapi.ThresholdMetType_NOT_MET,
	}, nil
}

func (p *ReclaimedNumaResourcesPlugin) GetTopEvictionPods(ctx context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if request == nil {
		return nil, fmt.Errorf("GetTopEvictionPods got nil request")
	}

	if len(request.ActivePods) == 0 {
		klog.Warningf("[%s] GetTopEvictionPods got empty active pods list", p.pluginName)
		return &pluginapi.GetTopEvictionPodsResponse{}, nil
	}
	activeFilteredPods := native.FilterPods(request.ActivePods, p.podFilter)

	numaPodsMap := make(map[string][]*v1.Pod)
	for _, pod := range activeFilteredPods {
		numaID, err := parseNumaIDFormPod(pod)
		if err != nil {
			klog.Warningf("[%s] failed to parse pod numaID: %v", p.pluginName, err)
			continue
		}
		numaPodsMap[numaID] = append(numaPodsMap[numaID], pod)
	}

	parseNumaScope := func(evictionScope string) (string, error) {
		fields := strings.Split(evictionScope, "_")
		if len(fields) < 1 {
			return "", fmt.Errorf("invalid eviction scope: %s", evictionScope)
		}

		numa := strings.TrimPrefix(fields[0], "numa")
		return numa, nil
	}

	evictionNumaID, err := parseNumaScope(request.EvictionScope)
	klog.Infof("[%s]GetTopEvictionPods parse evictionNumaID: %+v", p.pluginName, evictionNumaID)
	if err != nil {
		klog.Errorf("[%s] failed to parse eviction scope: %v", p.pluginName, err)
		return nil, err
	}

	candidateEvictionPods := numaPodsMap[evictionNumaID]
	sort.Slice(candidateEvictionPods, func(i, j int) bool {
		valueI, valueJ := int64(0), int64(0)

		resourceI, resourceJ := p.podRequestResourcesGetter(candidateEvictionPods[i]), p.podRequestResourcesGetter(candidateEvictionPods[j])
		if quantity, ok := resourceI[v1.ResourceName(request.EvictionScope)]; ok {
			valueI = (&quantity).Value()
		}
		if quantity, ok := resourceJ[v1.ResourceName(request.EvictionScope)]; ok {
			valueJ = (&quantity).Value()
		}
		return valueI > valueJ
	})

	retLen := general.MinUInt64(request.TopN, uint64(len(candidateEvictionPods)))

	var deletionOptions *pluginapi.DeletionOptions
	if gracePeriod := p.deletionGracePeriodGetter(); gracePeriod > 0 {
		deletionOptions = &pluginapi.DeletionOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}

	return &pluginapi.GetTopEvictionPodsResponse{
		TargetPods:      candidateEvictionPods[:retLen],
		DeletionOptions: deletionOptions,
	}, nil
}

func GetNumaResourceAllocatable(ctx context.Context, metaServer *metaserver.MetaServer) (map[string]v1.ResourceList, error) {
	cnr, err := metaServer.GetCNR(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get cnr from metaServer: %v", err)
	}

	allocatable := make(map[string]v1.ResourceList)

	for _, zone := range cnr.Status.TopologyZone {
		getNumaResourceAllocatableFromTopologyZone(zone, allocatable)
	}

	return allocatable, nil
}

func getNumaResourceAllocatableFromTopologyZone(zone *v1alpha1.TopologyZone, allocatable map[string]v1.ResourceList) {
	if zone == nil {
		return
	}

	if zone.Type == v1alpha1.TopologyTypeNuma {
		allocatable[zone.Name] = *zone.Resources.Allocatable
		return
	}

	for _, children := range zone.Children {
		getNumaResourceAllocatableFromTopologyZone(children, allocatable)
	}
}

func parseNumaIDFormPod(pod *v1.Pod) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("pod is nil")
	}

	if pod.Annotations == nil {
		return "", fmt.Errorf("pod anno is nil ")
	}

	numaID, ok := pod.Annotations[consts.PodAnnotationNUMABindResultKey]
	if !ok {
		return "", fmt.Errorf("pod numa binding annotation does not exist")
	}

	return numaID, nil
}

func emitNumaResourceMetrics(name string, numaResourceList map[string]v1.ResourceList,
	tags map[string]string, emitter metrics.MetricEmitter,
) {
	for numaID, resourceList := range numaResourceList {
		tags["numa"] = numaID
		native.EmitResourceMetrics(name, resourceList, tags, emitter)
	}
}
