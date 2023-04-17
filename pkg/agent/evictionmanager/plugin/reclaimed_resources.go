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

package plugin

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/events"

	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const ReclaimedResourcesEvictionPluginName = "reclaimed-resource-pressure-eviction-plugin"

type reclaimedResourcesPlugin struct {
	*process.StopControl
	*ResourcesEvictionPlugin
}

func NewReclaimedResourcesEvictionPlugin(_ *client.GenericClientSet, _ events.EventRecorder,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) EvictionPlugin {
	reclaimedResourcesGetter := func(ctx context.Context) (v1.ResourceList, error) {
		cnr, err := metaServer.GetCNR(ctx)
		if err != nil {
			return nil, err
		}

		allocatable := make(v1.ResourceList)
		if cnr != nil && cnr.Status.ResourceAllocatable != nil {
			allocatable = *cnr.Status.ResourceAllocatable
		}
		return allocatable, nil
	}

	reclaimedThresholdGetter := func(resourceName v1.ResourceName) float64 {
		if threshold, ok := conf.ReclaimedResourcesEvictionPluginConfiguration.DynamicConf.
			EvictionThreshold()[resourceName]; !ok {
			return 1.
		} else {
			return threshold
		}
	}

	p := NewResourcesEvictionPlugin(
		ReclaimedResourcesEvictionPluginName,
		metaServer,
		emitter,
		reclaimedResourcesGetter,
		reclaimedThresholdGetter,
		conf.SkipZeroQuantityResourceNames,
		conf.CheckReclaimedQoSForPod,
		conf.EvictionReclaimedPodGracefulPeriod,
	)

	return &reclaimedResourcesPlugin{
		StopControl:             process.NewStopControl(time.Time{}),
		ResourcesEvictionPlugin: p,
	}
}
