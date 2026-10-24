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
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type fakeHierarchyNode struct {
	identity       CgroupIdentity
	cpus           machine.CPUSet
	configuredCPUs machine.CPUSet
	mems           string
	configuredMems string
	activity       CgroupActivity
}

type fakeHierarchyWrite struct {
	rel      string
	identity CgroupIdentity
	cpus     machine.CPUSet
	mems     string
}

type fakeHierarchyState map[string]fakeHierarchyNode

type fakeHierarchyTrace struct {
	write  fakeHierarchyWrite
	before fakeHierarchyState
	after  fakeHierarchyState
}

type fakeHierarchyInvariant func(fakeHierarchyTrace) error

type fakeHierarchyDriver struct {
	nodes                       map[string]*fakeHierarchyNode
	roots                       []string
	writes                      []fakeHierarchyWrite
	traces                      []fakeHierarchyTrace
	invariants                  []fakeHierarchyInvariant
	exclusiveDomains            []string
	drainingDomains             []string
	witnessAuthorizedExpansions map[string]machine.CPUSet
	allowUnwitnessedExpansion   bool
	closeCalls                  int
	calls                       int
	beforeCall                  func(HierarchyOperation, string) error
	writeHook                   func(*fakeHierarchyDriver, fakeHierarchyWrite) error
	stableIdentity              bool
	capabilities                HierarchyCapabilities
}

func newFakeHierarchyDriver() *fakeHierarchyDriver {
	fake := &fakeHierarchyDriver{
		nodes:          make(map[string]*fakeHierarchyNode),
		stableIdentity: true,
	}
	fake.invariants = []fakeHierarchyInvariant{
		subsetInvariant,
		fake.exclusiveInvariant,
		fake.drainMonotonicInvariant,
		fake.witnessExpandInvariant,
	}
	return fake
}

func (f *fakeHierarchyDriver) add(rel string, identity CgroupIdentity, cpus, mems string) {
	f.nodes[rel] = &fakeHierarchyNode{
		identity:       identity,
		cpus:           machine.MustParse(cpus),
		configuredCPUs: machine.MustParse(cpus),
		mems:           mems,
		configuredMems: mems,
	}
	if filepath.Dir(rel) == "." {
		f.roots = append(f.roots, rel)
		sort.Strings(f.roots)
	}
}

func (f *fakeHierarchyDriver) markInactive(rel string) {
	node := f.nodes[rel]
	if node == nil {
		panic(fmt.Sprintf("mark inactive missing rel %q", rel))
	}
	node.activity.TasksEmpty = true
	node.activity.CgroupProcsEmpty = true
}

func (f *fakeHierarchyDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
) (EntryState, error) {
	node := f.nodes[rel]
	if node == nil {
		return EntryState{}, syscall.ENOENT
	}
	if node.identity != expected {
		return EntryState{}, ErrCgroupIdentityChanged
	}
	before := f.directChildren(rel)
	entry, err := f.ReadEntry(ctx, rel)
	if err != nil {
		return EntryState{}, err
	}
	if entry.Identity != expected {
		return EntryState{}, ErrCgroupIdentityChanged
	}
	entry.Activity = f.nodes[rel].activity
	after := f.directChildren(rel)
	if ChildrenFingerprint(before) != ChildrenFingerprint(after) {
		return EntryState{}, ErrCgroupIdentityChanged
	}
	entry.Activity.Childless = len(before) == 0
	return entry, nil
}

func (f *fakeHierarchyDriver) directChildren(rel string) []ChildRef {
	children := make([]ChildRef, 0)
	for candidate, node := range f.nodes {
		if filepath.Dir(candidate) == rel {
			children = append(children, ChildRef{Name: filepath.Base(candidate), Identity: node.identity})
		}
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name < children[j].Name })
	return children
}

func (f *fakeHierarchyDriver) bumpIdentity(rel string) {
	f.nodes[rel].identity.Inode++
}

func (f *fakeHierarchyDriver) Close() error {
	f.closeCalls++
	return nil
}

func (f *fakeHierarchyDriver) called(op HierarchyOperation, rel string) error {
	f.calls++
	if f.beforeCall != nil {
		return f.beforeCall(op, rel)
	}
	return nil
}

