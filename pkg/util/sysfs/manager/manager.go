package manager

import (
	"sync"

	"github.com/prometheus/procfs/sysfs"
)

var (
	initManagerOnce sync.Once
	sysFSManager    SysFSManager
)

type SysFSManager interface {
	GetSystemCPUs() ([]sysfs.CPU, error)
	GetCPUTopology(string string) (*sysfs.CPUTopology, error)
}

func GetSysFsManager() SysFSManager {
	initManagerOnce.Do(func() {
		sysFSManager = NewSysFsManager()
	})
	return sysFSManager
}
