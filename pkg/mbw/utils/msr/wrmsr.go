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

package msr

import (
	"encoding/binary"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/mbw/utils"
)

// Write() writes a given value to the provided register
func (d MSRDev) Write(regno int64, value uint64) error {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, value)
	count, err := utils.AppSyscall.Pwrite(d.fd, buf, regno)
	if err != nil {
		return err
	}
	if count != 8 {
		return fmt.Errorf("Write count not a uint64: %d", count)
	}

	return nil
}

// WriteMSR() writes the MSR on the given CPU as a one-time operation
func WriteMSR(cpu uint32, msr int64, value uint64) error {
	m, err := MSR(cpu)
	if err != nil {
		return err
	}

	defer m.Close()

	err = m.Write(msr, value)
	if err != nil {
		return err
	}

	return nil
}