func (f *fakeHierarchyDriver) Roots(context.Context) ([]RootRef, error) {
	if err := f.called(HierarchyOperationRoots, ""); err != nil {
		return nil, err
	}
	roots := make([]RootRef, 0, len(f.roots))
	for _, rel := range f.roots {
		roots = append(roots, RootRef{Rel: rel, Identity: f.nodes[rel].identity})
	}
	return roots, nil
}

func (f *fakeHierarchyDriver) StatIdentity(_ context.Context, rel string) (CgroupIdentity, error) {
	if err := f.called(HierarchyOperationStat, rel); err != nil {
		return CgroupIdentity{}, err
	}
	node := f.nodes[rel]
	if node == nil {
		return CgroupIdentity{}, syscall.ENOENT
	}
	return node.identity, nil
}

func (f *fakeHierarchyDriver) ReadEntry(_ context.Context, rel string) (EntryState, error) {
	if err := f.called(HierarchyOperationRead, rel); err != nil {
		return EntryState{}, err
	}
	node := f.nodes[rel]
	if node == nil {
		return EntryState{}, syscall.ENOENT
	}
	return EntryState{
		Rel: rel, Identity: node.identity,
		CPUs: node.cpus.Clone(), ConfiguredCPUs: node.configuredCPUs.Clone(),
		Mems: node.mems, ConfiguredMems: node.configuredMems,
	}, nil
}

func (f *fakeHierarchyDriver) ListChildren(_ context.Context, rel string) ([]ChildRef, error) {
	if err := f.called(HierarchyOperationList, rel); err != nil {
		return nil, err
	}
	if f.nodes[rel] == nil {
		return nil, syscall.ENOENT
	}
	return f.directChildren(rel), nil
}

func (f *fakeHierarchyDriver) WriteCPUs(_ context.Context, rel string, expected CgroupIdentity, cpus machine.CPUSet) error {
	if err := f.called(HierarchyOperationWriteCPUs, rel); err != nil {
		return err
	}
	node := f.nodes[rel]
	if node == nil {
		return syscall.ENOENT
	}
	if node.identity != expected {
		return ErrCgroupIdentityChanged
	}
	write := fakeHierarchyWrite{rel: rel, identity: expected, cpus: cpus.Clone(), mems: node.mems}
	before := f.snapshot()
	if f.writeHook != nil {
		if err := f.writeHook(f, write); err != nil {
			return err
		}
	}
	current := f.nodes[rel]
	if current != node || current == nil || current.identity != expected {
		return ErrCgroupIdentityChanged
	}
	trace := f.nextTrace(write, before)
	effective := cpus.Clone()
	if f.capabilities.EmptyConfiguredCPUSet && cpus.IsEmpty() {
		if parent := f.nodes[filepath.Dir(rel)]; parent != nil {
			effective = parent.cpus.Clone()
		} else {
			effective = current.cpus.Clone()
		}
	}
	trace.after[rel] = fakeHierarchyNode{
		identity: current.identity, cpus: effective.Clone(),
		configuredCPUs: cpus.Clone(), mems: current.mems,
		configuredMems: current.configuredMems,
		activity:       current.activity,
	}
	if err := f.checkInvariants(trace); err != nil {
		return err
	}
	current.configuredCPUs = cpus.Clone()
	current.cpus = effective
	f.writes = append(f.writes, write)
	f.traces = append(f.traces, trace)
	return nil
}

