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

package manager

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/prometheus/procfs"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/procfs/common"
)

const (
	IrqRootPath    = "/proc/irq"
	InterruptsFile = "/proc/interrupts"
)

type manager struct {
	procfs procfs.FS
}

// NewProcFSManager return a manager for procfs
func NewProcFSManager() *manager {
	fs, _ := procfs.NewDefaultFS()

	return &manager{fs}
}

// GetCPUInfo returns the CPUInfo of the host.
func (m *manager) GetCPUInfo() ([]procfs.CPUInfo, error) {
	return m.procfs.CPUInfo()
}

// GetProcStat returns the Stat of the host.
func (m *manager) GetProcStat() (procfs.Stat, error) {
	return m.procfs.Stat()
}

// GetPidComm returns the comm of the given pid.
func (m *manager) GetPidComm(pid int) (string, error) {
	proc, err := m.procfs.Proc(pid)
	if err != nil {
		general.Errorf("[Porcfs] get pid %d proc failed, err: %v", pid, err)
		return "", err
	}
	comm, err := proc.Comm()
	if err != nil {
		general.Errorf("[Porcfs] get pid %d comm failed, err: %v", pid, err)
		return "", err
	}

	return comm, nil
}

// GetPidCmdline returns the cmdline of the given pid.
func (m *manager) GetPidCmdline(pid int) ([]string, error) {
	proc, err := m.procfs.Proc(pid)
	if err != nil {
		general.Errorf("[Porcfs] get pid %d proc failed, err: %v", pid, err)
		return nil, err
	}

	data, err := proc.CmdLine()
	if err != nil {
		general.Errorf("[Porcfs] get pid %d cmdline failed, err: %v", pid, err)
		return nil, err
	}

	return data, nil
}

// GetPidCgroups returns the cgroups of the given pid.
func (m *manager) GetPidCgroups(pid int) ([]procfs.Cgroup, error) {
	proc, err := m.procfs.Proc(pid)
	if err != nil {
		general.Errorf("[Porcfs] get pid %d proc failed, err: %v", pid, err)
		return nil, err
	}

	cgroups, err := proc.Cgroups()
	if err != nil {
		general.Errorf("[Porcfs] get pid %d cgroup failed, err: %v", pid, err)
		return nil, err
	}

	return cgroups, nil
}

// GetMounts returns the mounts of the host.
func (m *manager) GetMounts() ([]*procfs.MountInfo, error) {
	return procfs.GetMounts()
}

// GetProcMounts returns the mounts of the given pid.
func (m *manager) GetProcMounts(pid int) ([]*procfs.MountInfo, error) {
	return procfs.GetProcMounts(pid)
}

// GetIPVSStats returns the ipvs stats of the host.
func (m *manager) GetIPVSStats() (procfs.IPVSStats, error) {
	return m.procfs.IPVSStats()
}

// GetNetDev returns the net dev stats of the host.
func (m *manager) GetNetDev() (map[string]procfs.NetDevLine, error) {
	return m.procfs.NetDev()
}

// GetNetStat returns the net stat stats of the host.
func (m *manager) GetNetStat() ([]procfs.NetStat, error) {
	return m.procfs.NetStat()
}

// GetNetSoftnetStat returns the net softnet stat stats of the host.
func (m *manager) GetNetSoftnetStat() ([]procfs.SoftnetStat, error) {
	return m.procfs.NetSoftnetStat()
}

// GetNetTCP returns the net tcp stats of the host.
func (m *manager) GetNetTCP() (procfs.NetTCP, error) {
	return m.procfs.NetTCP()
}

// GetNetTCP6 returns the net tcp6 stats of the host.
func (m *manager) GetNetTCP6() (procfs.NetTCP, error) {
	return m.procfs.NetTCP6()
}

// GetNetUDP returns the net udp stats of the host.
func (m *manager) GetNetUDP() (procfs.NetUDP, error) {
	return m.procfs.NetUDP()
}

