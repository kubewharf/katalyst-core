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

package model

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

type CPUSetPartitionView struct {
	Reserve                             machine.CPUSet
	Dedicated                           machine.CPUSet
	ReclaimRaw                          machine.CPUSet
	SharePool                           machine.CPUSet
	SharePoolMap                        map[string]machine.CPUSet
	Isolation                           machine.CPUSet
	DesiredNonReclaimPool               machine.CPUSet
	DesiredReclaimEffective             machine.CPUSet
	DesiredReclaimEffectivePerNUMA      map[int]machine.CPUSet
	TransientProtectedNonReclaim        machine.CPUSet
	TransientProtectedNonReclaimPerNUMA map[int]machine.CPUSet
	NonReclaimPool                      machine.CPUSet
	ReclaimEffective                    machine.CPUSet
	ReclaimEffectivePerNUMA             map[int]machine.CPUSet
	ContainerCPUSetByPod                map[string]map[string]machine.CPUSet
}

type CPUSetPoolKind string

const (
	CPUSetPoolKindReclaim   CPUSetPoolKind = "reclaim"
	CPUSetPoolKindShare     CPUSetPoolKind = "share"
	CPUSetPoolKindDedicated CPUSetPoolKind = "dedicated"
	CPUSetPoolKindIsolation CPUSetPoolKind = "isolation"
)

type CPUSetPoolIdentity struct {
	Kind         CPUSetPoolKind
	Name         string
	PodNamespace string
	PodName      string
}

func (i CPUSetPoolIdentity) Less(other CPUSetPoolIdentity) bool {
	if i.Kind != other.Kind {
		return i.Kind < other.Kind
	}
	if i.Name != other.Name {
		return i.Name < other.Name
	}
	if i.PodNamespace != other.PodNamespace {
		return i.PodNamespace < other.PodNamespace
	}
	return i.PodName < other.PodName
}

func (i CPUSetPoolIdentity) Valid() bool {
	switch i.Kind {
	case CPUSetPoolKindReclaim:
		return i.Name == "" && i.PodNamespace == "" && i.PodName == ""
	case CPUSetPoolKindShare:
		return i.Name != "" && i.PodNamespace == "" && i.PodName == ""
	case CPUSetPoolKindDedicated, CPUSetPoolKindIsolation:
		return i.Name == "" && i.PodNamespace != "" && i.PodName != ""
	default:
		return false
	}
}

type DesiredPoolOwner struct {
	ProofPodUID           string
	ExpectedCPUSet        machine.CPUSet
	ContainerCPUSetByName map[string]machine.CPUSet
}

type DesiredView struct {
	CPUSetPartitionView
	PoolOwners map[CPUSetPoolIdentity]DesiredPoolOwner
}

type AppliedViewLevel string

const (
	AppliedViewLevelParentSafe  AppliedViewLevel = "parent_safe"
	AppliedViewLevelReclaimOnly AppliedViewLevel = "reclaim_only"
	AppliedViewLevelFull        AppliedViewLevel = "full"
)

type AppliedView struct {
	CPUSetPartitionView
	Level AppliedViewLevel
	// CPUSetByRel is the final-snapshot proof for each controlled topology rel.
	// Consumers that act on a specific cgroup must authorize against this map
	// instead of inferring ownership from an aggregate partition union.
	CPUSetByRel map[string]machine.CPUSet
	// RelProofByRel binds each final-snapshot CPUSet proof to the stable
	// device/inode identity of the cgroup directory that was observed.
	RelProofByRel map[string]CgroupRelProof
	// PoolProjection is the final-snapshot-authorized pool ownership.
	PoolProjection AppliedPoolProjection
}

type AppliedPoolProjection struct {
	CPUSetByIdentity map[CPUSetPoolIdentity]machine.CPUSet
	UncoveredCPUs    machine.CPUSet
	AmbiguousCPUs    machine.CPUSet
}

// CgroupRelProof binds an observed CPUSet to a stable cgroup directory identity.
type CgroupRelProof struct {
	Device uint64
	Inode  uint64
	CPUSet machine.CPUSet
}

