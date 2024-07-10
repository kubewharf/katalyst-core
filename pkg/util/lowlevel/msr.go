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

package lowlevel

import (
	"encoding/binary"
	"fmt"
	"syscall"
)

const defaultFmtStr = "/dev/cpu/%d/msr"

// MSRDev represents a handler for frequent read/write operations
// for one-off MSR read/writes, try {Read,Write}MSR*() functions
type MSRDev struct {
	fd int
}

// Close() closes the connection to the MSR
func (d MSRDev) Close() error {
	return syscall.Close(d.fd)
}

// MSR() provides an interface for reoccurring access to a given CPU's MSR
func MSR(cpu uint32) (MSRDev, error) {
	cpuDir := fmt.Sprintf(defaultFmtStr, cpu)
	fd, err := syscall.Open(cpuDir, syscall.O_RDWR, 777)
	if err != nil {
		return MSRDev{}, err
	}
	return MSRDev{fd: fd}, nil
}

// Read() reads a given MSR on the CPU and returns the uint64
func (d MSRDev) Read(msr int64) (uint64, error) {
	regBuf := make([]byte, 8)

	rc, err := syscall.Pread(d.fd, regBuf, msr)

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

// Write() writes a given value to the provided register
func (d MSRDev) Write(regno int64, value uint64) error {

	buf := make([]byte, 8)

	binary.LittleEndian.PutUint64(buf, value)

	count, err := syscall.Pwrite(d.fd, buf, regno)
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