func (f *fakeHierarchyDriver) WriteMems(_ context.Context, rel string, expected CgroupIdentity, mems string) error {
	if err := f.called(HierarchyOperationWriteMems, rel); err != nil {
		return err
	}
	node := f.nodes[rel]
	if node == nil {
		return syscall.ENOENT
	}
	if node.identity != expected {
		return ErrCgroupIdentityChanged
	}
	write := fakeHierarchyWrite{rel: rel, identity: expected, cpus: node.cpus.Clone(), mems: mems}
	before := f.snapshot()
	if f.writeHook != nil {
		if err := f.writeHook(f, write); err != nil {
			return err
		}
	}
	current := f.nodes[rel]
	if current != node || current == nil || current.identity != expected {
		return ErrCgroupIdentityChanged
	}
	trace := f.nextTrace(write, before)
	effective := mems
	if f.capabilities.EmptyConfiguredCPUSet && mems == "" {
		if parent := f.nodes[filepath.Dir(rel)]; parent != nil {
			effective = parent.mems
		} else {
			effective = current.mems
		}
	}
	trace.after[rel] = fakeHierarchyNode{
		identity: current.identity, cpus: current.cpus.Clone(),
		configuredCPUs: current.configuredCPUs.Clone(), mems: effective,
		configuredMems: mems,
		activity:       current.activity,
	}
	if err := f.checkInvariants(trace); err != nil {
		return err
	}
	current.configuredMems = mems
	current.mems = effective
	f.writes = append(f.writes, write)
	f.traces = append(f.traces, trace)
	return nil
}

// PhysicalWriteCount reports how many physical CPU or memory writes have been
// committed against the live driver. Trace compilation must keep this at zero.
func (f *fakeHierarchyDriver) PhysicalWriteCount() int {
	return len(f.writes)
}

func (f *fakeHierarchyDriver) snapshot() fakeHierarchyState {
	state := make(fakeHierarchyState, len(f.nodes))
	for rel, node := range f.nodes {
		state[rel] = fakeHierarchyNode{
			identity:       node.identity,
			cpus:           node.cpus.Clone(),
			configuredCPUs: node.configuredCPUs.Clone(),
			mems:           node.mems,
			configuredMems: node.configuredMems,
			activity:       node.activity,
		}
	}
	return state
}

func (f *fakeHierarchyDriver) nextTrace(write fakeHierarchyWrite, before fakeHierarchyState) fakeHierarchyTrace {
	return fakeHierarchyTrace{
		write:  write,
		before: before,
		after:  f.snapshot(),
	}
}

func (f *fakeHierarchyDriver) checkInvariants(trace fakeHierarchyTrace) error {
	for _, invariant := range f.invariants {
		if err := invariant(trace); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeHierarchyDriver) exclusiveInvariant(trace fakeHierarchyTrace) error {
	return exclusiveInvariant(f.exclusiveDomains...)(trace)
}

func (f *fakeHierarchyDriver) drainMonotonicInvariant(trace fakeHierarchyTrace) error {
	return drainMonotonicInvariant(f.drainingDomains...)(trace)
}

func (f *fakeHierarchyDriver) witnessExpandInvariant(trace fakeHierarchyTrace) error {
	if f.allowUnwitnessedExpansion {
		return nil
	}
	return witnessExpandInvariant(f.witnessAuthorizedExpansions)(trace)
}

func (f *fakeHierarchyDriver) Classify(err error, _ HierarchyOperation) HierarchyErrorClass {
	switch {
	case err == nil:
		return HierarchyErrorNone
	case errors.Is(err, ErrCgroupIdentityChanged), errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENOTDIR), errors.Is(err, syscall.EBUSY):
		return HierarchyErrorStale
	default:
		return HierarchyErrorInvalid
	}
}

func (f *fakeHierarchyDriver) Capabilities() HierarchyCapabilities {
	capabilities := f.capabilities
	capabilities.StableIdentity = f.stableIdentity
	capabilities.KernelParentContainment = true
	return capabilities
}

func TestFakeHierarchyV2ReportsCapabilitiesAndKeepsInheritedEffectiveCPUs(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.capabilities = cgroupV2Policy.capabilities(true)
	fake.allowUnwitnessedExpansion = true
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.add("root/leaf", CgroupIdentity{Device: 1, Inode: 2}, "1-2", "0")

	if err := fake.WriteCPUs(
		context.Background(), "root/leaf", fake.nodes["root/leaf"].identity, machine.NewCPUSet(),
	); err != nil {
		t.Fatalf("WriteCPUs(empty) error = %v", err)
	}
	entry, err := fake.ReadEntry(context.Background(), "root/leaf")
	if err != nil {
		t.Fatalf("ReadEntry() error = %v", err)
	}
	if !fake.Capabilities().EmptyConfiguredCPUSet || !fake.Capabilities().EffectiveCPUSet {
		t.Fatalf("v2 capabilities = %+v, want configured-empty and effective semantics", fake.Capabilities())
	}
	if !entry.ConfiguredCPUs.IsEmpty() || !entry.CPUs.Equals(machine.NewCPUSet(0, 1, 2, 3)) {
		t.Fatalf("configured/effective = %s/%s, want empty/0-3 inheritance",
			entry.ConfiguredCPUs.String(), entry.CPUs.String())
	}
}

