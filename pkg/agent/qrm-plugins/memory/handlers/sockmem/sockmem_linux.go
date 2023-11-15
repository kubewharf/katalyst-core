//go:build linux
// +build linux

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

package sockmem

import (
	"fmt"

	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"golang.org/x/sys/unix"
)

type SockMemConfig struct {
	globalTCPMemRatio int
}

var sockMemConfig = SockMemConfig{
	globalTCPMemRatio: 20, // default: 20% * {host total memory}
}

func updateGlobalTCPMemRatio(ratio int) {
	if ratio < globalTCPMemRatioMin {
		ratio = globalTCPMemRatioMin
	} else if ratio > globalTCPMemRatioMax {
		ratio = globalTCPMemRatioMax
	}
	sockMemConfig.globalTCPMemRatio = ratio
}

func setHostTCPMem(memTotal uint64) error {
	tcpMemRatio := sockMemConfig.globalTCPMemRatio
	tcpMem, err := getHostTCPMemFile(hostTCPMemFile)
	if err != nil {
		fmt.Println("Error:", err)
		return err
	}

	pageSize := uint64(unix.Getpagesize())
	newUpperLimit := memTotal / pageSize / 100 * uint64(tcpMemRatio)
	if (newUpperLimit != tcpMem[2]) && (newUpperLimit > tcpMem[1]) {
		general.Infof("write to host tcp_mem, ratio=%d, newLimit=%d, oldLimit=%d", tcpMemRatio, newUpperLimit, tcpMem[2])
		tcpMem[2] = newUpperLimit
		setHostTCPMemFile(hostTCPMemFile, tcpMem)
	}
	return nil
}

/*
 * SetSockMemLimit is the unified solution for tcpmem limitation.
 * it includes 3 parts:
 * 1, set the global tcpmem limitation by changing net.ipv4.tcp_mem.
 * 2, do nothing under cgroupv2.
 * 3, set the cgroup tcpmem limitation under cgroupv1.
 */
func SetSockMemLimit(conf *coreconfig.Configuration,
	_ interface{}, _ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter, metaServer *metaserver.MetaServer) {
	general.Infof("called")

	// SettingSockMem featuregate.
	if !conf.EnableSettingSockMem {
		general.Infof("SetSockMemLimit disabled")
		return
	} else if metaServer == nil {
		general.Errorf("nil metaServer")
		return
	}

	updateGlobalTCPMemRatio(conf.SetGlobalTCPMemRatio)

	/*
	 * Step1, set the [limit] value for host net.ipv4.tcp_mem.
	 *
	 * Description of net.ipv4.tcp_mem:
	 * It includes 3 parts: min, pressure, limit.
	 * The format is like the following:
	 * net.ipv4.tcp_mem = [min] [pressure] [limit]
	 *
	 * Each parts means:
	 * [min]: represents the minimum number of pages allowed in the queue.
	 * [pressure]: represents the threshold at which the system considers memory
	 *   to be under pressure due to TCP socket usage. When the memory usage reaches
	 *   this value, the system may start taking actions like cleaning up or reclaiming memory.
	 * [limit]: indicates the maximum number of pages allowed in the queue.
	 */
	_ = setHostTCPMem(metaServer.MemoryCapacity)

	// Step2, do nothing for cg2.
	if common.CheckCgroup2UnifiedMode() {
		general.Infof("skip setSockMemLimit in cg2 env")
		return
	}

	// Step3, set tcp_mem accounting for pods under cgroupv1.
	// to-do
}
