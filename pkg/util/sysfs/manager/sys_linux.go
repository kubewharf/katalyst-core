package manager

import (
	"fmt"

	"github.com/prometheus/procfs/sysfs"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type manager struct {
	sys  sysfs.FS
	cpus map[string]sysfs.CPU
}

func NewSysFsManager() *manager {
	sys, _ := sysfs.NewDefaultFS()
	cpuMaps := make(map[string]sysfs.CPU)

	cpus, err := sys.CPUs()
	if err != nil {
		general.Warningf("could not get CPU info: %s", err)
	}

	for _, cpu := range cpus {
		cpuMaps[cpu.Number()] = cpu
	}

	m := &manager{sys, cpuMaps}

	return m
}

// GetSystemCPUs returns a slice of all CPUs in `/sys/devices/system/cpu`.
func (m *manager) GetSystemCPUs() ([]sysfs.CPU, error) {
	return m.sys.CPUs()
}

func (m *manager) GetCPUTopology(string string) (*sysfs.CPUTopology, error) {
	cpu, exist := m.cpus[string]
	if !exist {
		return nil, fmt.Errorf("the specified cpu does not exist")
	}

	return cpu.Topology()
}
