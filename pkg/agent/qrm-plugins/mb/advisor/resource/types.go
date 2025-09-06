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

package resource

import "k8s.io/apimachinery/pkg/util/sets"

type State string

const (
	ResourceStressful State = "underStress"
	ResourceAbundant  State = "abundance"
	ResourceFit       State = "fit"
)

type GroupSettings map[string]int

// DomQuotas represents the quotas for domains
type DomQuotas map[int]GroupSettings

// GetGroupedDomainSetting transforms group-v by domain into domain-v by group
// as the latter is mapped to resctrl FS group layout
func (d DomQuotas) GetGroupedDomainSetting() map[string][]int {
	numDomain := len(d)
	result := make(map[string][]int)
	for dom, groupSetting := range d {
		for group, v := range groupSetting {
			if _, ok := result[group]; !ok {
				result[group] = make([]int, numDomain)
			}
			result[group][dom] = v
		}
	}
	return result
}

// MBGroupIncomingStat represents the incoming bound memory bandwidth statistics
type MBGroupIncomingStat struct {
	CapacityInMB   int
	FreeInMB       int
	GroupTotalUses map[string]int
	ResourceState  State
	GroupLimits    GroupSettings
	GroupSorted    []sets.String
}
