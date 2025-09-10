//go:build !linux && !windows
// +build !linux,!windows

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

package machine

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

// GetExtraNetworkInfo get network info from /sys/class/net and system function net.Interfaces.
// if multiple network namespace is enabled, we should exec into all namespaces and parse nics for them.
func GetExtraNetworkInfo(_ *global.MachineInfoConfiguration) (*ExtraNetworkInfo, error) {
	return &ExtraNetworkInfo{}, nil
}

func DoNetNS(nsName, nsAbsPath string, cb func(sysFsDir string, nsAbsPath string) error) error {
	return cb("", nsAbsPath)
}

func GetNetDevRxPackets(nic *NicBasicInfo) (uint64, error) {
	return 0, nil
}

func GetIrqsAffinityCPUs(irqs []int) (map[int][]int64, error) {
	return map[int][]int64{}, nil
}

func CollectSoftNetStats(onlineCpus map[int64]bool) (map[int64]*SoftNetStat, error) {
	return map[int64]*SoftNetStat{}, nil
}

func GetNicRxQueuePackets(nic *NicBasicInfo) (map[int]uint64, error) {
	return map[int]uint64{}, nil
}

func ListActiveUplinkNics(netNSDir string) ([]*NicBasicInfo, error) {
	return []*NicBasicInfo{}, nil
}
