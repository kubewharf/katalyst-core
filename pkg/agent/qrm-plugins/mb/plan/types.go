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

package plan

import (
	"fmt"
	"sort"
	"strings"
)

const mbUnitAMD = 1_000 / 8 // AMD schemata value in unit of 1/8 GB

// GroupCCDPlan is for single control group, with
// key as CCD id, value as memory bandwidth quota in MegaBytes
type GroupCCDPlan map[int]int

// MBPlan is the memory bandwidth allocation plan
type MBPlan struct {
	MBGroups map[string]GroupCCDPlan
}

func getSortedKeys(c GroupCCDPlan) []int {
	keys := make([]int, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func (c GroupCCDPlan) ToSchemataInstruction() []byte {
	// the result looks like  "MB:2=32;3=32;"
	var sb strings.Builder
	sb.WriteString("MB:")

	ccds := getSortedKeys(c)
	for _, ccd := range ccds {
		mb := c[ccd]
		v := (mb + mbUnitAMD - 1) / mbUnitAMD
		sb.WriteString(fmt.Sprintf("%d=%d;", ccd, v))
	}

	// LF is critical for schemata update
	sb.WriteString("\n")
	return []byte(sb.String())
}
