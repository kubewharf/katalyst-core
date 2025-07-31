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
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	// CPUResourcePluginPolicyNameDynamic is the name of the dynamic policy.
	CPUResourcePluginPolicyNameDynamic = string(consts.ResourcePluginPolicyNameDynamic)

	// CPUResourcePluginPolicyNameNative is the name of the native policy.
	CPUResourcePluginPolicyNameNative = string(consts.ResourcePluginPolicyNameNative)

	CPUPluginDynamicPolicyName = "qrm_cpu_plugin_" + CPUResourcePluginPolicyNameDynamic
	ClearResidualState         = CPUPluginDynamicPolicyName + "_clear_residual_state"
	CheckCPUSet                = CPUPluginDynamicPolicyName + "_check_cpuset"
	SyncCPUIdle                = CPUPluginDynamicPolicyName + "_sync_cpu_idle"
	IRQTuning                  = CPUPluginDynamicPolicyName + "_irq_tuning"

	CommunicateWithAdvisor = CPUPluginDynamicPolicyName + "_communicate_with_advisor"
)

const (
	// CPUStateAnnotationKeyNUMAHint is the key stored in allocationInfo.Annotations
	// to indicate NUMA hint for the entry
	CPUStateAnnotationKeyNUMAHint = "numa_hint"
)

const (
	// CPUIncrRatioSharedCoresNUMABinding will be multiplied to the shared_cores with numa_biding entry request
	// and be used to increment pool size
	CPUIncrRatioSharedCoresNUMABinding = 2.0

	// CPUIncrRatioDefault is the default value be multiplied to the entry request
	// and be used to increment pool size
	CPUIncrRatioDefault = 1.0
)

const (
	// CPUNUMAHintPreferPolicyPacking refers to the strategy of putting as many containers as possible onto a single NUMA node in order to utilize the resources efficiently and reduce fragmentation.
	CPUNUMAHintPreferPolicyPacking = "packing"
	// CPUNUMAHintPreferPolicySpreading tries to distributing containers across multiple nodes. Aiming to balance the load by avoiding overloading individual nodes.
	CPUNUMAHintPreferPolicySpreading = "spreading"
	// CPUNUMAHintPreferPolicyDynamicPacking refers to the strategy of putting as many containers as possible onto a single NUMA node until the node hits configurable threshold.
	// if all nodes hit configurable threshold, use spreading policy instead.
	CPUNUMAHintPreferPolicyDynamicPacking = "dynamic_packing"
	// CPUNUMAHintPreferPolicyNone refers to the strategy of putting containers onto any NUMA node which is not overloaded.
	CPUNUMAHintPreferPolicyNone = "none"
)
