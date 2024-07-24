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

package helper

import (
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type ReclaimMetrics struct {
	// total cpu usage of cpus in reclaim pool
	PoolCPUUsage float64
	// cpu usage of root cgroup for reclaim pods
	CgroupCPUUsage float64
	// reclaim pool size
	Size int
	// reclaimedCoresSupply is the actual CPU resource can be supplied to reclaimed cores
	ReclaimedCoresSupply float64
}

// GetReclaimMetrics returns the reclaim CPU metrics for the given cpus and cgroupPath
func GetReclaimMetrics(cpus machine.CPUSet, cgroupPath string, metricsFetcher types.MetricsFetcher) (*ReclaimMetrics, error) {
	data := metricsFetcher.AggregateCoreMetric(cpus, pkgconsts.MetricCPUUsageRatio, metric.AggregatorSum)
	poolCPUUsage := data.Value

	data, err := metricsFetcher.GetCgroupMetric(cgroupPath, pkgconsts.MetricCPUUsageCgroup)
	if err != nil {
		return nil, err
	}
	cgroupCPUUsage := data.Value

	// when shared_cores overlap reclaimed_cores, the actual CPU resource can be supplied to reclaimed cores is idle + reclaimed_cores cpu usage
	reclaimedCoresSupply := general.MaxFloat64(float64(cpus.Size())-poolCPUUsage, 0) + cgroupCPUUsage

	return &ReclaimMetrics{
		PoolCPUUsage:         poolCPUUsage,
		CgroupCPUUsage:       cgroupCPUUsage,
		Size:                 cpus.Size(),
		ReclaimedCoresSupply: reclaimedCoresSupply,
	}, nil
}
