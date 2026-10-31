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

package cpusettopology

import (
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type projectedOwner struct {
	identity model.CPUSetPoolIdentity
	cpus     machine.CPUSet
}

func buildAppliedPoolProjection(
	level model.AppliedViewLevel,
	desired *model.DesiredView,
	applied *model.AppliedView,
) model.AppliedPoolProjection {
	projection := model.NewAppliedPoolProjection()
	if desired == nil || applied == nil {
		return projection
	}

	identities := sortedPoolIdentities(desired.PoolOwners)
	if level == model.AppliedViewLevelReclaimOnly {
		for _, identity := range identities {
			if identity != (model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim}) {
				continue
			}
			cpus := appliedProofForOwner(identity, desired.PoolOwners[identity], applied).
				Difference(applied.Reserve)
			if !cpus.IsEmpty() {
				projection.CPUSetByIdentity[identity] = cpus
			}
			break
		}
		return projection
	}

	owners := make([]projectedOwner, 0, len(identities))
	exclusiveCPUs := machine.NewCPUSet()
	for _, identity := range identities {
		owner := desired.PoolOwners[identity]
		cpus := appliedProofForOwner(identity, owner, applied).Difference(applied.Reserve)
		if !identity.Valid() {
			general.Warningf("cpuset_topology: invalid desired pool owner identity=%+v cpus=%s",
				identity, cpus.String())
			if !cpus.IsEmpty() {
				projection.AmbiguousCPUs = projection.AmbiguousCPUs.Union(cpus)
			}
			continue
		}
		if cpus.IsEmpty() {
			continue
		}
		owners = append(owners, projectedOwner{identity: identity, cpus: cpus})
		if identity.Kind == model.CPUSetPoolKindDedicated ||
			identity.Kind == model.CPUSetPoolKindIsolation {
			exclusiveCPUs = exclusiveCPUs.Union(cpus)
		}
	}

	claimCount := map[int]int{}
	for i := range owners {
		if owners[i].identity.Kind == model.CPUSetPoolKindShare {
			owners[i].cpus = owners[i].cpus.Difference(exclusiveCPUs)
		}
		for _, cpu := range owners[i].cpus.Difference(projection.AmbiguousCPUs).ToSliceInt() {
			claimCount[cpu]++
		}
	}
	crossOwnerAmbiguous := machine.NewCPUSet()
	for cpu, count := range claimCount {
		if count > 1 {
			crossOwnerAmbiguous.Add(cpu)
		}
	}
	if !crossOwnerAmbiguous.IsEmpty() {
		conflictingIdentities := make([]model.CPUSetPoolIdentity, 0, len(owners))
		for _, owner := range owners {
			if !owner.cpus.Intersection(crossOwnerAmbiguous).IsEmpty() {
				conflictingIdentities = append(conflictingIdentities, owner.identity)
			}
		}
		general.Warningf("cpuset_topology: cross-owner pool projection overlap identities=%+v cpus=%s",
			conflictingIdentities, crossOwnerAmbiguous.String())
		projection.AmbiguousCPUs = projection.AmbiguousCPUs.Union(crossOwnerAmbiguous)
	}

	reportable := machine.NewCPUSet()
	for _, owner := range owners {
		cpus := owner.cpus.Difference(projection.AmbiguousCPUs)
		if cpus.IsEmpty() {
			continue
		}
		projection.CPUSetByIdentity[owner.identity] = cpus
		reportable = reportable.Union(cpus)
	}
	finalNonReserve := applied.ReclaimEffective.Union(applied.NonReclaimPool).Difference(applied.Reserve)
	projection.UncoveredCPUs = finalNonReserve.
		Difference(reportable).
		Difference(projection.AmbiguousCPUs)
	return projection
}

func sortedPoolIdentities(
	owners map[model.CPUSetPoolIdentity]model.DesiredPoolOwner,
) []model.CPUSetPoolIdentity {
	identities := make([]model.CPUSetPoolIdentity, 0, len(owners))
	for identity := range owners {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		return identities[i].Less(identities[j])
	})
	return identities
}

func appliedProofForOwner(
	identity model.CPUSetPoolIdentity,
	owner model.DesiredPoolOwner,
	applied *model.AppliedView,
) machine.CPUSet {
	if owner.ExpectedCPUSet.IsEmpty() {
		return machine.NewCPUSet()
	}
	switch identity.Kind {
	case model.CPUSetPoolKindReclaim:
		return owner.ExpectedCPUSet.Intersection(applied.ReclaimEffective)
	case model.CPUSetPoolKindShare:
		return owner.ExpectedCPUSet.Intersection(applied.NonReclaimPool)
	case model.CPUSetPoolKindDedicated, model.CPUSetPoolKindIsolation:
		proved := machine.NewCPUSet()
		for containerName := range owner.ContainerCPUSetByName {
			proved = proved.Union(applied.ContainerCPUSetByPod[owner.ProofPodUID][containerName])
		}
		return owner.ExpectedCPUSet.Intersection(proved)
	default:
		return owner.ExpectedCPUSet.Intersection(
			applied.ReclaimEffective.Union(applied.NonReclaimPool),
		)
	}
}
