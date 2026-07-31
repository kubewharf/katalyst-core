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

package planner

import (
	"fmt"
	"sort"
)

// StableAdviceDomainSummary captures the lightweight stable-domain contract
// carried by SysAdvisor advice and validated by QRM before applying it.
type StableAdviceDomainSummary struct {
	perNUMAFloor        map[int]int
	perPoolBudget       map[string]int
	packageDomainDigest uint64
	blockGraphDigest    uint64
	overlapModeDigest   uint64
}

// LocalDomainSummary captures the same contract recomputed locally by QRM.
type LocalDomainSummary StableAdviceDomainSummary

func NewStableAdviceDomainSummary(
	perNUMAFloor map[int]int,
	perPoolBudget map[string]int,
	packageDomainDigest uint64,
	blockGraphDigest uint64,
	overlapModeDigest uint64,
) StableAdviceDomainSummary {
	return StableAdviceDomainSummary{
		perNUMAFloor:        cloneIntIntMap(perNUMAFloor),
		perPoolBudget:       cloneStringIntMap(perPoolBudget),
		packageDomainDigest: packageDomainDigest,
		blockGraphDigest:    blockGraphDigest,
		overlapModeDigest:   overlapModeDigest,
	}
}

func NewLocalDomainSummary(
	perNUMAFloor map[int]int,
	perPoolBudget map[string]int,
	packageDomainDigest uint64,
	blockGraphDigest uint64,
	overlapModeDigest uint64,
) LocalDomainSummary {
	return LocalDomainSummary(NewStableAdviceDomainSummary(
		perNUMAFloor,
		perPoolBudget,
		packageDomainDigest,
		blockGraphDigest,
		overlapModeDigest,
	))
}

// Clone returns an independent summary with cloned map fields.
func (s StableAdviceDomainSummary) Clone() StableAdviceDomainSummary {
	return StableAdviceDomainSummary{
		perNUMAFloor:        cloneIntIntMap(s.perNUMAFloor),
		perPoolBudget:       cloneStringIntMap(s.perPoolBudget),
		packageDomainDigest: s.packageDomainDigest,
		blockGraphDigest:    s.blockGraphDigest,
		overlapModeDigest:   s.overlapModeDigest,
	}
}

// PerNUMAFloor returns a cloned per-NUMA floor map.
func (s StableAdviceDomainSummary) PerNUMAFloor() map[int]int {
	return cloneIntIntMap(s.perNUMAFloor)
}

// PerPoolBudget returns a cloned per-pool budget map.
func (s StableAdviceDomainSummary) PerPoolBudget() map[string]int {
	return cloneStringIntMap(s.perPoolBudget)
}

func (s StableAdviceDomainSummary) PackageDomainDigest() uint64 {
	return s.packageDomainDigest
}

func (s StableAdviceDomainSummary) BlockGraphDigest() uint64 {
	return s.blockGraphDigest
}

func (s StableAdviceDomainSummary) OverlapModeDigest() uint64 {
	return s.overlapModeDigest
}

// Clone returns an independent local summary with cloned map fields.
func (l LocalDomainSummary) Clone() LocalDomainSummary {
	return LocalDomainSummary(StableAdviceDomainSummary(l).Clone())
}

// PerNUMAFloor returns a cloned per-NUMA floor map.
func (l LocalDomainSummary) PerNUMAFloor() map[int]int {
	return StableAdviceDomainSummary(l).PerNUMAFloor()
}

// PerPoolBudget returns a cloned per-pool budget map.
func (l LocalDomainSummary) PerPoolBudget() map[string]int {
	return StableAdviceDomainSummary(l).PerPoolBudget()
}

func (l LocalDomainSummary) PackageDomainDigest() uint64 {
	return StableAdviceDomainSummary(l).PackageDomainDigest()
}

func (l LocalDomainSummary) BlockGraphDigest() uint64 {
	return StableAdviceDomainSummary(l).BlockGraphDigest()
}

func (l LocalDomainSummary) OverlapModeDigest() uint64 {
	return StableAdviceDomainSummary(l).OverlapModeDigest()
}

// Validate reports whether the stable advice summary still matches the local
// domain summary recomputed by QRM.
func (s StableAdviceDomainSummary) Validate(local LocalDomainSummary) error {
	localSummary := StableAdviceDomainSummary(local)
	if err := validateIntIntMap("per NUMA floor", "NUMA", s.perNUMAFloor, localSummary.perNUMAFloor); err != nil {
		return err
	}
	if err := validateStringIntMap("per pool budget", "pool", s.perPoolBudget, localSummary.perPoolBudget); err != nil {
		return err
	}
	if s.packageDomainDigest != localSummary.packageDomainDigest {
		return fmt.Errorf("package domain digest mismatch: stable=%d local=%d", s.packageDomainDigest, localSummary.packageDomainDigest)
	}
	if s.blockGraphDigest != localSummary.blockGraphDigest {
		return fmt.Errorf("block graph digest mismatch: stable=%d local=%d", s.blockGraphDigest, localSummary.blockGraphDigest)
	}
	if s.overlapModeDigest != localSummary.overlapModeDigest {
		return fmt.Errorf("overlap mode digest mismatch: stable=%d local=%d", s.overlapModeDigest, localSummary.overlapModeDigest)
	}

	return nil
}

func cloneIntIntMap(src map[int]int) map[int]int {
	if src == nil {
		return nil
	}

	dst := make(map[int]int, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneStringIntMap(src map[string]int) map[string]int {
	if src == nil {
		return nil
	}

	dst := make(map[string]int, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func validateIntIntMap(summaryName, keyName string, stable, local map[int]int) error {
	for _, key := range sortedIntKeys(stable) {
		stableValue := stable[key]
		localValue, ok := local[key]
		if !ok {
			return fmt.Errorf("%s mismatch: missing local %s %d: stable=%d", summaryName, keyName, key, stableValue)
		}
		if stableValue != localValue {
			return fmt.Errorf("%s mismatch for %s %d: stable=%d local=%d", summaryName, keyName, key, stableValue, localValue)
		}
	}

	for _, key := range sortedIntKeys(local) {
		if _, ok := stable[key]; ok {
			continue
		}
		return fmt.Errorf("%s mismatch: unexpected local %s %d: local=%d", summaryName, keyName, key, local[key])
	}

	return nil
}

func validateStringIntMap(summaryName, keyName string, stable, local map[string]int) error {
	for _, key := range sortedStringKeys(stable) {
		stableValue := stable[key]
		localValue, ok := local[key]
		if !ok {
			return fmt.Errorf("%s mismatch: missing local %s %q: stable=%d", summaryName, keyName, key, stableValue)
		}
		if stableValue != localValue {
			return fmt.Errorf("%s mismatch for %s %q: stable=%d local=%d", summaryName, keyName, key, stableValue, localValue)
		}
	}

	for _, key := range sortedStringKeys(local) {
		if _, ok := stable[key]; ok {
			continue
		}
		return fmt.Errorf("%s mismatch: unexpected local %s %q: local=%d", summaryName, keyName, key, local[key])
	}

	return nil
}

func sortedIntKeys(values map[int]int) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Ints(keys)
	return keys
}

func sortedStringKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
