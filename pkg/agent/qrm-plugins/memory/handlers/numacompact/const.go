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

package numacompact

const (
	// invalidUnusableIndex marks a compaction state whose post-compaction THP-order unusable index
	// could not be read. Valid unusable-index values are in the range [0, 100].
	invalidUnusableIndex float64 = -1
)

const (
	// metricNameMemoryCompact is emitted (with a numa_id tag) whenever the idle-NUMA path
	// actually compacts a NUMA node. It is dedicated to this plugin and intentionally distinct
	// from the fragmem plugin's metric so the two features can be observed separately.
	metricNameMemoryCompact = "numa_memory_compact"

	// metricNameMemoryCompactCost is emitted (with a numa_id tag) after a NUMA node is actually
	// compacted, reporting how long the compaction took in milliseconds.
	metricNameMemoryCompactCost = "numa_memory_compact_cost_ms"

	// metricNameNumaMemCompactTHPUnusableIndexDiff reports the pre-compaction minus
	// post-compaction THP-order unusable index.
	metricNameNumaMemCompactTHPUnusableIndexDiff = "numa_memory_compact_thp_unusable_index_diff"

	// metricNameNumaMemCompactTHPOrderPlusFreeSizeDiffBytes reports the increase in free memory held
	// by buddy blocks at or above the current THP order after compaction.
	metricNameNumaMemCompactTHPOrderPlusFreeSizeDiffBytes = "numa_memory_compact_thp_order_plus_free_size_diff_bytes"

	// metricNameNumaMemCompactEnabled is emitted once per handler cycle to report whether the
	// idle-NUMA compaction feature is currently enabled (value 1) or disabled (value 0) via the
	// dynamic EnableNumaMemCompact switch.
	metricNameNumaMemCompactEnabled = "numa_memory_compact_enabled"

	// metricNameNumaMemCompactTHPUnusableIndexDegraded reports whether the current THP-order
	// unusable index exceeds the allowed delta from the post-compaction baseline.
	metricNameNumaMemCompactTHPUnusableIndexDegraded = "numa_memory_compact_thp_unusable_index_degraded"

	// metricNameNumaMemCompactError is emitted whenever the handler hits a runtime error, tagged
	// with a "reason" describing what failed.
	metricNameNumaMemCompactError = "numa_memory_compact_error"

	// metricNameNumaMemCompactTaskDurationSeconds reports the elapsed duration of a running scan
	// with status=ongoing, and the final duration of scans that actually compact memory with
	// status=completed.
	metricNameNumaMemCompactTaskDurationSeconds = "numa_memory_compact_task_duration_seconds"
)

const (
	// error reasons tagged on metricNameNumaMemCompactError.
	errReasonNilInput                 = "nil_input"
	errReasonDynamicConfMissing       = "dynamic_config_missing"
	errReasonReadStateFailed          = "read_state_failed"
	errReasonReadTHPOrder             = "read_thp_order_failed"
	errReasonReadTHPUnusableIndex     = "read_thp_unusable_index_failed"
	errReasonReadTHPOrderPlusFreeSize = "read_thp_order_plus_free_size_failed"
)
