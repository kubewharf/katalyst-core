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

package bulkhead

import (
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTopologyConvergenceDeadline covers the observed cgroup v1
	// multi-round convergence window under high-churn E2E workloads.
	DefaultTopologyConvergenceDeadline = 10 * time.Second
	DefaultDeadlockProbeOperations     = 4096
	// The outer handler also runs cpuset_mems, workqueue, and system_service
	// after topology convergence, so it must not expire at the topology budget.
	minTopologyHandlerTimeoutMargin = 100 * time.Millisecond
	maxTopologyHandlerTimeoutMargin = 5 * time.Second
	maxTopologyHandlerTimeout       = time.Duration(1<<63 - 1)
)

type ConvergenceBudget struct {
	MaxRounds                  int
	MaxHierarchyIOOperations   int
	MaxSnapshotNodes           int
	MaxSnapshotDepth           int
	MaxDomains                 int
	MaxTransferEdges           int
	MaxPlanOperations          int
	MaxDeadlockProbeOperations int
	DeadlineDuration           time.Duration
}

type DrainSelectionPolicy struct {
	MaxCPUsDrainRatio         float64
	GroupByNUMA               bool
	RequirePairedSwapProgress bool
}

type BulkheadConfiguration struct {
	BulkheadPrimaryRelPath      string
	BulkheadReclaimRelPaths     []string
	BulkheadReclaimNumaPrefixes []string
	BulkheadPartitionRelPaths   []string
	// BulkheadReclaimSiblingRelPaths lists reclaim-domain sibling cpuset
	// cgroups explicitly materialized by cpuset_topology. These rel paths are
	// managed as reclaim siblings, not primary reclaim roots.
	BulkheadReclaimSiblingRelPaths []string
	EnableBulkheadReclaimSiblings  bool

	// EnableBulkheadCpusetTopologyOnCgroupV2 gates the cpuset_topology plugin on
	// cgroup v2 hosts. When false (default) the plugin is inert on cgroup v2:
	// Enable returns false and its adjustment-disabled and periodical handlers
	// become no-ops, so cgroup v2 hosts are never touched. cgroup v1 behavior is
	// unaffected. When true the plugin also runs on cgroup v2.
	EnableBulkheadCpusetTopologyOnCgroupV2    bool
	PreserveReclaimCPUSetWhenTopologyDisabled bool

	BulkheadWorkqueueSysfsDir string
	BulkheadWorkqueueNames    []string

	// system_service plugin
	BulkheadSystemRelPath           string
	BulkheadSystemServiceProcfsPath string
	// BulkheadSystemdCommBlacklist lists userspace comm values that MUST
	// stay in the cgroup ROOT (i.e. NOT be migrated to the system cgroup).
	// Anything not on the blacklist is a candidate. Latency-critical
	// daemons such as systemd, kubelet, containerd should be listed here.
	BulkheadSystemdCommBlacklist []string
	// BulkheadSystemKThreadCommSubstrs is the substring whitelist for
	// kernel threads: an eligible kthread must have a comm containing one
	// of these substrings. Default is a small set of high-load movable
	// kthreads (kswapd, kcompactd). Never populate this with per-CPU
	// kthreads (migration/N, ksoftirqd/N) — they cannot be moved.
	BulkheadSystemKThreadCommSubstrs []string

	TopologyConvergenceBudget  ConvergenceBudget
	TopologyDrainSelection     DrainSelectionPolicy
	EnableAdmissionLeafDefer   bool
	AdmissionMaxRequiredWrites int
	AdmissionSafeDuration      time.Duration
}

func NewBulkheadConfiguration() *BulkheadConfiguration {
	return &BulkheadConfiguration{
		TopologyConvergenceBudget:  DefaultConvergenceBudget(),
		TopologyDrainSelection:     DefaultDrainSelectionPolicy(),
		EnableAdmissionLeafDefer:   true,
		AdmissionMaxRequiredWrites: 0,
		AdmissionSafeDuration:      5 * time.Second,
	}
}

func DefaultConvergenceBudget() ConvergenceBudget {
	return ConvergenceBudget{
		MaxRounds:                  0,
		MaxHierarchyIOOperations:   0,
		MaxSnapshotNodes:           4096,
		MaxSnapshotDepth:           16,
		MaxDomains:                 256,
		MaxTransferEdges:           4096,
		MaxPlanOperations:          0,
		MaxDeadlockProbeOperations: 0,
		DeadlineDuration:           DefaultTopologyConvergenceDeadline,
	}
}

// TopologyHandlerTimeout derives the outer handler bound from the configured
// coordinator deadline so the inner convergence budget always expires first.
func TopologyHandlerTimeout(c *BulkheadConfiguration) time.Duration {
	deadline := DefaultTopologyConvergenceDeadline
	if c != nil && c.TopologyConvergenceBudget.DeadlineDuration > 0 {
		deadline = c.TopologyConvergenceBudget.DeadlineDuration
	}
	margin := deadline / 2
	if margin < minTopologyHandlerTimeoutMargin {
		margin = minTopologyHandlerTimeoutMargin
	}
	if margin > maxTopologyHandlerTimeoutMargin {
		margin = maxTopologyHandlerTimeoutMargin
	}
	// Saturate instead of wrapping when an extreme configured deadline leaves
	// no representable room for the outer-handler margin.
	if deadline > maxTopologyHandlerTimeout-margin {
		return maxTopologyHandlerTimeout
	}
	return deadline + margin
}

func DefaultDrainSelectionPolicy() DrainSelectionPolicy {
	return DrainSelectionPolicy{
		MaxCPUsDrainRatio:         0,
		GroupByNUMA:               false,
		RequirePairedSwapProgress: true,
	}
}

func FormatBulkheadNUMARel(prefix string, numaID int) string {
	if prefix == "" {
		return ""
	}
	return strings.Trim(prefix+strconv.Itoa(numaID), "/")
}

func (c BulkheadConfiguration) ReclaimPerNUMA(reclaimIdx, numaID int) string {
	if reclaimIdx < 0 || reclaimIdx >= len(c.BulkheadReclaimNumaPrefixes) {
		return ""
	}
	return FormatBulkheadNUMARel(c.BulkheadReclaimNumaPrefixes[reclaimIdx], numaID)
}
