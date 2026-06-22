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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/evictionmanager/plugin"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	ReclaimedGPUResourcesEvictionPluginName = "reclaimed-gpu-resource-pressure-eviction-plugin"
)

const (
	thresholdMetToleranceDurationForGPU = 15
)

type ReclaimedGPUResourcesPlugin struct {
	*process.StopControl
	*ZoneResourcesPlugin
}

// NewReclaimedGPUResourcesEvictionPlugin constructs a GPU topology-aware eviction plugin.
// It wires threshold/deletion/tolerance getters from dynamic configuration and
// reuses the generic ZoneResourcesPlugin with zoneType=GPU to preserve behavior.
func NewReclaimedGPUResourcesEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration,
) plugin.EvictionPlugin {
	reclaimedThresholdGetter := func(resourceName v1.ResourceName) *float64 {
		if threshold, ok := conf.GetDynamicConfiguration().EvictionThreshold[resourceName]; !ok {
			return nil
		} else {
			return &threshold
		}
	}

	reclaimedSoftThresholdGetter := func(resourceName v1.ResourceName) *float64 {
		return nil
	}

	deletionGracePeriodGetter := func() int64 {
		return conf.GetDynamicConfiguration().ReclaimedResourcesEvictionConfiguration.DeletionGracePeriod
	}

	thresholdMetToleranceDurationGetter := func() int64 {
		return int64(thresholdMetToleranceDurationForGPU)
	}

	p := NewZoneResourcesPlugin(
		ReclaimedGPUResourcesEvictionPluginName,
		v1alpha1.TopologyTypeGPU,
		metaServer,
		emitter,
		nil,
		reclaimedThresholdGetter,
		reclaimedSoftThresholdGetter,
		nil,
		deletionGracePeriodGetter,
		thresholdMetToleranceDurationGetter,
		conf.SkipZeroQuantityResourceNames,
		conf.CheckReclaimedQoSForPod,
	)

	return &ReclaimedGPUResourcesPlugin{
		StopControl:         process.NewStopControl(time.Time{}),
		ZoneResourcesPlugin: p,
	}
}
