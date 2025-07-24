package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/prometheus/procfs/sysfs"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/sysfs/common"
)

const (
	ClassNetBasePath = "class/net"
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

func (m *manager) GetCPUTopology(cpuID string) (*sysfs.CPUTopology, error) {
	cpu, exist := m.cpus[cpuID]
	if !exist {
		return nil, fmt.Errorf("the specified cpu does not exist")
	}

	return cpu.Topology()
}

func (m *manager) GetNicRxQueueRPS(sysPath, nic string, queue int) (string, error) {
	nicSysDir := filepath.Join(sysPath, ClassNetBasePath, nic)
	queueRPSPath := fmt.Sprintf("%s/queues/rx-%d/rps_cpus", nicSysDir, queue)
	if _, err := os.Stat(queueRPSPath); err != nil && os.IsNotExist(err) {
		return "", fmt.Errorf("%s is not exist in nic %s", queueRPSPath, nicSysDir)
	}

	b, err := os.ReadFile(queueRPSPath)
	if err != nil {
		return "", fmt.Errorf("failed to ReadFile(%s), err %s", queueRPSPath, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}

func (m *manager) SetNicRxQueueRPS(sysPath, nic string, queue int, rpsConf string) error {
	nicSysDir := filepath.Join(sysPath, ClassNetBasePath, nic)
	queuePath := fmt.Sprintf("%s/queues/rx-%d", nicSysDir, queue)

	if err, applied, oldData := common.InstrumentedWriteFileIfChange(queuePath, "rps_cpus", rpsConf); err != nil {
		return err
	} else if applied {
		general.Infof("[Sysfs] set nic rx queue RPS successfully, nic: %v, queue: %v, data:%v, old data: %v\n", nic, queue, rpsConf, oldData)
	}

	return nil
}