func NewCPUSetPartitionView() CPUSetPartitionView {
	return CPUSetPartitionView{
		Reserve:                             machine.NewCPUSet(),
		Dedicated:                           machine.NewCPUSet(),
		ReclaimRaw:                          machine.NewCPUSet(),
		SharePool:                           machine.NewCPUSet(),
		SharePoolMap:                        map[string]machine.CPUSet{},
		Isolation:                           machine.NewCPUSet(),
		DesiredNonReclaimPool:               machine.NewCPUSet(),
		DesiredReclaimEffective:             machine.NewCPUSet(),
		DesiredReclaimEffectivePerNUMA:      map[int]machine.CPUSet{},
		TransientProtectedNonReclaim:        machine.NewCPUSet(),
		TransientProtectedNonReclaimPerNUMA: map[int]machine.CPUSet{},
		NonReclaimPool:                      machine.NewCPUSet(),
		ReclaimEffective:                    machine.NewCPUSet(),
		ReclaimEffectivePerNUMA:             map[int]machine.CPUSet{},
		ContainerCPUSetByPod:                map[string]map[string]machine.CPUSet{},
	}
}

func NewDesiredView() *DesiredView {
	return &DesiredView{
		CPUSetPartitionView: NewCPUSetPartitionView(),
		PoolOwners:          map[CPUSetPoolIdentity]DesiredPoolOwner{},
	}
}

func NewAppliedPoolProjection() AppliedPoolProjection {
	return AppliedPoolProjection{
		CPUSetByIdentity: map[CPUSetPoolIdentity]machine.CPUSet{},
		UncoveredCPUs:    machine.NewCPUSet(),
		AmbiguousCPUs:    machine.NewCPUSet(),
	}
}

func (v *DesiredView) DeepCopy() *DesiredView {
	if v == nil {
		return nil
	}
	return &DesiredView{
		CPUSetPartitionView: *v.CPUSetPartitionView.DeepCopy(),
		PoolOwners:          cloneDesiredPoolOwners(v.PoolOwners),
	}
}

func (v *DesiredView) ToAppliedView() *AppliedView {
	if v == nil {
		return nil
	}
	return &AppliedView{
		CPUSetPartitionView: *v.CPUSetPartitionView.DeepCopy(),
		Level:               AppliedViewLevelFull,
		CPUSetByRel:         map[string]machine.CPUSet{},
		RelProofByRel:       map[string]CgroupRelProof{},
		PoolProjection:      NewAppliedPoolProjection(),
	}
}

func (v *AppliedView) DeepCopy() *AppliedView {
	if v == nil {
		return nil
	}
	return &AppliedView{
		CPUSetPartitionView: *v.CPUSetPartitionView.DeepCopy(),
		Level:               v.Level,
		CPUSetByRel:         CloneCPUSetMap(v.CPUSetByRel),
		RelProofByRel:       cloneCgroupRelProofMap(v.RelProofByRel),
		PoolProjection:      cloneAppliedPoolProjection(v.PoolProjection),
	}
}

func (v *CPUSetPartitionView) DeepCopy() *CPUSetPartitionView {
	if v == nil {
		return nil
	}
	out := &CPUSetPartitionView{
		Reserve:                             v.Reserve.Clone(),
		Dedicated:                           v.Dedicated.Clone(),
		ReclaimRaw:                          v.ReclaimRaw.Clone(),
		SharePool:                           v.SharePool.Clone(),
		SharePoolMap:                        map[string]machine.CPUSet{},
		Isolation:                           v.Isolation.Clone(),
		DesiredNonReclaimPool:               v.DesiredNonReclaimPool.Clone(),
		DesiredReclaimEffective:             v.DesiredReclaimEffective.Clone(),
		DesiredReclaimEffectivePerNUMA:      map[int]machine.CPUSet{},
		TransientProtectedNonReclaim:        v.TransientProtectedNonReclaim.Clone(),
		TransientProtectedNonReclaimPerNUMA: map[int]machine.CPUSet{},
		NonReclaimPool:                      v.NonReclaimPool.Clone(),
		ReclaimEffective:                    v.ReclaimEffective.Clone(),
		ReclaimEffectivePerNUMA:             map[int]machine.CPUSet{},
		ContainerCPUSetByPod:                map[string]map[string]machine.CPUSet{},
	}
	for numaID, cpus := range v.ReclaimEffectivePerNUMA {
		out.ReclaimEffectivePerNUMA[numaID] = cpus.Clone()
	}
	for numaID, cpus := range v.DesiredReclaimEffectivePerNUMA {
		out.DesiredReclaimEffectivePerNUMA[numaID] = cpus.Clone()
	}
	for numaID, cpus := range v.TransientProtectedNonReclaimPerNUMA {
		out.TransientProtectedNonReclaimPerNUMA[numaID] = cpus.Clone()
	}
	for poolName, cpus := range v.SharePoolMap {
		out.SharePoolMap[poolName] = cpus.Clone()
	}
	for podUID, containers := range v.ContainerCPUSetByPod {
		out.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{}
		for containerName, cpus := range containers {
			out.ContainerCPUSetByPod[podUID][containerName] = cpus.Clone()
		}
	}
	return out
}

