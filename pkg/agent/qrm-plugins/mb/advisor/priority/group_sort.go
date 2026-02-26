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

package priority

import (
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

const defaultWeight = 5_000

// resctrlMajorGroupWeights is initialized with built-in resctrl groups and their priorities
var resctrlMajorGroupWeights = map[string]int{
	consts.ResctrlGroupRoot:      10_000, // intrinsic root the highest
	consts.ResctrlGroupDedicated: 9_000,
	consts.ResctrlGroupSystem:    8_000,
	consts.ResctrlGroupShare:     1_000, // there are 2 forms: "share-xx", and "share" which equals to "share-50"
	consts.ResctrlGroupReclaim:   100,
}

func getMajor(name string) string {
	parts := strings.Split(name, consts.ResctrlSubgroupSeparator)
	return parts[0]
}

func hasShareSubgroup(name string) bool {
	return strings.HasPrefix(name, consts.ResctrlShareSubgroupPrefix)
}

func extractNumberSplit(s string) (int, bool) {
	parts := strings.Split(s, consts.ResctrlSubgroupSeparator)
	if len(parts) < 2 {
		return 0, false
	}
	num, err := strconv.Atoi(parts[len(parts)-1])
	return num, err == nil
}

func getSubWeight(name string) int {
	if hasShareSubgroup(name) {
		if subWeight, ok := extractNumberSplit(name); ok {
			return subWeight
		}
	}

	// "share" is "share-50"
	if name == consts.ResctrlGroupShare {
		return 50
	}

	return 0
}
