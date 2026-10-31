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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cpuHeadroomManagerImpl struct {
	*GenericHeadroomManager
}

func NewCPUHeadroomManager(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer, metaCache metacache.MetaCache,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor,
) (manager.HeadroomManager, error) {
	gm := NewGenericHeadroomManager(
		v1.ResourceCPU,
		true,
		true,
		conf.HeadroomReporterSyncPeriod,
		headroomAdvisor,
		emitter,
		generateCPUWindowOptions(conf.HeadroomReporterConfiguration),
		generateReclaimCPUOptionsFunc(conf.DynamicAgentConfiguration),
		metaServer,
		metaCache,
		WithNUMAResultApportioner(newCPUNUMAResultApportioner(metaServer)),
	)

	cm := &cpuHeadroomManagerImpl{
		GenericHeadroomManager: gm,
	}

	return cm, nil
}

func newCPUNUMAResultApportioner(metaServer *metaserver.MetaServer) NUMAResultApportioner {
	return func(target resource.Quantity, current map[int]resource.Quantity) (
		resource.Quantity, map[int]resource.Quantity, error,
	) {
		weights := make(map[int]int64, len(current))
		limits := make(map[int]int64, len(current))
		for numaID, quantity := range current {
			value := quantity.MilliValue() / 1000
			weights[numaID] = value
			limits[numaID] = value
		}

		// distribute per single logical CPU (quantum=1): reclaim headroom is a
		// capacity hint and must not be rounded down to a whole-core multiple,
		// otherwise the sub-core remainder is silently lost on every report.
		allocations, effective, err := machine.ApportionNUMACPU(
			target.MilliValue()/1000,
			weights,
			limits,
			1,
		)
		if err != nil {
			return resource.Quantity{}, nil, err
		}
		klog.V(4).Infof("apportion cpu numa result: requested=%d, weights=%v, limits=%v, allocations=%v, effective=%d",
			target.MilliValue()/1000, weights, limits, allocations, effective)

		result := make(map[int]resource.Quantity, len(allocations))
		for numaID, allocation := range allocations {
			result[numaID] = *resource.NewQuantity(allocation, resource.DecimalSI)
		}
		return *resource.NewQuantity(effective, resource.DecimalSI), result, nil
	}
}

func generateCPUWindowOptions(conf *reporter.HeadroomReporterConfiguration) GenericSlidingWindowOptions {
	return GenericSlidingWindowOptions{
		SlidingWindowTime: conf.HeadroomReporterSlidingWindowTime,
		MinStep:           conf.HeadroomReporterSlidingWindowMinStep[v1.ResourceCPU],
		MaxStep:           conf.HeadroomReporterSlidingWindowMaxStep[v1.ResourceCPU],
		AggregateFunc:     conf.HeadroomReporterSlidingWindowAggregateFunction,
		AggregateArgs:     conf.HeadroomReporterSlidingWindowAggregateArguments,
	}
}

func generateReclaimCPUOptionsFunc(conf *dynamic.DynamicAgentConfiguration) GetGenericReclaimOptionsFunc {
	return func() GenericReclaimOptions {
		return GenericReclaimOptions{
			EnableReclaim:                 conf.GetDynamicConfiguration().EnableReclaim,
			ReservedResourceForReport:     conf.GetDynamicConfiguration().ReservedResourceForReport[v1.ResourceCPU],
			MinReclaimedResourceForReport: conf.GetDynamicConfiguration().MinReclaimedResourceForReport[v1.ResourceCPU],
		}
	}
}
