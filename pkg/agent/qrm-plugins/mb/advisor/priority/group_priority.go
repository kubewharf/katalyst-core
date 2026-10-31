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
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	instance *threadSafeGroupPriority
	once     sync.Once
)

func GetInstance() *threadSafeGroupPriority {
	once.Do(func() {
		instance = &threadSafeGroupPriority{
			majorWeights: make(map[string]int, len(resctrlMajorGroupWeights)),
			exactWeights: make(map[string]int),
		}
		for key, value := range resctrlMajorGroupWeights {
			instance.majorWeights[key] = value
		}
	})
	return instance
}

type threadSafeGroupPriority struct {
	mu           sync.RWMutex
	majorWeights map[string]int
	exactWeights map[string]int
}

// EquivalenceGroupKey is the shared grouping contract used by sorting and
// advisor preprocessing. Physical share subgroups intentionally include their
// complete CLOS ID so equal numeric priorities cannot merge their traffic.
type EquivalenceGroupKey struct {
	Weight         int
	PhysicalCLOSID string
}

func (g *threadSafeGroupPriority) GetWeight(name string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if weight, ok := g.exactWeights[name]; ok {
		return weight
	}

	baseWeight, ok := g.majorWeights[getMajor(name)]
	if !ok {
		return defaultWeight
	}

	return saturatingAddNonNegative(baseWeight, getSubWeight(name))
}

func saturatingAddNonNegative(left, right int) int {
	maxInt := int(^uint(0) >> 1)
	if right > maxInt-left {
		return maxInt
	}
	return left + right
}

func (g *threadSafeGroupPriority) GetEquivalenceGroupKey(name string) EquivalenceGroupKey {
	key := EquivalenceGroupKey{Weight: g.GetWeight(name)}
	if isPhysicalShareSubgroup(name) {
		key.PhysicalCLOSID = name
	}
	return key
}

func (g *threadSafeGroupPriority) SortGroups(groups []string) []sets.String {
	sort.Slice(groups, func(i, j int) bool {
		leftKey := g.GetEquivalenceGroupKey(groups[i])
		rightKey := g.GetEquivalenceGroupKey(groups[j])
		if leftKey.Weight != rightKey.Weight {
			return leftKey.Weight > rightKey.Weight
		}
		if leftKey.PhysicalCLOSID != rightKey.PhysicalCLOSID {
			return leftKey.PhysicalCLOSID < rightKey.PhysicalCLOSID
		}
		return groups[i] < groups[j]
	})

	return g.mergeGroupsByEquivalenceKey(groups)
}

func (g *threadSafeGroupPriority) mergeGroupsByEquivalenceKey(groups []string) []sets.String {
	var mergedGroups []sets.String
	var lastKey EquivalenceGroupKey
	for _, group := range groups {
		key := g.GetEquivalenceGroupKey(group)
		if len(mergedGroups) == 0 {
			mergedGroups = append(mergedGroups, sets.NewString(group))
			lastKey = key
			continue
		}

		if key == lastKey {
			mergedGroups[len(mergedGroups)-1].Insert(group)
			continue
		}

		mergedGroups = append(mergedGroups, sets.NewString(group))
		lastKey = key
	}
	return mergedGroups
}

func (g *threadSafeGroupPriority) AddWeight(name string, weight int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.exactWeights[name] = weight
}
