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
	"fmt"
	"sort"
	"sync"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/advisor/resource"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

var (
	instance *threadSafeGroupPriority
	once     sync.Once
)

func GetInstance() *threadSafeGroupPriority {
	once.Do(func() {
		instance = &threadSafeGroupPriority{
			weights: make(map[string]int, len(resctrlMajorGroupWeights)),
		}
		for key, value := range resctrlMajorGroupWeights {
			instance.weights[key] = value
		}
	})
	return instance
}

type threadSafeGroupPriority struct {
	mu      sync.RWMutex
	weights map[string]int
}

func (g *threadSafeGroupPriority) GetWeight(name string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	baseWeight, ok := g.weights[getMajor(name)]
	if !ok {
		return defaultWeight
	}

	return baseWeight + getSubWeight(name)
}

func (g *threadSafeGroupPriority) SortGroups(groups []string) []sets.String {
	sort.Slice(groups, func(i, j int) bool {
		return g.GetWeight(groups[i]) > g.GetWeight(groups[j])
	})

	return g.mergeGroupsByWeight(groups)
}

func (g *threadSafeGroupPriority) mergeGroupsByWeight(groups []string) []sets.String {
	var mergedGroups []sets.String
	for _, group := range groups {
		if len(mergedGroups) == 0 {
			mergedGroups = append(mergedGroups, sets.NewString(group))
			continue
		}

		lastGroup := mergedGroups[len(mergedGroups)-1]
		weightLastGroup, err := g.getWeightOfEquivGroup(lastGroup)
		if err != nil {
			general.Warningf("[mbm] failed to get allocation weight of group %v: %v", lastGroup, err)
			continue
		}

		if g.GetWeight(group) == weightLastGroup {
			lastGroup.Insert(group)
			continue
		}

		mergedGroups = append(mergedGroups, sets.NewString(group))
	}
	return mergedGroups
}

func (g *threadSafeGroupPriority) getWeightOfEquivGroup(equivGroups sets.String) (int, error) {
	repGroup, err := resource.GetGroupRepresentative(equivGroups)
	if err != nil {
		return 0, errors.Wrap(err, fmt.Sprintf("failed to get representative of groups %v", equivGroups))
	}

	return g.GetWeight(repGroup), nil
}

func (g *threadSafeGroupPriority) AddWeight(name string, weight int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.weights[name] = weight
}
