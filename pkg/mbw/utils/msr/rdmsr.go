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

// Read() reads a given MSR on the CPU and returns the uint64
func (d MSRDev) Read(msr int64) (uint64, error) {
	regBuf := make([]byte, 8)
	rc, err := utils.AppSyscall.Pread(d.fd, regBuf, msr)
	if err != nil {
		return 0, err
	}

	if rc != 8 {
		return 0, fmt.Errorf("Read wrong count of bytes: %d", rc)
	}

	// Suppose that an x86 processor will be little endian
	msrOut := binary.LittleEndian.Uint64(regBuf)

	return msrOut, nil
}

// ReadMSR() reads the MSR on the given CPU as a one-time operation
func ReadMSR(cpu uint32, msr int64) (uint64, error) {
	m, err := MSR(cpu)
	if err != nil {
		return 0, err
	}

	defer m.Close()

	msrD, err := m.Read(msr)
	if err != nil {
		return 0, err
	}

	return msrD, nil
}
