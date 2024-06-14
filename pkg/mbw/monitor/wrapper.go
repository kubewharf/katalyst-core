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

package monitor

import (
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// this file is non-functional wrapper to improve testability of mbw monitor
// The wrapper introduced here has no functional significance at all

var machineWrapper WrapperMachine = &machineProd{}

// wrapper of machine module functions
type WrapperMachine interface {
	GetKatalystMachineInfo(conf *global.MachineInfoConfiguration) (*machine.KatalystMachineInfo, error)
}

type machineProd struct{}

func (m machineProd) GetKatalystMachineInfo(conf *global.MachineInfoConfiguration) (*machine.KatalystMachineInfo, error) {
	return machine.GetKatalystMachineInfo(conf)
}
