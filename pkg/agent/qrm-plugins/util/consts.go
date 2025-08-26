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

package util

const (
	// common metrics for all types of qrm plugins
	MetricNameHeartBeat                    = "heartbeat"
	MetricNameAllocateFailed               = "alloc_failed"
	MetricNameGetTopologyHintsFailed       = "get_topology_hints_failed"
	MetricNameRemovePodFailed              = "remove_pod_failed"
	MetricNameLWAdvisorServerFailed        = "lw_advisor_server_failed"
	MetricNameGetAdviceFailed              = "get_advice_failed"
	MetricNameGetAdviceFeatureNotSupported = "get_advice_feature_not_supported"
	MetricNameHandleAdvisorRespCalled      = "handle_advisor_resp_called"
	MetricNameHandleAdvisorRespFailed      = "handle_advisor_resp_failed"
	MetricNameAdvisorUnhealthy             = "advisor_unhealthy"

	// metrics for cpu plugin
	MetricNamePoolSize                    = "pool_size"
	MetricNameRealStateInvalid            = "real_state_invalid"
	MetricNameCPUSetInvalid               = "cpuset_invalid"
	MetricNameCPUSetOverlap               = "cpuset_overlap"
	MetricNameOrphanContainer             = "orphan_container"
	MetricNameGetMemBWPreferenceFailed    = "get_mem_bw_preference_failed"
	MetricNameGetNUMAAllocatedMemBWFailed = "get_numa_allocated_mem_bw_failed"
	MetricNameSetExclusiveIRQCPUSize      = "set_exclusive_irq_cpu_size"

	// metrics for memory plugin
	MetricNameMemSetInvalid                           = "memset_invalid"
	MetricNameMemSetOverlap                           = "memset_overlap"
	MetricNameNodeMemsetInvalid                       = "node_memset_invalid"
	MetricNameMemoryHandleAdvisorContainerEntryFailed = "memory_handle_advisor_container_entry_failed"
	MetricNameMemoryHandleAdvisorExtraEntryFailed     = "memory_handle_advisor_extra_entry_failed"
	MetricNameMemoryHandleAdvisorMemoryLimit          = "memory_handle_advisor_memory_limit"
	MetricNameMemoryHandleAdvisorDropCache            = "memory_handle_advisor_drop_cache"
	MetricNameMemoryHandleAdvisorCPUSetMems           = "memory_handle_advisor_cpuset_mems"
	MetricNameMemoryHandlerAdvisorMemoryOffload       = "memory_handler_advisor_memory_offloading"
	MetricNameMemoryHandlerAdvisorMemoryNUMAHeadroom  = "memory_handler_advisor_memory_numa_headroom"
	MetricNameMemoryOOMPriorityDeleteFailed           = "memory_oom_priority_delete_failed"
	MetricNameMemoryOOMPriorityUpdateFailed           = "memory_oom_priority_update_failed"
	MetricNameMemoryNumaBalance                       = "memory_handle_numa_balance"
	MetricNameMemoryNumaBalanceCost                   = "memory_numa_balance_cost"
	MetricNameMemoryNumaBalanceResult                 = "memory_numa_balance_result"

	// metrics for some cases
	MetricNameShareCoresNoEnoughResourceFailed = "share_cores_no_enough_resource"

	// metrics for numa allocation
	MetricNameMetricBasedNUMAAllocationEnabled = "metric_based_numa_allocation_enabled"
	MetricNameMetricBasedNUMAAllocationSuccess = "metric_based_numa_allocation_success"
	MetricNameCollectNUMAMetrics               = "collect_numa_metrics"
	MetricNameNUMAMetricOverThreshold          = "numa_metric_over_threshold"

	// metrics for irq tuning
	MetricNameIrqTuningEnabled                        = "irq_tuning_enabled"
	MetricNameIrqTuningPolicy                         = "irq_tuning_policy"
	MetricNameIrqTuningNicsCount                      = "irq_tuning_nics_count"
	MetricNameIrqTuningLowThroughputNicsCount         = "irq_tuning_low_throughput_nics_count"
	MetricNameIrqTuningNormalThroughputNicsCount      = "irq_tuning_normal_throughput_nics_count"
	MetricNameIrqTuningBalanceFair                    = "irq_tuning_balance_fair"
	MetricNameIrqTuningRPSEnabled                     = "irq_tuning_rps_enabled"
	MetricNameIrqTuningNicThroughputClass             = "irq_tuning_throughput_class"
	MetricNameIrqTuningSriovContainersCount           = "irq_tuning_sriov_containers_count"
	MetricNameIrqTuningIrqAffForbiddenContainersCount = "irq_tuning_irq_aff_forbidden_containers_count"
	MetricNameIrqTuningNicIrqAffinityPolicy           = "irq_tuning_nic_irq_affinity_policy"
	MetricNameIrqTuningNicExclusiveIrqCores           = "irq_tuning_nic_exclusive_irq_cores"
	MetricNameIrqTuningTotalExclusiveIrqCores         = "irq_tuning_total_exclusive_irq_cores"
	MetricNameIrqTuningNicIrqLoadBalance              = "irq_tuning_nic_irq_load_balance"
	MetricNameIrqTuningNicExclusiveIrqCoresIrqUtilAvg = "irq_tuning_nic_exclusive_irq_cores_irq_util_avg"
	MetricNameIrqTuningNicExclusiveIrqCoresIrqUtilMax = "irq_tuning_nic_exclusive_irq_cores_irq_util_Max"
	MetricNameIrqTuningNicExclusiveIrqCoresIrqUtilMin = "irq_tuning_nic_exclusive_irq_cores_irq_util_Min"
	MetricNameIrqTuningNicExclusiveIrqCoresIrqUsage   = "irq_tuning_nic_exclusive_irq_cores_irq_usage"
	MetricNameIrqTuningNicExclusiveIrqCoresCpuUtilAvg = "irq_tuning_nic_exclusive_irq_cores_cpu_util_avg"
	MetricNameIrqTuningNicExclusiveIrqCoresCpuUtilMax = "irq_tuning_nic_exclusive_irq_cores_cpu_util_Max"
	MetricNameIrqTuningNicExclusiveIrqCoresCpuUtilMin = "irq_tuning_nic_exclusive_irq_cores_cpu_util_Min"
	MetricNameIrqTuningNicExclusiveIrqCoresCpuUsage   = "irq_tuning_nic_exclusive_irq_cores_cpu_usage"
	MetricNameIrqTuningErr                            = "irq_tuning_err"
)