func TestFakeHierarchyIdentityRevisionInvalidatesOldPlan(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 10}, "0-3", "0")
	oldIdentity := fake.nodes["root"].identity
	fake.bumpIdentity("root")

	err := fake.WriteCPUs(context.Background(), "root", oldIdentity, machine.MustParse("0-2"))
	if !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("WriteCPUs() error = %v, want identity changed", err)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("writes = %v, want none", fake.writes)
	}
}

func TestFakeHierarchyDefaultsToAllPostWriteInvariants(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	if got := len(fake.invariants); got != 4 {
		t.Fatalf("default invariant count = %d, want 4", got)
	}
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")

	err := fake.WriteCPUs(context.Background(), "root", fake.nodes["root"].identity, machine.MustParse("0-1"))
	if err == nil {
		t.Fatal("default invariants allowed parent shrink below child")
	}
}

func TestFakeHierarchyDefaultInvariantFailures(t *testing.T) {
	t.Parallel()
	t.Run("subset", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
		fake.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")

		err := fake.WriteCPUs(context.Background(), "root", fake.nodes["root"].identity, machine.MustParse("0-1"))
		if err == nil || !strings.Contains(err.Error(), "outside parent") {
			t.Fatalf("WriteCPUs() error = %v, want subset invariant failure", err)
		}
	})

	t.Run("exclusive", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.exclusiveDomains = []string{"primary", "reclaim"}
		fake.witnessAuthorizedExpansions = map[string]machine.CPUSet{
			"reclaim": machine.MustParse("1"),
		}
		fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
		fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")

		err := fake.WriteCPUs(context.Background(), "reclaim", fake.nodes["reclaim"].identity, machine.MustParse("1-3"))
		if err == nil || !strings.Contains(err.Error(), "exclusive domains") {
			t.Fatalf("WriteCPUs() error = %v, want exclusive invariant failure", err)
		}
	})

	t.Run("drain-monotonic", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.drainingDomains = []string{"source"}
		fake.witnessAuthorizedExpansions = map[string]machine.CPUSet{
			"source": machine.MustParse("2"),
		}
		fake.add("source", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")

		err := fake.WriteCPUs(context.Background(), "source", fake.nodes["source"].identity, machine.MustParse("0-2"))
		if err == nil || !strings.Contains(err.Error(), "drain expanded") {
			t.Fatalf("WriteCPUs() error = %v, want drain invariant failure", err)
		}
	})

	t.Run("witness-expand", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.witnessAuthorizedExpansions = map[string]machine.CPUSet{
			"destination": machine.MustParse("1"),
		}
		fake.add("destination", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")

		err := fake.WriteCPUs(context.Background(), "destination", fake.nodes["destination"].identity, machine.MustParse("0-2"))
		if err == nil || !strings.Contains(err.Error(), "exceeds witness 1") {
			t.Fatalf("WriteCPUs() error = %v, want witness invariant failure", err)
		}
	})
}

