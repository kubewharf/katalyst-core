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
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const defaultWeight = 5_000

var resctrlMajorGroupWeights = map[string]int{
	consts.ResctrlGroupRoot:      10_000, // intrinsic root the highest
	consts.ResctrlGroupDedicated: 9_000,
	consts.ResctrlGroupSystem:    9_000,
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

func getWeight(name string) int {
	baseWeight, ok := resctrlMajorGroupWeights[getMajor(name)]
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
		weightLastGroup, err := getWeightOfEquivGroup(lastGroup)
		if err != nil {
			general.Warningf("[mbm] failed to get allocation weight of group %v: %v", lastGroup, err)
			continue
		}

		if getWeight(group) == weightLastGroup {
			lastGroup.Insert(group)
			continue
		}

		mergedGroups = append(mergedGroups, sets.NewString(group))
	}
	return mergedGroups
}

func getWeightOfEquivGroup(equivGroups sets.String) (int, error) {
	repGroup, err := resource.GetGroupRepresentative(equivGroups)
	if err != nil {
		return 0, errors.Wrap(err, fmt.Sprintf("failed to get representative of groups %v", equivGroups))
	}

	return getWeight(repGroup), nil
}
