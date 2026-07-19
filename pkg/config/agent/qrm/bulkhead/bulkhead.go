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
)

type BulkheadConfiguration struct {
	BulkheadPrimaryRelPath        string
	BulkheadReclaimRelPaths       []string
	BulkheadReclaimNumaPrefixes   []string
	BulkheadPartitionRelPaths     []string
	EnableBulkheadReclaimSiblings bool

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
}

func NewBulkheadConfiguration() *BulkheadConfiguration {
	return &BulkheadConfiguration{}
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