const (
	MetricTagNameInplaceUpdateResizing = "inplaceUpdateResizing"
)

// those are OCI property names to be used by QRM plugins
const (
	OCIPropertyNameCPUSetCPUs         = "CpusetCpus"
	OCIPropertyNameCPUSetMems         = "CpusetMems"
	OCIPropertyNameMemoryLimitInBytes = "MemoryLimitInBytes"
)

const (
	//[TODO]: move them to apiserver
	PodAnnotationQuantityFromQRMDeclarationKey  = "qrm.katalyst.kubewharf.io/quantity-from-qrm-declaration"
	PodAnnotationQuantityFromQRMDeclarationTrue = "true"
	PodAnnotationResourceReallocationKey        = "qrm.katalyst.kubewharf.io/resource-reallocation"
)

const QRMTimeFormat = "2006-01-02 15:04:05.999999999 -0700 MST"

const QRMPluginPolicyTagName = "policy"

const (
	AdvisorRPCMetadataKeySupportsGetAdvice   = "supports_get_advice"
	AdvisorRPCMetadataValueSupportsGetAdvice = "true"

	// resctrl related annotations
	AnnotationRdtClosID           = "rdt.resources.beta.kubernetes.io/pod"
	AnnotationRdtNeedPodMonGroups = "rdt.resources.beta.kubernetes.io/need-mon-groups"
)
