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

package strategy

import (
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

// SubEntries is keyed by container name or empty string (for pool)
type SubEntries map[string]*metric.MetricRing

func (se SubEntries) IsPoolEntry() bool {
	return len(se) == 1 && se[advisorapi.FakedContainerName] != nil
}

// Entries are keyed by pod UID or pool name
type Entries map[string]SubEntries

type PoolMetricCollectHandler func(dynamicConfig *dynamic.Configuration, poolsUnderPressure bool,
	metricName string, metricValue float64, poolName string, poolSize int, collectTime int64)