func TestFakeHierarchyDefaultInvariantLegalWrites(t *testing.T) {
	t.Parallel()
	t.Run("subset", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
		fake.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "0-2", "0")

		if err := fake.WriteCPUs(context.Background(), "root/child", fake.nodes["root/child"].identity, machine.MustParse("1-2")); err != nil {
			t.Fatalf("legal subset write failed: %v", err)
		}
	})

	t.Run("exclusive", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.exclusiveDomains = []string{"primary", "reclaim"}
		fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
		fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")

		if err := fake.WriteCPUs(context.Background(), "reclaim", fake.nodes["reclaim"].identity, machine.MustParse("3")); err != nil {
			t.Fatalf("legal exclusive write failed: %v", err)
		}
	})

	t.Run("drain-monotonic", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.drainingDomains = []string{"source"}
		fake.add("source", CgroupIdentity{Device: 1, Inode: 1}, "0-2", "0")

		if err := fake.WriteCPUs(context.Background(), "source", fake.nodes["source"].identity, machine.MustParse("0-1")); err != nil {
			t.Fatalf("legal drain write failed: %v", err)
		}
	})

	t.Run("witness-expand", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.witnessAuthorizedExpansions = map[string]machine.CPUSet{
			"destination": machine.MustParse("1"),
		}
		fake.add("destination", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
		if err := fake.WriteCPUs(context.Background(), "destination", fake.nodes["destination"].identity, machine.MustParse("0-1")); err != nil {
			t.Fatalf("witness-authorized expansion failed: %v", err)
		}

		unconfigured := newFakeHierarchyDriver()
		unconfigured.add("unconfigured", CgroupIdentity{Device: 1, Inode: 2}, "2", "0")
		if err := unconfigured.WriteCPUs(context.Background(), "unconfigured", unconfigured.nodes["unconfigured"].identity, machine.MustParse("2-3")); err == nil {
			t.Fatal("unwitnessed expansion succeeded without explicit disabled mode")
		}

		explicitlyDisabled := newFakeHierarchyDriver()
		explicitlyDisabled.allowUnwitnessedExpansion = true
		explicitlyDisabled.add("disabled", CgroupIdentity{Device: 1, Inode: 3}, "4", "0")
		if err := explicitlyDisabled.WriteCPUs(context.Background(), "disabled", explicitlyDisabled.nodes["disabled"].identity, machine.MustParse("4-5")); err != nil {
			t.Fatalf("explicitly disabled witness mode rejected expansion: %v", err)
		}
	})
}

func TestBudgetedHierarchyDriverCloseForwardsExactlyOnce(t *testing.T) {
	t.Parallel()
	base := newFakeHierarchyDriver()
	driver := NewBudgetedHierarchyDriver(base, NewBudgetTracker(ConvergenceBudget{}))

	if err := driver.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := driver.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if base.closeCalls != 1 {
		t.Fatalf("underlying Close() calls = %d, want 1", base.closeCalls)
	}
}

func TestFakeHierarchyRejectsReplacementDuringWriteHook(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.writeHook = func(f *fakeHierarchyDriver, _ fakeHierarchyWrite) error {
		f.nodes["root"] = &fakeHierarchyNode{
			identity: CgroupIdentity{Device: 1, Inode: 2},
			cpus:     machine.MustParse("4-7"),
			mems:     "1",
		}
		return nil
	}

	err := fake.WriteCPUs(context.Background(), "root", CgroupIdentity{Device: 1, Inode: 1}, machine.MustParse("0-2"))
	if !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("WriteCPUs() error = %v, want identity changed", err)
	}
	if got := fake.nodes["root"].cpus.String(); got != "4-7" {
		t.Fatalf("replacement cpus = %q, want untouched 4-7", got)
	}
	if len(fake.writes) != 0 || len(fake.traces) != 0 {
		t.Fatalf("replacement recorded writes=%d traces=%d, want none", len(fake.writes), len(fake.traces))
	}
}

func TestFakeHierarchyCapturesBeforeHookAndValidatesChildChurn(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.writeHook = func(f *fakeHierarchyDriver, _ fakeHierarchyWrite) error {
		f.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")
		return nil
	}

	if err := fake.WriteCPUs(context.Background(), "root", fake.nodes["root"].identity, machine.MustParse("0-2")); err != nil {
		t.Fatalf("legal write with child churn failed: %v", err)
	}
	if len(fake.traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(fake.traces))
	}
	trace := fake.traces[0]
	if _, ok := trace.before["root/child"]; ok {
		t.Fatal("before trace captured child created by hook")
	}
	if child, ok := trace.after["root/child"]; !ok || child.cpus.String() != "0-1" {
		t.Fatalf("after trace child = %+v, present=%v", child, ok)
	}

	fake = newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.writeHook = func(f *fakeHierarchyDriver, _ fakeHierarchyWrite) error {
		f.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")
		return nil
	}
	err := fake.WriteCPUs(context.Background(), "root", fake.nodes["root"].identity, machine.MustParse("0-1"))
	if err == nil {
		t.Fatal("write succeeded despite child created outside target")
	}
	if len(fake.writes) != 0 || len(fake.traces) != 0 {
		t.Fatalf("illegal child churn recorded writes=%d traces=%d, want none", len(fake.writes), len(fake.traces))
	}
}

