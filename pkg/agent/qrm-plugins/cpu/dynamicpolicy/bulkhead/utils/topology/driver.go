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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// RootRef identifies a hierarchy root selected for topology reconciliation.
type RootRef struct {
	Rel      string
	Identity CgroupIdentity
}

// EntryState is one stable read of a hierarchy entry.
type EntryState struct {
	Rel      string
	Identity CgroupIdentity
	// CPUs/Mems are the effective kernel state used by the planner and ownership.
	CPUs machine.CPUSet
	Mems string
	// ConfiguredCPUs/ConfiguredMems record configured state for empty-target proofs, snapshots, and publication validation;
	// in cgroup v2, empty configured state means parent inheritance, not an empty effective set.
	ConfiguredCPUs machine.CPUSet
	ConfiguredMems string
	// Activity proves that both cgroup v1 membership files and child closure
	// were read from the same pinned cgroup directory generation.
	Activity CgroupActivity
}

type CgroupActivity struct {
	TasksEmpty       bool
	CgroupProcsEmpty bool
	// Childless is true only when one pinned-directory observation saw the same
	// empty child set both before and after membership and cpuset reads.
	Childless bool
}

func (a CgroupActivity) Inactive() bool {
	return a.TasksEmpty && a.CgroupProcsEmpty && a.Childless
}

// HierarchyCapabilities describes semantics guaranteed by a hierarchy backend.
type HierarchyCapabilities struct {
	StableIdentity          bool
	EmptyConfiguredCPUSet   bool
	EffectiveCPUSet         bool
	KernelParentContainment bool
	PartitionRoots          bool
}

// cgroupVersionPolicy only describes immutable cgroup-version semantics; it holds no state and performs no filesystem I/O.
// Constants avoid accidental runtime policy changes through mutable package-level structs.
type cgroupVersionPolicy uint8

const (
	cgroupV1Policy cgroupVersionPolicy = iota + 1
	cgroupV2Policy
)

func (p cgroupVersionPolicy) observedCPUsFile() string {
	if p == cgroupV2Policy {
		return "cpuset.cpus.effective"
	}
	return "cpuset.cpus"
}

func (p cgroupVersionPolicy) observedMemsFile() string {
	if p == cgroupV2Policy {
		return "cpuset.mems.effective"
	}
	return "cpuset.mems"
}

func (p cgroupVersionPolicy) configuredCPUsFile() string { return "cpuset.cpus" }

func (p cgroupVersionPolicy) configuredMemsFile() string { return "cpuset.mems" }

func (p cgroupVersionPolicy) validateConfiguredCPUs(cpus machine.CPUSet) error {
	if cpus.IsEmpty() && p != cgroupV2Policy {
		return fmt.Errorf("%w", ErrEmptyCPUSetUnsupported)
	}
	return nil
}

func (p cgroupVersionPolicy) capabilities(stable bool) HierarchyCapabilities {
	isV2 := p == cgroupV2Policy
	return HierarchyCapabilities{
		StableIdentity:          stable,
		EmptyConfiguredCPUSet:   isV2,
		EffectiveCPUSet:         isV2,
		KernelParentContainment: true,
		// Partition and sched-load-balance remain handled by CgroupClient logic above the coordinator.
		PartitionRoots: false,
	}
}

// HierarchyOperation identifies the driver operation being classified.
type HierarchyOperation string

const (
	HierarchyOperationRoots     HierarchyOperation = "roots"
	HierarchyOperationStat      HierarchyOperation = "stat"
	HierarchyOperationRead      HierarchyOperation = "read"
	HierarchyOperationList      HierarchyOperation = "list"
	HierarchyOperationWriteCPUs HierarchyOperation = "write_cpus"
	HierarchyOperationWriteMems HierarchyOperation = "write_mems"
)

// HierarchyErrorClass drives fail-closed coordinator handling.
type HierarchyErrorClass string

const (
	HierarchyErrorNone    HierarchyErrorClass = "none"
	HierarchyErrorStale   HierarchyErrorClass = "stale"
	HierarchyErrorInvalid HierarchyErrorClass = "invalid"
	HierarchyErrorBudget  HierarchyErrorClass = "budget"
)

// HierarchyDriver is the only hierarchy I/O boundary used by the phase
// coordinator. The owner must call Close. Writes are conditional on the
// identity observed by the plan.
type HierarchyDriver interface {
	Close() error
	Roots(context.Context) ([]RootRef, error)
	StatIdentity(context.Context, string) (CgroupIdentity, error)
	ReadEntry(context.Context, string) (EntryState, error)
	ListChildren(context.Context, string) ([]ChildRef, error)
	WriteCPUs(context.Context, string, CgroupIdentity, machine.CPUSet) error
	WriteMems(context.Context, string, CgroupIdentity, string) error
	Classify(error, HierarchyOperation) HierarchyErrorClass
	Capabilities() HierarchyCapabilities
}

type hierarchyActivityReader interface {
	ReadEntryWithActivity(context.Context, string, CgroupIdentity) (EntryState, error)
}

type budgetedHierarchyActivityReader interface {
	readEntryWithActivityAndBudget(context.Context, string, CgroupIdentity, *BudgetTracker) (EntryState, error)
}
