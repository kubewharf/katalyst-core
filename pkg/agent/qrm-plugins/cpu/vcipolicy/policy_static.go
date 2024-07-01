/*
Copyright 2017 The Kubernetes Authors.

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

package vcipolicy

import (
	"fmt"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/vcipolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Names of the options, as part of the user interface.
const (
	FullPCPUsOneThreadOnly string = "full-pcpus-one-thread-only"
)

// assignableCPUs returns the set of unassigned CPUs minus the reserved set.
func (p *StaticPolicy) assignableCPUs(s state.State) machine.CPUSet {
	return s.GetDefaultCPUSet().Difference(p.reservedCPUs)
}

func (p *StaticPolicy) allocateCPUs(s state.State, numCPUs int, numaAffinity []uint64, cpusPerNUMA map[int]int, policyOpts string) (machine.CPUSet, error) {
	assignableCPUs := p.assignableCPUs(s)
	result := machine.NewCPUSet()
	// For RUSS
	if len(cpusPerNUMA) != 0 {
		general.Infof("[cpumanager] numa id to cpu number:%+v", cpusPerNUMA)
		for nid, num := range cpusPerNUMA {
			var err error
			alignedCPUs := machine.NewCPUSet()
			cpusAvailable := assignableCPUs.Intersection(p.topology.CPUDetails.CPUsInNUMANodes(nid))
			switch policyOpts {
			case FullPCPUsOneThreadOnly:
				alignedCPUs, err = takeByTopologyFreeCores(p.topology, cpusAvailable, num)
			default:
				alignedCPUs, err = takeByTopology(p.topology, cpusAvailable, num)
			}
			if err != nil {
				return machine.NewCPUSet(), err
			}

			result = result.Union(alignedCPUs)
		}

		// Remove allocated CPUs from the shared CPUSet.
		s.SetDefaultCPUSet(s.GetDefaultCPUSet().Difference(result))
		general.Infof("[cpumanager] allocateCPUs by specified numa id returning \"%v\"", result)
		return result, nil
	}

	// If RUSS is not exists, allocate cpu by local cpumanager
	// If there are aligned CPUs in numaAffinity, attempt to take those first.
	if len(numaAffinity) != 0 {
		alignedCPUs := machine.NewCPUSet()
		for _, numaNodeID := range numaAffinity {
			alignedCPUs = alignedCPUs.Union(assignableCPUs.Intersection(p.topology.CPUDetails.CPUsInNUMANodes(int(numaNodeID))))
		}

		numAlignedToAlloc := alignedCPUs.Size()
		if numCPUs < numAlignedToAlloc {
			numAlignedToAlloc = numCPUs
		}

		alignedCPUs, err := takeByTopology(p.topology, alignedCPUs, numAlignedToAlloc)
		if err != nil {
			return machine.NewCPUSet(), err
		}

		result = result.Union(alignedCPUs)
	}

	// Get any remaining CPUs from what's leftover after attempting to grab aligned ones.
	remainingCPUs, err := takeByTopology(p.topology, assignableCPUs.Difference(result), numCPUs-result.Size())
	if err != nil {
		return machine.NewCPUSet(), err
	}
	result = result.Union(remainingCPUs)

	// Remove allocated CPUs from the shared CPUSet.
	s.SetDefaultCPUSet(s.GetDefaultCPUSet().Difference(result))

	general.Infof("[cpumanager] allocateCPUs: returning \"%v\"", result)
	return result, nil
}

func (p *StaticPolicy) validateState(s state.State) error {
	tmpAssignments := s.GetCPUAssignments()
	tmpDefaultCPUset := s.GetDefaultCPUSet()

	// Default cpuset cannot be empty when assignments exist
	if tmpDefaultCPUset.IsEmpty() {
		if len(tmpAssignments) != 0 {
			return fmt.Errorf("default cpuset cannot be empty")
		}
		// state is empty initialize
		allCPUs := p.topology.CPUDetails.CPUs()
		s.SetDefaultCPUSet(allCPUs)
		return nil
	}

	// State has already been initialized from file (is not empty)
	// 1. Check if the reserved cpuset is not part of default cpuset because:
	// - kube/system reserved have changed (increased) - may lead to some containers not being able to start
	// - user tampered with file
	if !p.reservedCPUs.Intersection(tmpDefaultCPUset).Equals(p.reservedCPUs) {
		return fmt.Errorf("not all reserved cpus: \"%s\" are present in defaultCpuSet: \"%s\"",
			p.reservedCPUs.String(), tmpDefaultCPUset.String())
	}

	// 2. Check if state for static policy is consistent
	for pod, cset := range tmpAssignments {
		// None of the cpu in DEFAULT cset should be in s.assignments
		if !tmpDefaultCPUset.Intersection(cset).IsEmpty() {
			return fmt.Errorf("pod: %s cpuset: \"%s\" overlaps with default cpuset \"%s\"",
				pod, cset.String(), tmpDefaultCPUset.String())
		}

	}

	// 3. It's possible that the set of available CPUs has changed since
	// the state was written. This can be due to for example
	// offlining a CPU when kubelet is not running. If this happens,
	// CPU manager will run into trouble when later it tries to
	// assign non-existent CPUs to containers. Validate that the
	// topology that was received during CPU manager startup matches with
	// the set of CPUs stored in the state.
	totalKnownCPUs := tmpDefaultCPUset.Clone()
	tmpCPUSets := []machine.CPUSet{}
	for _, cset := range tmpAssignments {
		tmpCPUSets = append(tmpCPUSets, cset)
	}
	totalKnownCPUs = totalKnownCPUs.UnionAll(tmpCPUSets)
	if !totalKnownCPUs.Equals(p.topology.CPUDetails.CPUs()) {
		return fmt.Errorf("current set of available CPUs \"%s\" doesn't match with CPUs in state \"%s\"",
			p.topology.CPUDetails.CPUs().String(), totalKnownCPUs.String())
	}

	return nil
}