func hierarchyInvariantHook(f *fakeHierarchyDriver, write fakeHierarchyWrite) error {
	parent := filepath.Dir(write.rel)
	if parent != "." {
		parentNode := f.nodes[parent]
		if parentNode == nil {
			return fmt.Errorf("missing parent %q", parent)
		}
		if !write.cpus.IsSubsetOf(parentNode.cpus) {
			return fmt.Errorf("child %q target %s outside parent %s", write.rel, write.cpus.String(), parentNode.cpus.String())
		}
	}
	for candidate, node := range f.nodes {
		if filepath.Dir(candidate) == write.rel && !node.cpus.IsSubsetOf(write.cpus) {
			return fmt.Errorf("child %q cpus %s outside target %s", candidate, node.cpus.String(), write.cpus.String())
		}
	}
	return nil
}

func subsetInvariant(trace fakeHierarchyTrace) error {
	for rel, node := range trace.after {
		parent := filepath.Dir(rel)
		if parent == "." {
			continue
		}
		parentNode, ok := trace.after[parent]
		if !ok {
			return fmt.Errorf("missing parent %q", parent)
		}
		if !node.cpus.IsSubsetOf(parentNode.cpus) {
			return fmt.Errorf("child %q cpus %s outside parent %s", rel, node.cpus.String(), parentNode.cpus.String())
		}
	}
	return nil
}

func exclusiveInvariant(rels ...string) fakeHierarchyInvariant {
	return func(trace fakeHierarchyTrace) error {
		for i, leftRel := range rels {
			left, ok := trace.after[leftRel]
			if !ok {
				return fmt.Errorf("missing exclusive domain %q", leftRel)
			}
			for _, rightRel := range rels[i+1:] {
				right, ok := trace.after[rightRel]
				if !ok {
					return fmt.Errorf("missing exclusive domain %q", rightRel)
				}
				if overlap := left.cpus.Intersection(right.cpus); !overlap.IsEmpty() {
					return fmt.Errorf("exclusive domains %q and %q overlap on %s", leftRel, rightRel, overlap.String())
				}
			}
		}
		return nil
	}
}

func drainMonotonicInvariant(rels ...string) fakeHierarchyInvariant {
	draining := make(map[string]struct{}, len(rels))
	for _, rel := range rels {
		draining[rel] = struct{}{}
	}
	return func(trace fakeHierarchyTrace) error {
		if _, ok := draining[trace.write.rel]; !ok {
			return nil
		}
		before, beforeOK := trace.before[trace.write.rel]
		after, afterOK := trace.after[trace.write.rel]
		if !beforeOK || !afterOK {
			return fmt.Errorf("missing draining domain %q", trace.write.rel)
		}
		if !after.cpus.IsSubsetOf(before.cpus) {
			return fmt.Errorf("drain expanded %q from %s to %s", trace.write.rel, before.cpus.String(), after.cpus.String())
		}
		return nil
	}
}

func witnessExpandInvariant(witnesses map[string]machine.CPUSet) fakeHierarchyInvariant {
	return func(trace fakeHierarchyTrace) error {
		before, beforeOK := trace.before[trace.write.rel]
		after, afterOK := trace.after[trace.write.rel]
		if !beforeOK || !afterOK {
			return fmt.Errorf("missing expanding domain %q", trace.write.rel)
		}
		authorized, configured := witnesses[trace.write.rel]
		entering := after.cpus.Difference(before.cpus)
		if entering.IsEmpty() {
			return nil
		}
		if !configured {
			return fmt.Errorf("expand %q entering %s has no witness", trace.write.rel, entering.String())
		}
		if !entering.IsSubsetOf(authorized) {
			return fmt.Errorf("expand %q entering %s exceeds witness %s", trace.write.rel, entering.String(), authorized.String())
		}
		return nil
	}
}

