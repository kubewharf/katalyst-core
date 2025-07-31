package manager

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"

	"github.com/prometheus/procfs/sysfs"
)

var (
	initManagerOnce sync.Once
	sysFSManager    SysFSManager
)

type SysFSManager interface {
	GetSystemCPUs() ([]sysfs.CPU, error)
	GetCPUTopology(cpuID string) (*sysfs.CPUTopology, error)
	GetNicRxQueueRPS(nic string, queue int) (string, error)

	SetNicRxQueueRPS(nic string, queue int, destCPUSet machine.CPUSet) error
}

func GetSysFsManager() SysFSManager {
	initManagerOnce.Do(func() {
		sysFSManager = NewSysFsManager()
	})
	return sysFSManager
}
