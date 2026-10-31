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

package cpumetrics

import (
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type labeledPool struct {
	identity model.CPUSetPoolIdentity
	label    string
	cpus     machine.CPUSet
}

type labelAssignment struct {
	pools             []labeledPool
	conflictCPUByKind map[model.CPUSetPoolKind]machine.CPUSet
}

func assignPoolLabels(
	byIdentity map[model.CPUSetPoolIdentity]machine.CPUSet,
) labelAssignment {
	result := labelAssignment{
		conflictCPUByKind: map[model.CPUSetPoolKind]machine.CPUSet{
			model.CPUSetPoolKindShare:     machine.NewCPUSet(),
			model.CPUSetPoolKindDedicated: machine.NewCPUSet(),
			model.CPUSetPoolKindIsolation: machine.NewCPUSet(),
		},
	}
	identities := sortedAppliedPoolIdentities(byIdentity)
	labels := make(map[model.CPUSetPoolIdentity]string, len(identities))
	fixedByLabel := make(map[string][]model.CPUSetPoolIdentity)

	for _, identity := range identities {
		label, fixed := formattedPoolLabel(identity)
		if !fixed {
			continue
		}
		if label == "" {
			addConflictCPU(&result, identity, byIdentity[identity])
			continue
		}
		fixedByLabel[label] = append(fixedByLabel[label], identity)
	}

	for label, fixed := range fixedByLabel {
		reclaimIndex := -1
		for i, identity := range fixed {
			if identity.Kind == model.CPUSetPoolKindReclaim {
				reclaimIndex = i
				break
			}
		}
		if reclaimIndex >= 0 {
			labels[fixed[reclaimIndex]] = label
			for i, identity := range fixed {
				if i != reclaimIndex {
					addConflictCPU(&result, identity, byIdentity[identity])
				}
			}
			continue
		}
		if len(fixed) == 1 {
			labels[fixed[0]] = label
			continue
		}
		for _, identity := range fixed {
			addConflictCPU(&result, identity, byIdentity[identity])
		}
	}

	for _, identity := range identities {
		label, ok := labels[identity]
		if !ok {
			continue
		}
		// pool.cpus is only read downstream (IsEmpty / ToSliceInt / Intersection),
		// never mutated, and the projection is an immutable snapshot for this
		// periodical run, so sharing the underlying CPUSet map avoids a per-pool
		// deep Clone.
		result.pools = append(result.pools, labeledPool{
			identity: identity,
			label:    label,
			cpus:     byIdentity[identity],
		})
	}
	return result
}

func formattedPoolLabel(identity model.CPUSetPoolIdentity) (string, bool) {
	switch identity.Kind {
	case model.CPUSetPoolKindReclaim:
		return utilmetric.MetricTagValueFormat(model.CPUSetPoolKindReclaim), true
	case model.CPUSetPoolKindShare:
		return utilmetric.MetricTagValueFormat(identity.Name), true
	case model.CPUSetPoolKindDedicated, model.CPUSetPoolKindIsolation:
		if identity.PodNamespace == "" || identity.PodName == "" {
			return "", true
		}
		return utilmetric.MetricTagValueFormat(string(identity.Kind) + "-" + identity.PodNamespace + "/" + identity.PodName), true
	default:
		return "", false
	}
}

func sortedAppliedPoolIdentities(
	byIdentity map[model.CPUSetPoolIdentity]machine.CPUSet,
) []model.CPUSetPoolIdentity {
	identities := make([]model.CPUSetPoolIdentity, 0, len(byIdentity))
	for identity := range byIdentity {
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		return identities[i].Less(identities[j])
	})
	return identities
}

func addConflictCPU(
	result *labelAssignment,
	identity model.CPUSetPoolIdentity,
	cpus machine.CPUSet,
) {
	current, ok := result.conflictCPUByKind[identity.Kind]
	if !ok {
		return
	}
	result.conflictCPUByKind[identity.Kind] = current.Union(cpus)
}
