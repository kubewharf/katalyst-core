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
	"fmt"

	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type ReclaimMetrics struct {
	// cpu usage of root cgroup for reclaim pods
	CgroupCPUUsage float64
	// reclaimedCoresSupply is the actual CPU resource can be supplied to reclaimed cores
	ReclaimedCoresSupply float64
}

// GetReclaimMetricsMulti aggregates reclaim CPU metrics across one or more
// sibling reclaim cgroup paths that share the same reclaim CPU pool (cpus).
//
// overlapReclaim describes how the reclaim cpuset relates to the share pool:
//   - true: reclaimed_cores overlaps (shares cores with) the share pool. The
//     supply is the pool's spare capacity (cpuset size minus the current
//     per-core utilization of the shared pool) plus what reclaim already uses,
//     because non-reclaim workloads compete for the same cores.
//   - false: the reclaim cpuset is exclusive to reclaimed_cores, so nothing
//     else competes for those cores and the supply equals the cpuset size
//     directly. Subtracting per-core utilization here would understate the
//     per-numa supply below the cpuset size.
//
// In both branches the supply is clamped to the aggregated CFS quota when every
// sibling path is quota-limited.
func GetReclaimMetricsMulti(cpus machine.CPUSet, siblingCgroupPaths []string, metricsFetcher types.MetricsFetcher, overlapReclaim bool) (*ReclaimMetrics, error) {
	if len(siblingCgroupPaths) == 0 {
		return nil, fmt.Errorf("no reclaim cgroup paths provided")
	}

	var totalCgroupCPUUsage, totalCfsQuota float64
	unlimited := false
	for _, cgroupPath := range siblingCgroupPaths {
		usage, err := metricsFetcher.GetCgroupMetric(cgroupPath, pkgconsts.MetricCPUUsageCgroup)
		if err != nil {
			return nil, err
		}
		quota, err := metricsFetcher.GetCgroupMetric(cgroupPath, pkgconsts.MetricCPUQuotaCgroup)
		if err != nil {
			return nil, err
		}
		period, err := metricsFetcher.GetCgroupMetric(cgroupPath, pkgconsts.MetricCPUPeriodCgroup)
		if err != nil {
			return nil, err
		}
		// convert the CFS quota/period pair into cores
		cfsQuota := quota.Value
		if cfsQuota > 0 && period.Value > 0 {
			cfsQuota = cfsQuota / period.Value
		}

		// siblings are disjoint subtrees, so their usage and quota sum. a single
		// uncapped subtree (quota <= 0) lets the whole pool burst freely.
		totalCgroupCPUUsage += usage.Value
		if cfsQuota > 0 {
			totalCfsQuota += cfsQuota
		} else {
			unlimited = true
		}
	}

	// the pool's spare (unused) capacity is a property of the shared pool, so it
	// is measured once rather than per sibling path.
	var reclaimedCoresSupply float64
	if overlapReclaim {
		poolCPUUsage := metricsFetcher.AggregateCoreMetric(cpus, pkgconsts.MetricCPUUsageRatio, metric.AggregatorSum).Value
		reclaimedCoresSupply = general.MaxFloat64(float64(cpus.Size())-poolCPUUsage, 0) + totalCgroupCPUUsage
	} else {
		reclaimedCoresSupply = float64(cpus.Size())
	}
	// only clamp to the quota when every path has a quota
	if !unlimited && totalCfsQuota > 0 {
		reclaimedCoresSupply = general.MinFloat64(reclaimedCoresSupply, totalCfsQuota)
	}

	return &ReclaimMetrics{
		CgroupCPUUsage:       totalCgroupCPUUsage,
		ReclaimedCoresSupply: reclaimedCoresSupply,
	}, nil
}
