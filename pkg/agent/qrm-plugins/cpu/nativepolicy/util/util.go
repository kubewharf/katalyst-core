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
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GenerateMachineStateFromPodEntries for native policy
func GenerateMachineStateFromPodEntries(topology *machine.CPUTopology, podEntries state.PodEntries, _ state.NUMANodeMap) (state.NUMANodeMap, error) {
	return state.GenerateMachineStateFromPodEntriesByPolicy(topology, podEntries, cpuconsts.CPUResourcePluginPolicyNameNative)
}
