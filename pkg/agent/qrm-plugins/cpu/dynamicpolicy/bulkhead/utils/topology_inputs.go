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
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type RelExistsFunc func(rel string) error

func BuildTopologyNodeSpecsFromView(
	cfg bulkheadconfig.BulkheadConfiguration,
	view *model.CPUSetPartitionView,
	cpuDetails machine.CPUDetails,
	reclaimSiblings []string,
	relExists RelExistsFunc,
) ([]topology.NodeSpec, error) {
	specs := []topology.NodeSpec{{
		Rel:            cfg.BulkheadPrimaryRelPath,
		Role:           topology.TopoNodeRolePrimary,
		Domain:         topology.DomainPrimary,
		ControlledRoot: true,
		TrustAnchor:    true,
	}}
	if view != nil {
		specs[0].CPUs = view.NonReclaimPool
	}

	for reclaimIdx, reclaimRel := range cfg.BulkheadReclaimRelPaths {
		if reclaimRel == "" {
			continue
		}
		if relExists != nil {
			if err := relExists(reclaimRel); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("stat reclaim rel path %q: %w", reclaimRel, err)
				}
				general.InfofV(4, "bulkhead: reclaim rel path does not exist, retaining topology boundary, rel=%q err=%v", reclaimRel, err)
			}
		}
		reclaimSpec := topology.NodeSpec{
			Rel:            reclaimRel,
			Role:           topology.TopoNodeRoleReclaim,
			Domain:         topology.DomainReclaim,
			ControlledRoot: true,
			TrustAnchor:    true,
			Metadata: map[string]string{
				"reclaim-index": strconv.Itoa(reclaimIdx),
			},
		}
		if view != nil {
			reclaimSpec.CPUs = view.ReclaimEffective
		}
		specs = append(specs, reclaimSpec)

		if view == nil {
			continue
		}
		for _, numaID := range sortedPhysicalAndViewNUMAIDs(view.ReclaimEffectivePerNUMA, cpuDetails) {
			cpus := view.ReclaimEffectivePerNUMA[numaID]
			physicalNUMACPUs := cpuDetails.CPUsInNUMANodes(numaID)
			if physicalNUMACPUs.IsEmpty() {
				return nil, fmt.Errorf("build reclaim NUMA bucket %d: physical CPU topology is required", numaID)
			}
			rel := cfg.ReclaimPerNUMA(reclaimIdx, numaID)
			if rel == "" {
				continue
			}
			if relExists != nil {
				if err := relExists(rel); err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						return nil, fmt.Errorf("stat reclaim NUMA rel path %q: %w", rel, err)
					}
					general.InfofV(4, "bulkhead: reclaim NUMA rel path does not exist, retaining topology boundary, rel=%q err=%v", rel, err)
				}
			}
			// cpuset_topology owns cpuset.cpus only. cpuset.mems for reclaim
			// NUMA buckets is reconciled by the independent cpuset_mems plugin
			// so it can run outside admission and be rolled back separately.
			specs = append(specs, topology.NodeSpec{
				Rel:         rel,
				Role:        topology.TopoNodeRoleReclaimNUMABucket,
				CPUs:        cpus,
				ParentRel:   parentRelForReclaimNUMA(reclaimRel, rel),
				Domain:      topology.DomainReclaim,
				TrustAnchor: true,
				Constraint: topology.TopologyConstraint{
					CPUUpperBound: physicalNUMACPUs,
					Scope:         topology.TopologyScopeNUMANode,
				},
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
				if !errors.Is(err, os.ErrNotExist) {
					return nil, fmt.Errorf("stat reclaim sibling rel path %q: %w", rel, err)
				}
				general.InfofV(4, "bulkhead: reclaim sibling rel path does not exist, retaining topology boundary, rel=%q err=%v", rel, err)
			}
		}
		spec := topology.NodeSpec{
			Rel:            rel,
			Role:           topology.TopoNodeRoleReclaimSibling,
			Domain:         topology.DomainReclaim,
			ControlledRoot: true,
			TrustAnchor:    true,
		}
		if view != nil {
			spec.CPUs = view.ReclaimEffective
		}
		specs = append(specs, spec)
	}
	if err := completeReclaimHierarchyEnvelopes(specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func completeReclaimHierarchyEnvelopes(specs []topology.NodeSpec) error {
	reclaimIndexes := make([]int, 0)
	for i := range specs {
		if specs[i].Role == topology.TopoNodeRoleReclaim {
			reclaimIndexes = append(reclaimIndexes, i)
		}
	}

	for _, index := range reclaimIndexes {
		specs[index].ParentRel = nearestReclaimAncestor(specs[index].Rel, specs, reclaimIndexes)
	}

	for i := range specs {
		if specs[i].Role != topology.TopoNodeRoleReclaimNUMABucket {
			continue
		}
		specs[i].ParentRel = nearestReclaimAncestor(specs[i].Rel, specs, reclaimIndexes)
	}
	return nil
}

func nearestReclaimAncestor(rel string, specs []topology.NodeSpec, reclaimIndexes []int) string {
	parent := ""
	for _, index := range reclaimIndexes {
		candidate := specs[index].Rel
		if isStrictRelDescendant(rel, candidate) && len(candidate) > len(parent) {
			parent = candidate
		}
	}
	return parent
}

func isStrictRelDescendant(rel, ancestor string) bool {
	rel = strings.Trim(rel, "/")
	ancestor = strings.Trim(ancestor, "/")
	return rel != "" && ancestor != "" && rel != ancestor && strings.HasPrefix(rel, ancestor+"/")
}

func sortedPhysicalAndViewNUMAIDs(perNUMA map[int]machine.CPUSet, cpuDetails machine.CPUDetails) []int {
	seen := map[int]struct{}{}
	for _, numaID := range cpuDetails.NUMANodes().ToSliceInt() {
		seen[numaID] = struct{}{}
	}
	for numaID := range perNUMA {
		seen[numaID] = struct{}{}
	}
	numaIDs := make([]int, 0, len(seen))
	for numaID := range seen {
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
