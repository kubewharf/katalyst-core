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

package advisor

import (
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
)

const defaultWeight = 5_000

var resctrlGroupWeights = map[string]int{
	"/":         10_000, // intrinsic root the highest
	"dedicated": 9_000,
	"system":    9_000,
	"share":     1_050, // identical to shared-50
	"shared":    1_000,
	"reclaim":   100,
}

func getMajor(name string) string {
	parts := strings.Split(name, "-")
	return parts[0]
}

func isSharedGroup(name string) bool {
	return strings.HasPrefix(name, "shared")
}

func extractNumberSplit(s string) (int, bool) {
	parts := strings.Split(s, "-")
	if len(parts) < 2 {
		return 0, false
	}
	num, err := strconv.Atoi(parts[len(parts)-1])
	return num, err == nil
}

func getSubWeight(name string) int {
	if isSharedGroup(name) {
		if subWeight, ok := extractNumberSplit(name); ok {
			return subWeight
		}
	}

	return 0
}

func getWeight(name string) int {
	baseWeight, ok := resctrlGroupWeights[getMajor(name)]
	if !ok {
		return defaultWeight
	}

	return baseWeight + getSubWeight(name)
}

// sortGroups sorts groups by their weights in desc order;
// groups having identical weight put in one set
func sortGroups(groups []string) []sets.String {
	sort.Slice(groups, func(i, j int) bool {
		return getWeight(groups[i]) > getWeight(groups[j])
	})

	return mergeGroupsByWeight(groups)
}

func mergeGroupsByWeight(groups []string) []sets.String {
	var mergedGroups []sets.String
	for _, group := range groups {
		if len(mergedGroups) == 0 {
			mergedGroups = append(mergedGroups, sets.NewString(group))
			continue
		}

		lastGroup := mergedGroups[len(mergedGroups)-1]
		lastGroupRep, err := resource.GetGroupRepresentative(lastGroup)
		if err == nil && getWeight(group) == getWeight(lastGroupRep) {
			lastGroup.Insert(group)
			continue
		}

		mergedGroups = append(mergedGroups, sets.NewString(group))
	}
	return mergedGroups
}
