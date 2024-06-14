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

//go:build amd64 && !gccgo && !noasm && !appengine
// +build amd64,!gccgo,!noasm,!appengine

package rdt

import (
	"fmt"
	"membw_manager/pkg/utils/msr"
)

type PQOS_EVENT_TYPE uint64

const (
	IA32_QM_EVTSEL int64 = 0xC8D
	IA32_QM_CTR    int64 = 0xC8E
	PQR_ASSOC      int64 = 0xC8F

	PQOS_MON_EVENT_L3_OCCUP PQOS_EVENT_TYPE = 1
	PQOS_MON_EVENT_TMEM_BW  PQOS_EVENT_TYPE = 2
	PQOS_MON_EVENT_LMEM_BW  PQOS_EVENT_TYPE = 3
	MBM_MAX_VALUE           uint64          = (1 << 24)
)

func asmCpuidex(op, op2 uint32) (eax, ebx, ecx, edx uint32) // implemented in asm_amd64.s

func GetRDTScalar() uint32 {
	var eax, ecx uint32 = 0xf, 0x1
	_, ebx, _, _ := asmCpuidex(eax, ecx)
	// x86 CPU puts rdt_scalar in ebx register after we call cpuid instruction

	return ebx
}

func GetRDTValue(core uint32, event PQOS_EVENT_TYPE) (uint64, error) {
	var rmid, msr_value uint64 = 0, 0
	var err error

	msr_value, err = msr.ReadMSR(core, PQR_ASSOC)
	if err != nil {
		return msr_value, fmt.Errorf("faild to read msr %v", err.Error())
	}

	rmid = (msr_value & 0x00000000000003ff)
	msr_value = (rmid << 32) + uint64(event)
	err = msr.WriteMSR(core, IA32_QM_EVTSEL, msr_value)
	if err != nil {
		return msr_value, fmt.Errorf("faild to write msr %v", err.Error())
	}

	msr_value, err = msr.ReadMSR(core, IA32_QM_CTR)
	if err != nil {
		return msr_value, fmt.Errorf("faild to read msr %v", err.Error())
	}

	if (msr_value & 0x8000000000000000) != 0 {
		msr_value = 0xffffffff
		return msr_value, fmt.Errorf("unsupport rmid %d detected on core %d, rmid may be changed", rmid, core)
	}

	if (msr_value & 0x4000000000000000) != 0 {
		msr_value = 0xffffffff
		return msr_value, fmt.Errorf("data for rmid %d not availabled on core %d", rmid, core)
	}

	return msr_value, nil
}
