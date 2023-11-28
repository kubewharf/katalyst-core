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

const EnableSetSockMemPeriodicalHandlerName = "SetSockMem"

const (
	// Constants for global tcpmem ratio
	globalTCPMemRatioMin float64 = 20.0 // min ratio for host tcp mem: 20%
	globalTCPMemRatioMax float64 = 80.0 // max ratio for host tcp mem: 80%
	hostTCPMemFile               = "/proc/sys/net/ipv4/tcp_mem"

	// Constants for cgroupv1 tcpmem statistics
	kernSockMemAccountingOn float64 = 9223372036854767616.0

	// Constants for cgroupv1 tcpmem ratio
	cgroupTCPMemMin2G    float64 = 2147483648.0 // static min value for pod's sockmem: 2G
	cgroupTCPMemRatioMin float64 = 20.0         // min ratio for pod's sockmem: 20%
	cgroupTCPMemRatioMax float64 = 200.0        // max ratio for pod's sockmem: 200%
)

const (
	metricNameTCPMemoryHost   = "async_handler_tcp_memory_host"
	metricNameTCPMemoryCgroup = "async_handler_tcp_memory_cgroup"
)
