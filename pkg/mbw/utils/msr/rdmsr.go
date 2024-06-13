package msr

import (
	"encoding/binary"
	"fmt"
	"syscall"
)

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
