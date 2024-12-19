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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type memoryHeadroomManagerImpl struct {
	*GenericHeadroomManager
}

func NewMemoryHeadroomManager(emitter metrics.MetricEmitter, meteServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor,
) (manager.HeadroomManager, error) {
	gm := NewGenericHeadroomManager(
		v1.ResourceMemory,
		false,
		false,
		conf.HeadroomReporterSyncPeriod,
		headroomAdvisor,
		emitter,
		generateMemoryWindowOptions(conf.HeadroomReporterConfiguration),
		generateReclaimedMemoryOptionsFunc(conf.DynamicAgentConfiguration),
		meteServer,
	)

	cm := &memoryHeadroomManagerImpl{
		GenericHeadroomManager: gm,
	}

	return cm, nil
}

func generateMemoryWindowOptions(conf *reporter.HeadroomReporterConfiguration) GenericSlidingWindowOptions {
	return GenericSlidingWindowOptions{
		SlidingWindowTime: conf.HeadroomReporterSlidingWindowTime,
		MinStep:           conf.HeadroomReporterSlidingWindowMinStep[v1.ResourceMemory],
		MaxStep:           conf.HeadroomReporterSlidingWindowMaxStep[v1.ResourceMemory],
		AggregateFunc:     conf.HeadroomReporterSlidingWindowAggregateFunction,
		AggregateArgs:     conf.HeadroomReporterSlidingWindowAggregateArguments,
	}
}

func generateReclaimedMemoryOptionsFunc(conf *dynamic.DynamicAgentConfiguration) GetGenericReclaimOptionsFunc {
	return func() GenericReclaimOptions {
		return GenericReclaimOptions{
			EnableReclaim:                 conf.GetDynamicConfiguration().EnableReclaim,
			ReservedResourceForReport:     conf.GetDynamicConfiguration().ReservedResourceForReport[v1.ResourceMemory],
			MinReclaimedResourceForReport: conf.GetDynamicConfiguration().MinReclaimedResourceForReport[v1.ResourceMemory],
		}
	}
}