// GetNetUDP6 returns the net udp6 stats of the host.
func (m *manager) GetNetUDP6() (procfs.NetUDP, error) {
	return m.procfs.NetUDP6()
}

// GetSoftirqs returns the softirqs stats of the host.
func (m *manager) GetSoftirqs() (procfs.Softirqs, error) {
	return m.procfs.Softirqs()
}

// GetProcInterrupts returns the proc interrupts stats of the host.
func (m *manager) GetProcInterrupts() (procfs.Interrupts, error) {
	data, err := ReadFileNoStat(InterruptsFile)
	if err != nil {
		general.Errorf("[Porcfs] get /proc/interrupts failed, err: %v", err)
		return nil, err
	}
	return parseInterrupts(bytes.NewReader(data))
}

// GetPSIStatsForResource returns the psi stats of the given resource.
func (m *manager) GetPSIStatsForResource(resourceName string) (procfs.PSIStats, error) {
	return m.procfs.PSIStatsForResource(resourceName)
}

// GetSchedStat returns the sched stat stats of the host.
func (m *manager) GetSchedStat() (*procfs.Schedstat, error) {
	return m.procfs.Schedstat()
}

// ApplyProcInterrupts apply the proc interrupts for the given irq number and cpuset.
func (m *manager) ApplyProcInterrupts(irqNumber int, cpuset string) error {
	if irqNumber < 0 {
		return fmt.Errorf("invalid IRQ number: %d ", irqNumber)
	}

	dir := fmt.Sprintf("/proc/irq/%d", irqNumber)
	if err, applied, oldData := common.InstrumentedWriteFileIfChange(dir, "smp_affinity_list", cpuset); err != nil {
		return err
	} else if applied {
		general.Infof("[Procfs] apply proc interrupts successfully, irq number: %v, data: %v, old data: %v\n", irqNumber, cpuset, oldData)
	}

	return nil
}

// ReadFileNoStat uses io.ReadAll to read contents of entire file.
// This is similar to os.ReadFile but without the call to os.Stat, because
// many files in /proc and /sys report incorrect file sizes (either 0 or 4096).
// Reads a max file size of 1024kB.  For files larger than this, a scanner
// should be used.
func ReadFileNoStat(filename string) ([]byte, error) {
	const maxBufferSize = 1024 * 1024

	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := io.LimitReader(f, maxBufferSize)
	return io.ReadAll(reader)
}

func parseInterrupts(r io.Reader) (procfs.Interrupts, error) {
	var (
		interrupts = procfs.Interrupts{}
		scanner    = bufio.NewScanner(r)
	)

	if !scanner.Scan() {
		return nil, errors.New("interrupts empty")
	}
	cpuNum := len(strings.Fields(scanner.Text())) // one header per cpu

	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 { // skip empty lines
			continue
		}
		if len(parts) < 2 {
			return nil, fmt.Errorf("not enough fields in interrupts (expected 2+ fields but got %d): %v", len(parts), parts)
		}
		intName := parts[0][:len(parts[0])-1] // remove trailing :

		if len(parts) == 2 {
			interrupts[intName] = procfs.Interrupt{
				Info:    "",
				Devices: "",
				Values: []string{
					parts[1],
				},
			}
			continue
		}

		if len(parts) < cpuNum+2 {
			general.Warningf("[Procfs]Unexpected number of fields in interrupts (expected %d but got %d): %v", cpuNum+2, len(parts), parts)
			return nil, fmt.Errorf("unexpected number of fields in interrupts (expected %d but got %d): %v", cpuNum+2, len(parts), parts)
		}

		intr := procfs.Interrupt{
			Values: parts[1 : cpuNum+1],
		}

		if _, err := strconv.Atoi(intName); err == nil { // numeral interrupt
			intr.Info = parts[cpuNum+1]
			intr.Devices = strings.Join(parts[cpuNum+2:], " ")
		} else {
			intr.Info = strings.Join(parts[cpuNum+1:], " ")
		}
		interrupts[intName] = intr
	}

	return interrupts, scanner.Err()
}