func EqualDesiredView(a, b *DesiredView) bool {
	if !equalCPUSetPartitionView(cpuSetPartitionViewFromDesired(a), cpuSetPartitionViewFromDesired(b)) {
		return false
	}
	if a == nil || b == nil {
		return a == b
	}
	return equalDesiredPoolOwners(a.PoolOwners, b.PoolOwners)
}

func EqualAppliedView(a, b *AppliedView) bool {
	if !equalCPUSetPartitionView(cpuSetPartitionViewFromApplied(a), cpuSetPartitionViewFromApplied(b)) {
		return false
	}
	if a == nil || b == nil {
		return a == b
	}
	return equalCPUSetMap(a.CPUSetByRel, b.CPUSetByRel) &&
		equalCgroupRelProofMap(a.RelProofByRel, b.RelProofByRel) &&
		equalAppliedPoolProjection(a.PoolProjection, b.PoolProjection) &&
		a.Level == b.Level
}

func cpuSetPartitionViewFromDesired(v *DesiredView) *CPUSetPartitionView {
	if v == nil {
		return nil
	}
	return &v.CPUSetPartitionView
}

func cpuSetPartitionViewFromApplied(v *AppliedView) *CPUSetPartitionView {
	if v == nil {
		return nil
	}
	return &v.CPUSetPartitionView
}

func equalCPUSetPartitionView(a, b *CPUSetPartitionView) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Reserve.Equals(b.Reserve) &&
		a.Dedicated.Equals(b.Dedicated) &&
		a.ReclaimRaw.Equals(b.ReclaimRaw) &&
		a.SharePool.Equals(b.SharePool) &&
		equalCPUSetMap(a.SharePoolMap, b.SharePoolMap) &&
		a.Isolation.Equals(b.Isolation) &&
		a.DesiredNonReclaimPool.Equals(b.DesiredNonReclaimPool) &&
		a.DesiredReclaimEffective.Equals(b.DesiredReclaimEffective) &&
		equalCPUSetByNUMA(a.DesiredReclaimEffectivePerNUMA, b.DesiredReclaimEffectivePerNUMA) &&
		a.TransientProtectedNonReclaim.Equals(b.TransientProtectedNonReclaim) &&
		equalCPUSetByNUMA(a.TransientProtectedNonReclaimPerNUMA, b.TransientProtectedNonReclaimPerNUMA) &&
		a.NonReclaimPool.Equals(b.NonReclaimPool) &&
		a.ReclaimEffective.Equals(b.ReclaimEffective) &&
		equalCPUSetByNUMA(a.ReclaimEffectivePerNUMA, b.ReclaimEffectivePerNUMA) &&
		equalNestedCPUSetMap(a.ContainerCPUSetByPod, b.ContainerCPUSetByPod)
}

func equalCPUSetMap(a, b map[string]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || !av.Equals(bv) {
			return false
		}
	}
	return true
}

func CloneCPUSetMap(in map[string]machine.CPUSet) map[string]machine.CPUSet {
	out := make(map[string]machine.CPUSet, len(in))
	for key, cpus := range in {
		out[key] = cpus.Clone()
	}
	return out
}

