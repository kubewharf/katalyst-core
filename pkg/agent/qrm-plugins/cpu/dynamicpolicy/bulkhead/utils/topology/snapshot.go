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

package topology

import (
	"context"
	"fmt"
	"path/filepath"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type domainSnapshot struct {
	allowedCPUs machine.CPUSet

	observedByRel         map[string]machine.CPUSet
	targetByRel           map[string]machine.CPUSet
	observedPrimaryDomain machine.CPUSet
	targetPrimaryDomain   machine.CPUSet
	observedReclaimDomain machine.CPUSet
	targetReclaimDomain   machine.CPUSet
}

// buildDomainSnapshot captures the observed cpuset ownership before any apply
// phase starts. This keeps a phase from mixing its own writes with the state it
// is reasoning about; convergence rereads the cpusets of discovered or cached
// rels after applying changes. Dynamic child discovery in that pass remains
// bounded by the applyCache rel list.
func buildDomainSnapshot(ctx context.Context, cg cgroupclient.CgroupClient, dag *TopoDAG, targetByRel map[string]machine.CPUSet, cpuDetails machine.CPUDetails, reservedCPUs machine.CPUSet, cache *applyCache) (domainSnapshot, error) {
	snapshot := domainSnapshot{
		observedByRel:         map[string]machine.CPUSet{},
		targetByRel:           cloneCPUSetMap(targetByRel),
		observedPrimaryDomain: machine.NewCPUSet(),
		targetPrimaryDomain:   domainTargetUnion(domainNodes(dag, cpusetDomainPrimary), targetByRel),
		observedReclaimDomain: machine.NewCPUSet(),
		targetReclaimDomain:   domainTargetUnion(domainNodes(dag, cpusetDomainReclaim), targetByRel),
	}
	if len(cpuDetails) > 0 {
		desiredUnion := snapshot.targetPrimaryDomain.Union(snapshot.targetReclaimDomain)
		snapshot.allowedCPUs = cpuDetails.CPUs().Difference(reservedCPUs).Intersection(desiredUnion)
	} else {
		snapshot.allowedCPUs = machine.NewCPUSet()
	}

	controlled := map[string]struct{}{}
	for _, node := range dag.Nodes() {
		controlled[node.Rel] = struct{}{}
	}
	for _, node := range dag.Nodes() {
		if err := snapshot.observeRel(ctx, cg, node.Rel, domainOf(node.Role), controlled, cache); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

func (s *domainSnapshot) observeRel(ctx context.Context, cg cgroupclient.CgroupClient, rel string, domain cpusetDomain, controlled map[string]struct{}, cache *applyCache) error {
	current, err := cg.ReadCPUSet(ctx, rel)
	if err != nil {
		if _, isControlled := controlled[rel]; !isControlled && isCgroupNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("snapshot read cpuset, rel=%q: %w", rel, err)
	}
	s.recordOwner(rel, current, domain)

	children, err := cache.listChildren(ctx, rel)
	if err != nil {
		if _, isControlled := controlled[rel]; !isControlled && isCgroupNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("snapshot list children, rel=%q: %w", rel, err)
	}
	for _, name := range children {
		childRel := filepath.Join(rel, name)
		if _, ok := controlled[childRel]; ok {
			continue
		}
		if err := s.observeRel(ctx, cg, childRel, domain, controlled, cache); err != nil {
			return err
		}
	}
	return nil
}

func (s *domainSnapshot) recordOwner(rel string, cpus machine.CPUSet, domain cpusetDomain) {
	s.observedByRel[rel] = cpus.Clone()
	switch domain {
	case cpusetDomainPrimary:
		s.observedPrimaryDomain = s.observedPrimaryDomain.Union(cpus)
	case cpusetDomainReclaim:
		s.observedReclaimDomain = s.observedReclaimDomain.Union(cpus)
	}
}

func (s *domainSnapshot) unownedCPUs() machine.CPUSet {
	if s.allowedCPUs.IsEmpty() {
		return machine.NewCPUSet()
	}
	return s.allowedCPUs.
		Difference(s.observedPrimaryDomain).
		Difference(s.observedReclaimDomain)
}

func (s *domainSnapshot) safeUnownedToPrimary() machine.CPUSet {
	return s.targetPrimaryDomain.Intersection(s.unownedCPUs())
}

func (s *domainSnapshot) safeUnownedToReclaim() machine.CPUSet {
	return s.targetReclaimDomain.Intersection(s.unownedCPUs())
}

func cloneCPUSetMap(in map[string]machine.CPUSet) map[string]machine.CPUSet {
	out := make(map[string]machine.CPUSet, len(in))
	for rel, cpus := range in {
		out[rel] = cpus.Clone()
	}
	return out
}
