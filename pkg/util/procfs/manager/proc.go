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
	"github.com/prometheus/procfs"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GetCPUInfo returns the CPUInfo of the host.
func GetCPUInfo() ([]procfs.CPUInfo, error) {
	return GetProcFSManager().GetCPUInfo()
}

// GetProcStat returns the Stat of the host.
func GetProcStat() (procfs.Stat, error) {
	return GetProcFSManager().GetProcStat()
}

// GetPidComm returns the comm of the given pid.
func GetPidComm(pid int) (string, error) {
	return GetProcFSManager().GetPidComm(pid)
}

// GetPidCmdline returns the cmdline of the given pid.
func GetPidCmdline(pid int) ([]string, error) {
	return GetProcFSManager().GetPidCmdline(pid)
}

// GetPidCgroups returns the cgroups of the given pid.
func GetPidCgroups(pid int) ([]procfs.Cgroup, error) {
	return GetProcFSManager().GetPidCgroups(pid)
}

// GetMounts returns the mounts of the host.
func GetMounts() ([]*procfs.MountInfo, error) {
	return GetProcFSManager().GetMounts()
}

// GetProcMounts returns the mounts of the given pid.
func GetProcMounts(pid int) ([]*procfs.MountInfo, error) {
	return GetProcFSManager().GetProcMounts(pid)
}

// GetIPVSStats returns the IPVSStats of the host.
func GetIPVSStats() (procfs.IPVSStats, error) {
	return GetProcFSManager().GetIPVSStats()
}

// GetNetDev returns the net dev of the host.
func GetNetDev() (map[string]procfs.NetDevLine, error) {
	return GetProcFSManager().GetNetDev()
}

// GetNetStat returns the net stat of the host.
func GetNetStat() ([]procfs.NetStat, error) {
	return GetProcFSManager().GetNetStat()
}

// GetNetTCP returns the net TCP of the host.
func GetNetTCP() (procfs.NetTCP, error) {
	return GetProcFSManager().GetNetTCP()
}

// GetNetTCP6 returns the net TCP6 of the host.
func GetNetTCP6() (procfs.NetTCP, error) {
	return GetProcFSManager().GetNetTCP6()
}

// GetNetUDP returns the net UDP of the host.
func GetNetUDP() (procfs.NetUDP, error) {
	return GetProcFSManager().GetNetUDP()
}

// GetNetUDP6 returns the net UDP6 of the host.
func GetNetUDP6() (procfs.NetUDP, error) {
	return GetProcFSManager().GetNetUDP6()
}

// GetSoftirqs returns the softirqs of the host.
func GetSoftirqs() (procfs.Softirqs, error) {
	return GetProcFSManager().GetSoftirqs()
}

// GetProcInterrupts returns the proc interrupts of the host.
func GetProcInterrupts() (procfs.Interrupts, error) {
	return GetProcFSManager().GetProcInterrupts()
}

// GetPSIStatsForResource returns the psi stats for the given resource.
func GetPSIStatsForResource(resourceName string) (procfs.PSIStats, error) {
	return GetProcFSManager().GetPSIStatsForResource(resourceName)
}

// GetSchedStat returns the sched stat of the host.
func GetSchedStat() (*procfs.Schedstat, error) {
	return GetProcFSManager().GetSchedStat()
}

// ApplyProcInterrupts applies the given cpuset to the given irq number.
func ApplyProcInterrupts(irqNumber int, cpuset machine.CPUSet) error {
	return GetProcFSManager().ApplyProcInterrupts(irqNumber, cpuset)
}
