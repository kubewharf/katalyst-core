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

package consts

import (
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	MemoryResourcePluginPolicyNameDynamic = string(apiconsts.ResourcePluginPolicyNameDynamic)

	MemoryPluginDynamicPolicyName = "qrm_memory_plugin_" + MemoryResourcePluginPolicyNameDynamic
	ClearResidualState            = MemoryPluginDynamicPolicyName + "_clear_residual_state"
	SyncMemoryStateFromSpec       = MemoryPluginDynamicPolicyName + "_sync_memory_state_form_spec"
	CheckMemSet                   = MemoryPluginDynamicPolicyName + "_check_mem_set"
	ApplyExternalCGParams         = MemoryPluginDynamicPolicyName + "_apply_external_cg_params"
	SetExtraControlKnob           = MemoryPluginDynamicPolicyName + "_set_extra_control_knob"
	OOMPriority                   = MemoryPluginDynamicPolicyName + "_oom_priority"
	SetSockMem                    = MemoryPluginDynamicPolicyName + "_set_sock_mem"
	CommunicateWithAdvisor        = MemoryPluginDynamicPolicyName + "_communicate_with_advisor"
	DropCache                     = MemoryPluginDynamicPolicyName + "_drop_cache"
	EvictLogCache                 = MemoryPluginDynamicPolicyName + "_evict_log_cache"
)
