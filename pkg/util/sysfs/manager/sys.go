package manager

import (
	"github.com/prometheus/procfs/sysfs"
)

func GetSystemCPUs() ([]sysfs.CPU, error) {
	return GetSysFsManager().GetSystemCPUs()
}

func GetCPUTopology(string string) (*sysfs.CPUTopology, error) {
	return GetSysFsManager().GetCPUTopology(string)
}

func GetNicRxQueueRPS(sysPath, nic string, queue int) (string, error) {
	return GetSysFsManager().GetNicRxQueueRPS(sysPath, nic, queue)
}

func SetNicRxQueueRPS(sysPath, nic string, queue int, rpsConf string) error {
	return GetSysFsManager().SetNicRxQueueRPS(sysPath, nic, queue, rpsConf)
}
