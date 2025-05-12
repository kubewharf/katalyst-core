package manager

import "github.com/prometheus/procfs/sysfs"

func GetSystemCPUs() ([]sysfs.CPU, error) {
	return GetSysFsManager().GetSystemCPUs()
}

func GetCPUTopology(string string) (*sysfs.CPUTopology, error) {
	return GetSysFsManager().GetCPUTopology(string)
}
