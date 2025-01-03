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

package manager

import (
	"fmt"
	"unsafe"

	"github.com/kubewharf/katalyst-core/pkg/util/bitmask"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func GetMemPolicy(addr unsafe.Pointer, flags int) (mode int, nodemask bitmask.BitMask, err error) {
	return 0, bitmask.NewEmptyBitMask(), fmt.Errorf("GetMemPolicy is unsupported in this build")
}

func SetMemPolicy(mode int, nodemask bitmask.BitMask) (err error) {
	return fmt.Errorf("SetMemPolicy is unsupported in this build")
}

func doReclaimMemory(cmd string, mems machine.CPUSet) error {
	return fmt.Errorf("doReclaimMemory is unsupported in this build")
}
