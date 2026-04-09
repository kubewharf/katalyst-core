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

package userwatermark

const (
	MetricNameUserWatermarkReclaimEnabled             = "user_watermark_reclaim_enabled"
	MetricNameUserWatermarkReclaimResult              = "user_watermark_reclaim_result"
	MetricNameUserWatermarkReclaimFailedCount         = "user_watermark_reclaim_failed_count"
	MetricNameUserWatermarkReclaimCost                = "user_watermark_reclaim_cost"
	MetricNameUserWatermarkReclaimPSI                 = "user_watermark_reclaim_psi_avg_60"
	MetricNameUserWatermarkReclaimMemoryFree          = "user_watermark_reclaim_memory_free"
	MetricNameUserWatermarkReclaimRefault             = "user_watermark_reclaim_refault"
	MetricNameUserWatermarkReclaimAccuracyRatio       = "user_watermark_reclaim_accuracy_ratio"
	MetricNameUserWatermarkReclaimScanEfficiencyRatio = "user_watermark_reclaim_scan_efficiency_ratio"

	MetricTagKeyCGroupPath    = "cgroup_path"
	MetricTagKeyQosLevel      = "qos_level"
	MetricTagKeyObjectName    = "object_name"
	MetricTagKeyContainerName = "container_name"
	MetricTagKeySuccess       = "success"
	MetricTagKeyReason        = "reason"
	MetricTagKeyHighWaterMark = "high_watermark"
	MetricTagKeyLowWaterMark  = "low_watermark"
	MetricTagKeyReclaimTarget = "reclaim_target"
	MetricTagKeyReclaimedSize = "reclaimed_size"
)
