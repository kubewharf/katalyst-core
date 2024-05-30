package msr

import (
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
