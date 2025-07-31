package manager

import (
	"github.com/prometheus/procfs/sysfs"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func GetSystemCPUs() ([]sysfs.CPU, error) {
	return GetSysFsManager().GetSystemCPUs()
}

func GetCPUTopology(string string) (*sysfs.CPUTopology, error) {
	return GetSysFsManager().GetCPUTopology(string)
}

func GetNicRxQueueRPS(nic string, queue int) (string, error) {
	return GetSysFsManager().GetNicRxQueueRPS(nic, queue)
}

func SetNicRxQueueRPS(nic string, queue int, destCPUSet machine.CPUSet) error {
	return GetSysFsManager().SetNicRxQueueRPS(nic, queue, destCPUSet)
}
