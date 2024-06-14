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
