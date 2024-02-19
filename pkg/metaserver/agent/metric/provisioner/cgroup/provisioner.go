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

package cgroup

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// NewCGroupMetricsProvisioner returns the default implementation of CGroup.
func NewCGroupMetricsProvisioner(baseConf *global.BaseConfiguration,
	emitter metrics.MetricEmitter, _ pod.PodFetcher, metricStore *utilmetric.MetricStore) types.MetricsProvisioner {
	return &CGroupMetricsProvisioner{
		metricStore: metricStore,
		emitter:     emitter,
		baseConf:    baseConf,
	}
}

type CGroupMetricsProvisioner struct {
	baseConf    *global.BaseConfiguration
	metricStore *utilmetric.MetricStore
	emitter     metrics.MetricEmitter
}

func (m *CGroupMetricsProvisioner) Run(ctx context.Context) {
	m.sample(ctx)
}

func (m *CGroupMetricsProvisioner) sample(ctx context.Context) {
	// todo, we should add sample logic here by cgroupManager

	// todo, we should also make sure that the self-collected metrics
	//  should be checked firstly (whether malachite missed)
}
