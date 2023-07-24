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

import (
	"context"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

// AllocationHandler and HintHandler are used as standard functions
// for qrm plugins to acquire resource allocation/hint info
type AllocationHandler func(context.Context, *pluginapi.ResourceRequest) (*pluginapi.ResourceAllocationResponse, error)
type HintHandler func(context.Context, *pluginapi.ResourceRequest) (*pluginapi.ResourceHintsResponse, error)

const (
	MetricNameAllocateByCPUAdvisorServerCalled = "alloc_by_cpu_advisor_server_called"
	MetricNameHandleAdvisorRespCalled          = "handle_advisor_resp_called"
	MetricNameIsolatedPodNum                   = "isolated_pod_num"
	MetricNamePoolSize                         = "pool_size"
	MetricNameAllocateByCPUAdvisorServerFailed = "alloc_by_cpu_advisor_server_failed"
	MetricNameHandleAdvisorRespFailed          = "handle_advisor_resp_failed"
	MetricNameAllocateFailed                   = "alloc_failed"
	MetricNameGetTopologyHintsFailed           = "get_topology_hints_failed"
	MetricNameRemovePodFailed                  = "remove_pod_failed"
	MetricNameLWCPUAdvisorServerFailed         = "lw_cpu_advisor_server_failed"
	MetricNameLWMemoryAdvisorServerFailed      = "lw_memory_advisor_server_failed"
	MetricNameHeartBeat                        = "heartbeat"
	MetricNameRealStateInvalid                 = "real_state_invalid"
	MetricNameCPUSetInvalid                    = "cpuset_invalid"
	MetricNameCPUSetOverlap                    = "cpuset_overlap"
	MetricNameMemSetInvalid                    = "memset_invalid"
	MetricNameMemSetOverlap                    = "memset_overlap"
	MetricNameNodeMemsetInvalid                = "node_memset_invalid"
)

// those are OCI property names to be used by QRM plugins
const (
	OCIPropertyNameCPUSetCPUs         = "CpusetCpus"
	OCIPropertyNameCPUSetMems         = "CpusetMems"
	OCIPropertyNameMemoryLimitInBytes = "MemoryLimitInBytes"
)

const QRMTimeFormat = "2006-01-02 15:04:05.999999999 -0700 MST"

const QRMPluginPolicyTagName = "policy"
