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
	msrutils "github.com/kubewharf/katalyst-core/pkg/mbw/utils/msr"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// this file is non-functional wrappers to improve testability of mbw monitor
// The wrappers introduced here have no functional significance by themselves

var (
	machineWrapper WrapperMachine = &machineProd{}
	msrSingleton   WrapperMSR     = &msrProd{}
)

// wrapper of machine module functions
type WrapperMachine interface {
	GetKatalystMachineInfo(conf *global.MachineInfoConfiguration) (*machine.KatalystMachineInfo, error)
}

type machineProd struct{}

func (m machineProd) GetKatalystMachineInfo(conf *global.MachineInfoConfiguration) (*machine.KatalystMachineInfo, error) {
	return machine.GetKatalystMachineInfo(conf)
}

type WrapperMSR interface {
	ReadMSR(cpu uint32, msr int64) (uint64, error)
	WriteMSR(cpu uint32, msr int64, value uint64) error
}

type msrProd struct{}

func (m msrProd) ReadMSR(cpu uint32, msr int64) (uint64, error) {
	return msrutils.ReadMSR(cpu, msr)
}

func (m msrProd) WriteMSR(cpu uint32, msr int64, value uint64) error {
	return msrutils.WriteMSR(cpu, msr, value)
}
