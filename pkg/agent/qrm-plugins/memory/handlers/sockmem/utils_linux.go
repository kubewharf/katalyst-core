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

package sockmem

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func alignToPageSize(number int64) int64 {
	pageSize := int64(syscall.Getpagesize())
	alignedNumber := (number + pageSize - 1) &^ (pageSize - 1)
	return alignedNumber
}

func getHostTCPMemFile(TCPMemFile string) ([]uint64, error) {
	data, err := os.ReadFile(TCPMemFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s, err %v", TCPMemFile, err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty content in %s", TCPMemFile)
	}

	line := lines[0]
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return nil, fmt.Errorf("unexpected number of fields in %s", TCPMemFile)
	}

	var tcpMem []uint64
	for _, field := range fields {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return nil, err
		}
		tcpMem = append(tcpMem, value)
	}

	return tcpMem, nil
}

func setHostTCPMemFile(TCPMemFile string, tcpMem []uint64) error {
	if len(tcpMem) != 3 {
		return fmt.Errorf("tcpMem array must have exactly three elements")
	}

	_, err := os.Stat(TCPMemFile)
	if err != nil {
		return err
	}

	content := fmt.Sprintf("%d\t%d\t%d\n", tcpMem[0], tcpMem[1], tcpMem[2])
	err = os.WriteFile(TCPMemFile, []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("failed to write to %s, err %v", TCPMemFile, err)
	}

	return nil
}