func cloneDesiredPoolOwners(in map[CPUSetPoolIdentity]DesiredPoolOwner) map[CPUSetPoolIdentity]DesiredPoolOwner {
	out := make(map[CPUSetPoolIdentity]DesiredPoolOwner, len(in))
	for identity, owner := range in {
		out[identity] = DesiredPoolOwner{
			ProofPodUID:           owner.ProofPodUID,
			ExpectedCPUSet:        CloneOptionalCPUSet(owner.ExpectedCPUSet),
			ContainerCPUSetByName: cloneOptionalCPUSetMap(owner.ContainerCPUSetByName),
		}
	}
	return out
}

func equalDesiredPoolOwners(a, b map[CPUSetPoolIdentity]DesiredPoolOwner) bool {
	if len(a) != len(b) {
		return false
	}
	for identity, ownerA := range a {
		ownerB, ok := b[identity]
		if !ok ||
			ownerA.ProofPodUID != ownerB.ProofPodUID ||
			!ownerA.ExpectedCPUSet.Equals(ownerB.ExpectedCPUSet) ||
			!equalCPUSetMap(ownerA.ContainerCPUSetByName, ownerB.ContainerCPUSetByName) {
			return false
		}
	}
	return true
}

func cloneAppliedPoolProjection(in AppliedPoolProjection) AppliedPoolProjection {
	return AppliedPoolProjection{
		CPUSetByIdentity: cloneCPUSetByIdentity(in.CPUSetByIdentity),
		UncoveredCPUs:    CloneOptionalCPUSet(in.UncoveredCPUs),
		AmbiguousCPUs:    CloneOptionalCPUSet(in.AmbiguousCPUs),
	}
}

func equalAppliedPoolProjection(a, b AppliedPoolProjection) bool {
	return equalCPUSetByIdentity(a.CPUSetByIdentity, b.CPUSetByIdentity) &&
		a.UncoveredCPUs.Equals(b.UncoveredCPUs) &&
		a.AmbiguousCPUs.Equals(b.AmbiguousCPUs)
}

func cloneCPUSetByIdentity(in map[CPUSetPoolIdentity]machine.CPUSet) map[CPUSetPoolIdentity]machine.CPUSet {
	out := make(map[CPUSetPoolIdentity]machine.CPUSet, len(in))
	for identity, cpus := range in {
		out[identity] = CloneOptionalCPUSet(cpus)
	}
	return out
}

func equalCPUSetByIdentity(a, b map[CPUSetPoolIdentity]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for identity, cpusA := range a {
		cpusB, ok := b[identity]
		if !ok || !cpusA.Equals(cpusB) {
			return false
		}
	}
	return true
}

func cloneOptionalCPUSetMap(in map[string]machine.CPUSet) map[string]machine.CPUSet {
	out := make(map[string]machine.CPUSet, len(in))
	for key, cpus := range in {
		out[key] = CloneOptionalCPUSet(cpus)
	}
	return out
}

// CloneOptionalCPUSet returns a deep copy of in, preserving the
// uninitialized (zero-value) state so that consumers can distinguish
// "not set" from "set to empty". This is the shared implementation used
// across the bulkhead subsystem for optional CPUSet fields.
func CloneOptionalCPUSet(in machine.CPUSet) machine.CPUSet {
	if !in.Initialed {
		return machine.CPUSet{}
	}
	return in.Clone()
}

func cloneCgroupRelProofMap(in map[string]CgroupRelProof) map[string]CgroupRelProof {
	out := make(map[string]CgroupRelProof, len(in))
	for key, proof := range in {
		proof.CPUSet = proof.CPUSet.Clone()
		out[key] = proof
	}
	return out
}

func equalCgroupRelProofMap(a, b map[string]CgroupRelProof) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || av.Device != bv.Device || av.Inode != bv.Inode || !av.CPUSet.Equals(bv.CPUSet) {
			return false
		}
	}
	return true
}

func equalCPUSetByNUMA(a, b map[int]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for key, av := range a {
		bv, ok := b[key]
		if !ok || !av.Equals(bv) {
			return false
		}
	}
	return true
}

func equalNestedCPUSetMap(a, b map[string]map[string]machine.CPUSet) bool {
	if len(a) != len(b) {
		return false
	}
	for podUID, containersA := range a {
		containersB, ok := b[podUID]
		if !ok || !equalCPUSetMap(containersA, containersB) {
			return false
		}
	}
	return true
}
