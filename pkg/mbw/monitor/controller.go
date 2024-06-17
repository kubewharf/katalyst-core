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
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type MB_CONTROL_ACTION int

const (
	BW_LEN                = 11
	MBA_COS_MAX           = 16 // AMD supports up to 16 cos per CCD, while Intel supports up to 16 cos per socket
	L3QOS_BW_CONTROL_BASE = 0xc0000200

	MEMORY_BANDWIDTH_THRESHOLD_PHYSICAL_NUMA = 140000 // fixed threshold 140GBps each physical numa node
	MEMORY_BANDWIDTH_INCREASE_STEP           = 10000  // the incremental rate each step
	MEMORY_BANDWIDTH_DECREASE_STEP           = 5000   // the decremental rate each step
	MEMORY_BANDWIDTH_INCREASE_MIN            = 256    // the minimum incremental value
	MEMORY_BANDWIDTH_DECREASE_MIN            = 512    // the minimum decremental value

	MEMORY_BANDWIDTH_PHYSICAL_NUMA_SWEETPOINT      = 0.95
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_PAINPOINT       = 1.05
	MEMORY_BANDWIDTH_PHYSICAL_NUMA_UNTHROTTLEPOINT = 0.85

	INSTANCE_HIGH_PRIORITY = 1
	INSTANCE_LOW_PRIORITY  = 2

	MEMORY_BANDWIDTH_CONTROL_RAISE      MB_CONTROL_ACTION = 1
	MEMORY_BANDWIDTH_CONTROL_REDUCE     MB_CONTROL_ACTION = 2
	MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE MB_CONTROL_ACTION = 3
)

func checkMSR(core uint32, msr int64, target uint64) error {
	ret, err := msrSingleton.ReadMSR(core, msr)
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
	if wErr := msrSingleton.WriteMSR(core, msr, val); wErr != nil {
		general.Errorf("failed to write %d to msr %d on core %d - %v", val, msr, core, wErr)
		return wErr
	}

	if cErr := checkMSR(core, msr, val); cErr != nil {
		general.Errorf("failed to bind pqr assoc - %v", cErr)
		return cErr
	}

	return nil
}

func bindCorePQRAssoc(core uint32, rmid, cos uint64) error {
	rmidBase := rmid & 0b1111111111
	cosBase := cos & 0b11111111111111111111111111111111
	target := (cosBase << 32) | rmidBase
	return writeMBAMSR(core, rdt.PQR_ASSOC, target)
}

// set the mba cos on a target die (i.e. a AMD CCD)
func (m MBMonitor) ConfigCCDMBACos(ccd, cos, ul int, max uint64) error {
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
	// use the first core on each CCD when throttling the CCD as a whole
	core := m.KatalystMachineInfo.CCDMap[ccd][0]
	if err := writeMBAMSR(uint32(core), int64(bwControl), targetVal); err != nil {
		general.Errorf("failed to set core mba cos on core %d - %v", core, err)
		return err
	}

	return nil
}

func (m MBMonitor) ResetMBACos() error {
	for i := 0; i < m.MachineInfo.NumCores; i++ {
		bindCorePQRAssoc(uint32(i), 0, 0)
	}

	var err error = nil
	for ccd := 0; ccd < m.NumCCDs; ccd++ {
		for cos := 0; cos < MBA_COS_MAX; cos++ {
			// use the first core on each CCD when throttling the CCD as a whole
			if err = m.ConfigCCDMBACos(ccd, cos, 1, 0); err != nil {
				return err
			}
		}
	}

	general.Infof("reset MBA cos on all cores")
	return err
}
