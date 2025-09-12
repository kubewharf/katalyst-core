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
