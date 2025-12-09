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
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const ReclaimedNumaResourcesEvictionPluginName = "reclaimed-numa-resource-pressure-eviction-plugin"

type ReclaimedNumaResourcesPlugin struct {
	*process.StopControl
	*ZoneResourcesPlugin
}

func NewReclaimedNumaResourcesEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
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

	p := NewZoneResourcesPlugin(
		ReclaimedNumaResourcesEvictionPluginName,
		v1alpha1.TopologyTypeNuma,
		metaServer,
		emitter,
		PodNUMARequestResourcesGetter,
		reclaimedThresholdGetter,
		deletionGracePeriodGetter,
		thresholdMetToleranceDurationGetter,
		conf.SkipZeroQuantityResourceNames,
		conf.CheckReclaimedQoSForPod,
	)

	return &ReclaimedNumaResourcesPlugin{
		StopControl:         process.NewStopControl(time.Time{}),
		ZoneResourcesPlugin: p,
	}
}

func PodNUMARequestResourcesGetter(pod *v1.Pod, zoneID string, podZoneAllocations map[string]ZoneAllocation) v1.ResourceList {
	result := GenericPodZoneRequestResourcesGetter(pod, zoneID, podZoneAllocations)
	if len(result) != 0 {
		return result
	}

	if pod == nil {
		return nil
	}

	numaID, err := parseNumaIDFormPod(pod)
	if err != nil {
		return nil
	}

	if zoneID != numaID {
		return nil
	}

	return native.SumUpPodRequestResources(pod)
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
