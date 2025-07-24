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
	GetCPUTopology(cpuID string) (*sysfs.CPUTopology, error)
	GetNicRxQueueRPS(sysPath, nic string, queue int) (string, error)

	SetNicRxQueueRPS(sysPath, nic string, queue int, rpsConf string) error
}

func GetSysFsManager() SysFSManager {
	initManagerOnce.Do(func() {
		sysFSManager = NewSysFsManager()
	})
	return sysFSManager
}
