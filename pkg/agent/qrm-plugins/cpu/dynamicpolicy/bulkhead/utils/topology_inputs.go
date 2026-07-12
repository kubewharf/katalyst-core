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

package utils

import (
	"sort"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type RelExistsFunc func(rel string) error

func BuildTopologyNodeSpecsFromView(
	cfg bulkheadconfig.BulkheadConfiguration,
	view *CPUSetPartitionView,
	reclaimSiblings []string,
	relExists RelExistsFunc,
) []topology.NodeSpec {
	specs := []topology.NodeSpec{{Rel: cfg.BulkheadPrimaryRelPath, Role: topology.TopoNodeRolePrimary}}
	if view != nil {
		specs[0].CPUs = view.NonReclaimPool
	}

	for reclaimIdx, reclaimRel := range cfg.BulkheadReclaimRelPaths {
		if reclaimRel == "" {
			continue
		}
		if relExists != nil {
			if err := relExists(reclaimRel); err != nil {
				general.InfofV(4, "bulkhead: reclaim rel path does not exist, skipping topology spec, rel=%q err=%v", reclaimRel, err)
				continue
			}
		}
		reclaimSpec := topology.NodeSpec{Rel: reclaimRel, Role: topology.TopoNodeRoleReclaim}
		if view != nil {
			reclaimSpec.CPUs = view.ReclaimEffective
		}
		specs = append(specs, reclaimSpec)

		if view == nil {
			continue
		}
		for _, numaID := range sortedNUMAIDs(view.ReclaimEffectivePerNUMA) {
			cpus := view.ReclaimEffectivePerNUMA[numaID]
			if cpus.IsEmpty() {
				continue
			}
			rel := cfg.ReclaimPerNUMA(reclaimIdx, numaID)
			if rel == "" {
				continue
			}
			if relExists != nil {
				if err := relExists(rel); err != nil {
					general.InfofV(4, "bulkhead: reclaim NUMA rel path does not exist, skipping topology spec, rel=%q err=%v", rel, err)
					continue
				}
			}
			specs = append(specs, topology.NodeSpec{
				Rel:       rel,
				Role:      topology.TopoNodeRoleReclaimNUMABucket,
				CPUs:      cpus,
				Mems:      strconv.Itoa(numaID),
				ParentRel: parentRelForReclaimNUMA(reclaimRel, rel),
				Metadata: map[string]string{
					"numa":          strconv.Itoa(numaID),
					"reclaim-index": strconv.Itoa(reclaimIdx),
				},
			})
		}
	}

	seen := make(map[string]struct{}, len(reclaimSiblings))
	for _, rel := range reclaimSiblings {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			continue
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		if relExists != nil {
			if err := relExists(rel); err != nil {
				general.InfofV(4, "bulkhead: reclaim sibling rel path does not exist, skipping topology spec, rel=%q err=%v", rel, err)
				continue
			}
		}
		spec := topology.NodeSpec{Rel: rel, Role: topology.TopoNodeRoleReclaimSibling}
		if view != nil {
			spec.CPUs = view.ReclaimEffective
		}
		specs = append(specs, spec)
	}
	return specs
}

func sortedNUMAIDs(perNUMA map[int]machine.CPUSet) []int {
	numaIDs := make([]int, 0, len(perNUMA))
	for numaID := range perNUMA {
		numaIDs = append(numaIDs, numaID)
	}
	sort.Ints(numaIDs)
	return numaIDs
}

func parentRelForReclaimNUMA(reclaimRel, numaRel string) string {
	if reclaimRel == "" || numaRel == "" || numaRel == reclaimRel {
		return ""
	}
	if strings.HasPrefix(numaRel, reclaimRel+"/") {
		return reclaimRel
	}
	return ""
}
