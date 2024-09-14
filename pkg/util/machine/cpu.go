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

package machine

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/cpuid/v2"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const cpuInfoPath = "/proc/cpuinfo"

var (
	avx2RegExp   = regexp.MustCompile(`^flags\s*:.* (avx2 .*|avx2)$`)
	avx512RegExp = regexp.MustCompile(`^flags\s*:.* (avx512 .*|avx512)$`)

	smtActive = false
	checkOnce = sync.Once{}
)

type CPU_VENDOR_NAME string

const (
	CPU_VENDOR_INTEL   CPU_VENDOR_NAME = "Intel"
	CPU_VENDOR_AMD     CPU_VENDOR_NAME = "AMD"
	CPU_VENDOR_ARM     CPU_VENDOR_NAME = "ARM"
	CPU_VENDOR_UNKNOWN CPU_VENDOR_NAME = "UNKNOWN"
)

type ExtraCPUInfo struct {
	// SupportInstructionSet instructions all cpus support.
	SupportInstructionSet sets.String

	// below are CPU related info detected by cpuid package which is dependency of mbw lib
	// may consider to unify with cadvisor detected info
	Vendor CPU_VENDOR_NAME
	Family int
	Model  int
}

// GetExtraCPUInfo get extend cpu info from proc system
func GetExtraCPUInfo() (*ExtraCPUInfo, error) {
	cpuInfo, err := os.ReadFile(cpuInfoPath)
	if err != nil {
		return nil, errors.Wrapf(err, "could not read file %s", cpuInfoPath)
	}

	return &ExtraCPUInfo{
		SupportInstructionSet: getCPUInstructionInfo(string(cpuInfo)),
		Vendor:                CPU_VENDOR_NAME(cpuid.CPU.VendorID.String()),
		Family:                cpuid.CPU.Family,
		Model:                 cpuid.CPU.Model,
	}, nil
}

// getCPUInstructionInfo get cpu instruction info by parsing flags with "avx2", "avx512".
func getCPUInstructionInfo(cpuInfo string) sets.String {
	supportInstructionSet := make(sets.String)

	for _, line := range strings.Split(cpuInfo, "\n") {
		if line == "" {
			continue
		}

		// whether flags match avx2, if matched we think this
		// machine is support avx2 instruction
		if avx2RegExp.MatchString(line) {
			supportInstructionSet.Insert("avx2")
		}

		// whether flags match avx2, if matched we think this
		// machine is support avx512 instruction
		if avx512RegExp.MatchString(line) {
			supportInstructionSet.Insert("avx512")
		}
	}

	return supportInstructionSet
}

// GetCoreNumReservedForReclaim generates per numa reserved for reclaim resource value map.
// per numa reserved resource is taken in a fair way with even step, e.g.
// 4 -> 1 1 1 1; 2 -> 1 1 1 1; 8 -> 2 2 2 2;
func GetCoreNumReservedForReclaim(numReservedCores, numNumaNodes int) map[int]int {
	if numNumaNodes <= 0 {
		numNumaNodes = 1
	}

	if numReservedCores < numNumaNodes {
		numReservedCores = numNumaNodes
	}

	reservedPerNuma := numReservedCores / numNumaNodes
	step := numNumaNodes / numReservedCores

	if reservedPerNuma < 1 {
		reservedPerNuma = 1
	}
	if step < 1 {
		step = 1
	}

	reservedForReclaim := make(map[int]int)
	for id := 0; id < numNumaNodes; id++ {
		if id%step == 0 {
			reservedForReclaim[id] = reservedPerNuma
		} else {
			reservedForReclaim[id] = 0
		}
	}

	return reservedForReclaim
}

func SmtActive() bool {
	checkOnce.Do(func() {
		data, err := ioutil.ReadFile("/sys/devices/system/cpu/smt/active")
		if err != nil {
			klog.ErrorS(err, "failed to check SmtActive")
			return
		}
		active, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			klog.ErrorS(err, "failed to parse smt active file")
			return
		}
		klog.Infof("smt active %v", active)
		smtActive = active == 1
	})
	return smtActive
}
