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
	"io/ioutil"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"
)

const cpuInfoPath = "/proc/cpuinfo"

var (
	avx2RegExp   = regexp.MustCompile(`^flags\s*:.* (avx2 .*|avx2)$`)
	avx512RegExp = regexp.MustCompile(`^flags\s*:.* (avx512 .*|avx512)$`)
)

type ExtraCPUInfo struct {
	// SupportInstructionSet instructions all cpus support.
	SupportInstructionSet sets.String
}

// GetExtraCPUInfo get extend cpu info from proc system
func GetExtraCPUInfo() (*ExtraCPUInfo, error) {
	cpuInfo, err := ioutil.ReadFile(cpuInfoPath)
	if err != nil {
		return nil, errors.Wrapf(err, "could not read file %s", cpuInfoPath)
	}

	return &ExtraCPUInfo{
		SupportInstructionSet: getCPUInstructionInfo(string(cpuInfo)),
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
// 4 -> 1 1 1 1; 2 -> 1 0 1 0
func GetCoreNumReservedForReclaim(numReservedCores, numNumaNodes int) map[int]int {
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
