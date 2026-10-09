/*
Copyright 2026 The Katalyst Authors.

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

package resctrl

import "k8s.io/apimachinery/pkg/util/sets"

type ResctrlConfig struct {
	// EnableResctrlHint is the flag that enable/disable resctrl option related pod admission.
	EnableResctrlHint bool
	// DisableRDT disables RDT CLOS injection and lifecycle from startup config.
	DisableRDT bool
	// EnableResctrlGroupLifecycleManagement is deprecated and kept only for CLI compatibility.
	// Resctrl CLOS lifecycle is controlled by dynamic RDTConfig.DisableRDT.
	EnableResctrlGroupLifecycleManagement bool

	// CPUSetPoolToSharedSubgroup maps non-default shared-core pools to explicit
	// "share-<id>" CLOS groups. The default "share" pool always uses "shared-50".
	CPUSetPoolToSharedSubgroup map[string]int
	EnabledQoS                 []string

	// MonGroupEnabledClosIDs is about mon_group layout hint policy.
	MonGroupEnabledClosIDs []string
	// MonGroupMaxCountRatio is the ratio of mon_groups max count in info/L3_MON/num_rmids.
	MonGroupMaxCountRatio float64

	// DefaultClosIDs is the list of resctrl CLOS directories that should exist by default.
	DefaultClosIDs []string
	// SkipCleanupClosIDs is a list of resctrl closID directories to skip cleaning.
	SkipCleanupClosIDs sets.String
}

func NewResctrlConfig() *ResctrlConfig {
	return &ResctrlConfig{
		CPUSetPoolToSharedSubgroup: make(map[string]int),
		SkipCleanupClosIDs:         sets.NewString(),
	}
}