func TestFakeHierarchyChecksInvariantBeforeRecordingWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")
	fake.writeHook = hierarchyInvariantHook

	err := fake.WriteCPUs(context.Background(), "root", fake.nodes["root"].identity, machine.MustParse("0-1"))
	if err == nil {
		t.Fatal("WriteCPUs() succeeded despite child-subset violation")
	}
	if len(fake.writes) != 0 {
		t.Fatalf("writes = %v, want none", fake.writes)
	}
}

func TestFakeHierarchyCallHookReturnsError(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	injected := errors.New("injected read failure")
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "root" {
			return injected
		}
		return nil
	}

	if _, err := fake.ReadEntry(context.Background(), "root"); !errors.Is(err, injected) {
		t.Fatalf("ReadEntry() error = %v, want injected error", err)
	}
	if len(fake.traces) != 0 {
		t.Fatalf("traces = %v, want none after failed call", fake.traces)
	}
}

func TestFakeHierarchyListChildrenMissingParent(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()

	children, err := fake.ListChildren(context.Background(), "missing")

	if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("ListChildren() = (%v, %v), want ENOENT", children, err)
	}
}

func TestFakeHierarchyRecordsCompleteTraceAfterWrite(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")

	if err := fake.WriteCPUs(context.Background(), "root/child", fake.nodes["root/child"].identity, machine.MustParse("1")); err != nil {
		t.Fatal(err)
	}
	if len(fake.traces) != 1 {
		t.Fatalf("trace count = %d, want 1", len(fake.traces))
	}
	trace := fake.traces[0]
	if len(trace.before) != 2 || len(trace.after) != 2 {
		t.Fatalf("trace hierarchy sizes = before:%d after:%d, want 2/2", len(trace.before), len(trace.after))
	}
	if got := trace.before["root/child"].cpus.String(); got != "0-1" {
		t.Fatalf("before child cpus = %q, want 0-1", got)
	}
	if got := trace.after["root/child"].cpus.String(); got != "1" {
		t.Fatalf("after child cpus = %q, want 1", got)
	}
	if got := trace.after["root"].cpus.String(); got != "0-3" {
		t.Fatalf("after root cpus = %q, want complete unchanged root", got)
	}
}

func TestFakeHierarchySubsetAndExclusiveInvariants(t *testing.T) {
	t.Parallel()
	t.Run("subset", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
		fake.add("root/child", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")
		fake.invariants = []fakeHierarchyInvariant{subsetInvariant}

		err := fake.WriteCPUs(context.Background(), "root", fake.nodes["root"].identity, machine.MustParse("0-1"))
		if err == nil {
			t.Fatal("parent shrink succeeded despite subset violation")
		}
	})

	t.Run("exclusive", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
		fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "2-3", "0")
		fake.invariants = []fakeHierarchyInvariant{exclusiveInvariant("primary", "reclaim")}

		err := fake.WriteCPUs(context.Background(), "reclaim", fake.nodes["reclaim"].identity, machine.MustParse("1-3"))
		if err == nil {
			t.Fatal("reclaim expansion succeeded despite exclusive overlap")
		}
	})
}

func TestFakeHierarchyDrainMonotonicAndWitnessExpandInvariants(t *testing.T) {
	t.Parallel()
	t.Run("drain-monotonic", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("source", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
		fake.invariants = []fakeHierarchyInvariant{drainMonotonicInvariant("source")}

		err := fake.WriteCPUs(context.Background(), "source", fake.nodes["source"].identity, machine.MustParse("0-2"))
		if err == nil {
			t.Fatal("drain expanded source")
		}
	})

	t.Run("witness-expand", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("destination", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
		fake.invariants = []fakeHierarchyInvariant{witnessExpandInvariant(map[string]machine.CPUSet{
			"destination": machine.MustParse("1"),
		})}

		err := fake.WriteCPUs(context.Background(), "destination", fake.nodes["destination"].identity, machine.MustParse("0-2"))
		if err == nil {
			t.Fatal("destination expanded beyond witness")
		}
	})
}
