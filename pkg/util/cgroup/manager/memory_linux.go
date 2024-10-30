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
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func GetMemPolicy(addr unsafe.Pointer, flags int) (mode int, nodemask bitmask.BitMask, err error) {
	var mask uint64
	_, _, errno := syscall.Syscall6(syscall.SYS_GET_MEMPOLICY,
		uintptr(unsafe.Pointer(&mode)), uintptr(unsafe.Pointer(&mask)), 64, uintptr(addr), uintptr(flags), 0)
	if errno != 0 {
		err = errno
	}
	nodemask = bitmask.NewEmptyBitMask()
	bit := 0
	for mask != 0 {
		if mask&1 == 1 {
			nodemask.Add(bit)
		}
		mask >>= 1
		bit++
	}
	return
}

func SetMemPolicy(mode int, nodemask bitmask.BitMask) (err error) {
	var mask uint64
	for _, bit := range nodemask.GetBits() {
		mask |= 1 << uint64(bit)
	}
	_, _, errno := syscall.Syscall(syscall.SYS_SET_MEMPOLICY, uintptr(mode), uintptr(unsafe.Pointer(&mask)), 64)
	if errno != 0 {
		err = errno
	}
	return
}

func doReclaimMemory(cmd string, mems machine.CPUSet) error {
	// When memory is reclaimed by calling memory.reclaim interface and offloaded to zram,
	// kernel will allocate additional zram memory at the NUMAs allowed for the caller process to store the compressed memory.
	// However, the allowed NUMA nodes of katalyst and business processes may not be consistent,
	// which will lead to the allocation of zram memory on NUMAs that are not affinity to the business processes,
	// bringing unpredictable problems. Therefore, before reclaiming business memory,
	// we have to temporarily set the allowed NUMAs of katalyst same as business workload, and restore it after reclaiming.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// save original memory policy
	mode, mask, err := GetMemPolicy(nil, 0)
	if err != nil {
		return err
	}

	newMask := bitmask.NewEmptyBitMask()
	newMask.Add(mems.ToSliceInt()...)

	if err := SetMemPolicy(MPOL_BIND, newMask); err != nil {
		return err
	}

	_, err = exec.Command("bash", "-c", cmd).Output()
	klog.ErrorS(err, "failed to exec %v", cmd)

	// restore original memory policy
	if err := SetMemPolicy(mode, mask); err != nil {
		return err
	}
	return nil
}
