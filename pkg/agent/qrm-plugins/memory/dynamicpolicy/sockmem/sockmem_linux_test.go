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
	"io/ioutil"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetLimitFromTCPMemFile(t *testing.T) {
	// case1, normal input/outout
	tmpFile, err := ioutil.TempFile("", "tcp_mem")
	if err != nil {
		t.Fatalf("Error creating temporary file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	testData := []byte("187365\t249822\t999999\n")
	_, err = tmpFile.Write(testData)
	if err != nil {
		t.Fatalf("Error writing test data to temporary file: %v", err)
	}
	tmpFile.Close()

	tcpMem, err := getHostTCPMemFile(tmpFile.Name())
	if err != nil {
		t.Errorf("Expected no error, but got error: %v", err)
	}
	expectedUpperLimit := uint64(999999)
	if expectedUpperLimit != tcpMem[2] {
		t.Errorf("Expected upper limit to be %d, but got %d", expectedUpperLimit, tcpMem[2])
	}

	expectedUpperLimit = uint64(249822)
	if expectedUpperLimit != tcpMem[1] {
		t.Errorf("Expected pressure limit to be %d, but got %d", expectedUpperLimit, tcpMem[1])
	}

	// case2, null file
	_, err = getHostTCPMemFile("nullfile")
	if err == nil {
		t.Error("Expected an error, but got none")
	}

	// case3, file with invalid data
	testData = []byte("invalid_data\n")
	err = ioutil.WriteFile(tmpFile.Name(), testData, 0644)
	if err != nil {
		t.Fatalf("Error writing invalid test data to temporary file: %v", err)
	}
	_, err = getHostTCPMemFile(tmpFile.Name())
	if err == nil {
		t.Error("Expected an error, but got none")
	}
}

func TestSetLimitToTCPMemFile(t *testing.T) {
	// case1, normal logic
	tmpFile, err := ioutil.TempFile("", "tcp_mem")
	if err != nil {
		t.Fatalf("Error creating temporary file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	testData := []byte("187365\t249822\t999999\n")
	_, err = tmpFile.Write(testData)
	if err != nil {
		t.Fatalf("Error writing test data to temporary file: %v", err)
	}
	tmpFile.Close()

	tcpMem := []uint64{1, 2, 123456}
	setHostTCPMemFile(tmpFile.Name(), tcpMem)
	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Error reading modified file: %v", err)
	}
	newValues := strings.Fields(string(data))
	if len(newValues) < 3 {
		t.Errorf("Expected at least 3 values in the file, but got %d", len(newValues))
	}
	newUpper, _ := strconv.Atoi(newValues[2])
	if newUpper != 123456 {
		t.Errorf("Expected upper limit to be 123456, but got %d", newUpper)
	}

	// case2, null file
	err = setHostTCPMemFile("nonexistentfile", tcpMem)
	if err == nil {
		t.Error("Expected an error, but got none")
	} else if !os.IsNotExist(err) {
		t.Errorf("Expected 'file not found' error, but got: %v", err)
	}
	// case3, file with invalid data
	wrongTcpMem := []uint64{1, 2, 3, 4}
	err = setHostTCPMemFile(tmpFile.Name(), wrongTcpMem)
	if err == nil {
		t.Error("tcp_mem is wrong, need return err")
	}
}

func TestAlignToPageSize(t *testing.T) {
	pageSize := int64(syscall.Getpagesize())

	// Test case 1: Number already aligned to page size
	result := alignToPageSize(pageSize * 2)
	assert.Equal(t, pageSize*2, result, "Unexpected result for aligned number")

	// Test case 2: Number smaller than page size
	result = alignToPageSize(pageSize - 1)
	assert.Equal(t, pageSize, result, "Unexpected result for number smaller than page size")
}

func TestUpdateHostTCPMemRatio(t *testing.T) {
	// Test case 1: Valid ratio within the range
	UpdateHostTCPMemRatio(30)
	assert.Equal(t, 30, sockMemConfig.hostTCPMemRatio, "Unexpected hostTCPMemRatio value")

	// Test case 2: Ratio below the minimum
	UpdateHostTCPMemRatio(hostTCPMemRatioMin - 1)
	assert.Equal(t, hostTCPMemRatioMin, sockMemConfig.hostTCPMemRatio, "Unexpected hostTCPMemRatio value")

	// Test case 3: Ratio above the maximum
	UpdateHostTCPMemRatio(hostTCPMemRatioMax + 1)
	assert.Equal(t, hostTCPMemRatioMax, sockMemConfig.hostTCPMemRatio, "Unexpected hostTCPMemRatio value")
}

func TestUpdateCgroupTCPMemRatio(t *testing.T) {
	// Test case 1: Valid ratio within the range
	UpdateCgroupTCPMemRatio(50)
	assert.Equal(t, 50, sockMemConfig.cgroupTCPMemRatio, "Unexpected cgroupTCPMemRatio value")

	// Test case 2: Ratio below the minimum
	UpdateCgroupTCPMemRatio(cgroupTCPMemRatioMin - 1)
	assert.Equal(t, cgroupTCPMemRatioMin, sockMemConfig.cgroupTCPMemRatio, "Unexpected cgroupTCPMemRatio value")

	// Test case 3: Ratio above the maximum
	UpdateCgroupTCPMemRatio(cgroupTCPMemRatioMax + 1)
	assert.Equal(t, cgroupTCPMemRatioMax, sockMemConfig.cgroupTCPMemRatio, "Unexpected cgroupTCPMemRatio value")
}

func TestSetCg1TCPMem(t *testing.T) {
	podUID := "pod123"
	containerID := "container456"
	memLimit := int64(1024)
	memTCPLimit := int64(512)
	err := SetCg1TCPMem(podUID, containerID, memLimit, memTCPLimit)
	if err == nil {
		t.Error("Expected an error, but got none")
	}
}
