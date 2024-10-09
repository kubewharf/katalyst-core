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

package amd

import (
	"fmt"

	utils "github.com/kubewharf/katalyst-core/pkg/util/lowlevel"
)

const (
	BW_LEN                      = 11
	MBA_COS_MAX                 = 16 // AMD supports up to 16 cos per CCD, while Intel supports up to 16 cos per socket
	L3QOS_BW_CONTROL_BASE       = 0xc0000200
	PQR_ASSOC             int64 = 0xC8F
)

func checkMSR(core uint32, msr int64, target uint64) error {
	ret, err := utils.ReadMSR(core, msr)
	if err != nil {
		return fmt.Errorf("failed to read msr - %v", err)
	}

	if ret != target {
		return fmt.Errorf("failed to set msr %d on core %d to the expected value %d from %d",
			msr, core, target, ret)
	}

	return nil
}

func writeMBAMSR(core uint32, msr int64, val uint64) error {
	if wErr := utils.WriteMSR(core, msr, val); wErr != nil {
		fmt.Printf("failed to write %d to msr %d on core %d - %v", val, msr, core, wErr)
		return wErr
	}

	if cErr := checkMSR(core, msr, val); cErr != nil {
		fmt.Printf("failed to bind pqr assoc - %v", cErr)
		return cErr
	}

	return nil
}

func bindCorePQRAssoc(core uint32, rmid, cos uint64) error {
	rmidBase := rmid & 0b1111111111
	cosBase := cos & 0b11111111111111111111111111111111
	target := (cosBase << 32) | rmidBase
	return writeMBAMSR(core, PQR_ASSOC, target)
}

// bind a set of cores to a qos doamin (represented by a cos) by setting each core's pqr_assoc register to the cos value
// when throtting these cores, just set any one's bwControl register to update the cos
func BindCoresToCos(cos int, rmid int, cores ...int) error {
	for _, core := range cores {
		// assign a unique rmid to each core
		// note: we can also bind all cores to the same rmid, then
		// each core's MB collected by rdt represents the whole cos domain (i.e. a CCD)
		if err := bindCorePQRAssoc(uint32(core), uint64(rmid), uint64(cos)); err != nil {
			return fmt.Errorf("faild to bind core %d to cos %d - %v", core, cos, err)
		}
	}

	return nil
}

func Assign_Uniq_RMID(core uint32, rmid uint64) error {
	assoc, err := utils.ReadMSR(core, PQR_ASSOC)
	if err != nil {
		return fmt.Errorf("failed to assign rmid to core %d - faild to read msr %v", core, err.Error())
	}

	assoc = (assoc & 0xffffffff00000000) + rmid

	err = utils.WriteMSR(core, PQR_ASSOC, assoc)
	if err != nil {
		return fmt.Errorf("failed to assign rmid to core %d - faild to write msr %v", core, err.Error())
	}

	utils.WriteMSR(core, 0xc0000400, 0x3f)
	utils.WriteMSR(core, 0xc0000401, 0x15)

	return nil
}

func ReadCCDMBA(core, cos int) (uint64, error) {
	bwControl := L3QOS_BW_CONTROL_BASE + cos
	maskA := uint64((1 << BW_LEN) - 1)

	mba, err := utils.ReadMSR(uint32(core), int64(bwControl))
	if err != nil {
		fmt.Printf("failed to read core mba cos on core %d - %v", core, err)
		return 0, err
	}

	mba = (mba & maskA) * 1000 / 8

	return mba, nil
}

// set the mba cos on a target die (i.e. a AMD CCD)
// use the first core on each CCD when throttling the CCD as a whole
func ConfigCCDMBACos(core, cos, ul int, max uint64) error {
	bwControl := L3QOS_BW_CONTROL_BASE + cos
	maskA := uint64((1 << BW_LEN) - 1)
	a := max * 8 / 1000 // bandwidth is expressed in 1/8GBps increments
	a1 := a & maskA
	b1 := ul & 1

	if a >= maskA {
		a1 = 0
		b1 = 1
	}

	shiftedB := b1 << BW_LEN
	targetVal := a1 | uint64(shiftedB)
	if err := writeMBAMSR(uint32(core), int64(bwControl), targetVal); err != nil {
		fmt.Printf("failed to set core mba cos on core %d - %v", core, err)
		return err
	}

	return nil
}
