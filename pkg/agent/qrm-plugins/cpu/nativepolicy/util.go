package nativepolicy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateMachineStateFromPodEntries(topology *machine.CPUTopology, podEntries state.PodEntries) (state.NUMANodeMap, error) {
	return state.GenerateMachineStateFromPodEntries(topology, podEntries, coreconsts.CPUResourcePluginPolicyNameNative)
}
