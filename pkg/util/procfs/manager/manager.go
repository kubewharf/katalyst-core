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
	"sync"

	"github.com/prometheus/procfs"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	initManagerOnce sync.Once
	procFSManager   ProcFSManager
)

type ProcFSManager interface {
	GetCPUInfo() ([]procfs.CPUInfo, error)
	GetProcStat() (procfs.Stat, error)
	GetPidComm(pid int) (string, error)
	GetPidCmdline(pid int) ([]string, error)
	GetPidCgroups(pid int) ([]procfs.Cgroup, error)
	GetMounts() ([]*procfs.MountInfo, error)
	GetProcMounts(pid int) ([]*procfs.MountInfo, error)
	GetIPVSStats() (procfs.IPVSStats, error)
	GetNetDev() (map[string]procfs.NetDevLine, error)
	GetNetStat() ([]procfs.NetStat, error)
	GetNetTCP() (procfs.NetTCP, error)
	GetNetTCP6() (procfs.NetTCP, error)
	GetNetUDP() (procfs.NetUDP, error)
	GetNetUDP6() (procfs.NetUDP, error)
	GetSoftirqs() (procfs.Softirqs, error)
	GetProcInterrupts() (procfs.Interrupts, error)
	GetPSIStatsForResource(resourceName string) (procfs.PSIStats, error)
	GetSchedStat() (*procfs.Schedstat, error)

	ApplyProcInterrupts(irqNumber int, cpuset machine.CPUSet) error
}

// GetProcFSManager returns the singleton ProcFSManager.
func GetProcFSManager() ProcFSManager {
	initManagerOnce.Do(func() {
		procFSManager = NewProcFSManager()
	})
	return procFSManager
}
