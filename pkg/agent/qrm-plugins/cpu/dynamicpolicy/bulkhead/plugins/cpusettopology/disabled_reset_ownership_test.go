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

package cpusettopology

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ownershipFixture wires a cgroup-v1 96-CPU tree that hosts a long-lived dedicated
// subset pod. The pause pod cgroup and the main app container leaf both start on
// the dedicated subset (CPUs 0-7); the reset must never clobber the main leaf
// with the whole-machine cpuset, and a startup/restart that never entered an
// enabled epoch must not touch either leaf at all.
type ownershipFixture struct {
	p           *CPUSetTopologyPlugin
	cg          *fakeCgroupClient
	in          bulkheadapi.HandlerContext
	pauseRel    string
	mainRel     string
	mainCPUs    machine.CPUSet
	machineCPUs machine.CPUSet
}

func newOwnershipFixture(t *testing.T, preserve bool) ownershipFixture {
	t.Helper()

	const (
		podUID      = "ownership-dedicated-pod"
		containerID = "ownership-dedicated-container"
	)
	mainRel := "primary/dedicated/pod-x/container-main"
	pauseRel := "primary/dedicated/pod-x"
	registerRelativeCgroupPathHandlerForTest(t, cgcommonRelativeCgroupHandler(podUID, containerID, mainRel))

	machineCPUs := machine.MustParse("0-95")
	nonReclaim := machine.MustParse("0-47")
	reclaim := machine.MustParse("48-95")
	mainCPUs := machine.MustParse("0-7")

	cg := &fakeCgroupClient{
		version: cgroupclient.CgroupVersionV1,
		existing: map[string]bool{
			"primary":           true,
			"primary/dedicated": true,
			pauseRel:            true,
			mainRel:             true,
			"reclaim":           true,
			"reclaim/reclaim-0": true,
		},
		cpus: map[string]machine.CPUSet{
			"primary":           nonReclaim.Clone(),
			"primary/dedicated": nonReclaim.Clone(),
			pauseRel:            mainCPUs.Clone(),
			mainRel:             mainCPUs.Clone(),
			"reclaim":           reclaim.Clone(),
			"reclaim/reclaim-0": reclaim.Clone(),
		},
		children: map[string][]string{
			"":                  {"primary", "reclaim"},
			"primary":           {"dedicated"},
			"primary/dedicated": {"pod-x"},
			pauseRel:            {"container-main"},
			"reclaim":           {"reclaim-0"},
		},
	}
	p := &CPUSetTopologyPlugin{
		cfg:    bulkheadconfigForOwnership(preserve),
		cgroup: cg,
	}
	topology := &machine.CPUTopology{CPUDetails: make(machine.CPUDetails, 96)}
	for cpu := 0; cpu < 96; cpu++ {
		topology.CPUDetails[cpu] = machine.CPUTopoInfo{}
	}
	in := bulkheadapi.HandlerContext{
		CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
			MetaServer: ownedMetaServer(podUID, containerID),
			Topology:   topology,
		},
		DesiredView: &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
			NonReclaimPool:          nonReclaim,
			ReclaimEffective:        reclaim,
			ReclaimEffectivePerNUMA: map[int]machine.CPUSet{0: reclaim},
			ContainerCPUSetByPod: map[string]map[string]machine.CPUSet{
				podUID: {"main": mainCPUs},
			},
		}},
	}
	return ownershipFixture{
		p: p, cg: cg, in: in,
		pauseRel: pauseRel, mainRel: mainRel,
		mainCPUs: mainCPUs, machineCPUs: machineCPUs,
	}
}

func bulkheadconfigForOwnership(preserve bool) bulkheadconfig.BulkheadConfiguration {
	return bulkheadconfig.BulkheadConfiguration{
		BulkheadPrimaryRelPath:                    "primary",
		BulkheadReclaimRelPaths:                   []string{"reclaim"},
		BulkheadReclaimNumaPrefixes:               []string{"reclaim/reclaim-"},
		PreserveReclaimCPUSetWhenTopologyDisabled: preserve,
	}
}

// enabledOwnershipContext returns a HandlerContext whose dynamic config enables
// the full topology (so Enable reports true).
func enabledOwnershipContext(base bulkheadapi.HandlerContext) bulkheadapi.HandlerContext {
	base.DynamicConf = enabledBulkheadCpusetTopologyDynamicConf()
	return base
}

func ownedMetaServer(podUID, containerID string) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: []*v1.Pod{{
				ObjectMeta: metav1.ObjectMeta{UID: types.UID(podUID)},
				Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
				Status: v1.PodStatus{ContainerStatuses: []v1.ContainerStatus{{
					Name:        "main",
					ContainerID: "containerd://" + containerID,
				}}},
			}}},
		},
	}
}

func cgcommonRelativeCgroupHandler(podUID, containerID, rel string) cgcommon.RelativeCgroupPathHandler {
	return cgcommon.RelativeCgroupPathHandler{
		Name: "ownership-proof-" + podUID,
		Handler: func(gotPodUID, gotContainerID string) (string, bool, error) {
			if gotPodUID == podUID && gotContainerID == containerID {
				return rel, false, nil
			}
			return "", true, nil
		},
	}
}

// TestStartupDisabledRequiresNoResetAndLeavesDedicatedLeavesUntouched pins the
// ownership fix: a plugin that has never entered an enabled epoch (process start,
// or restart without in-memory ownership proof) must NOT be treated as an
// enabled->disabled exit transition. NeedsDisabledReset stays false, so the
// manager never runs the bulk reset and the long-lived dedicated subset Pod,
// pause, and main-container leaves are byte-for-byte untouched.
func TestStartupDisabledRequiresNoResetAndLeavesDedicatedLeavesUntouched(t *testing.T) {
	t.Parallel()

	for _, preserve := range []bool{false, true} {
		preserve := preserve
		t.Run(boolName(preserve, "preserve_on", "preserve_off"), func(t *testing.T) {
			t.Parallel()
			fx := newOwnershipFixture(t, preserve)

			// Startup: the plugin reports disabled on a v1 host (no enabled epoch).
			if got := fx.p.Enable(fx.in); got {
				t.Fatalf("Enable() = true on startup, want false")
			}
			// Even with reclaim-preservation engaged, there is no ownership proof.
			if got := fx.p.ShouldReconcileWhenDisabled(context.Background(), fx.in); got != preserve {
				t.Fatalf("ShouldReconcileWhenDisabled() = %t, want %t", got, preserve)
			}
			if fx.p.NeedsDisabledReset() {
				t.Fatalf("NeedsDisabledReset() = true on startup without ownership proof, want false")
			}

			// Mirror the manager decision: the disabled reset handler runs ONLY when
			// NeedsDisabledReset() reports pending. With no ownership proof the gate
			// stays closed, so the bulk reset is never invoked and the leaves are
			// byte-for-byte untouched.
			if fx.p.NeedsDisabledReset() {
				if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
					t.Fatalf("disabled handler returned unexpected error: %v", err)
				}
				fx.p.MarkDisabledResetComplete()
			}
			if len(fx.cg.writes) != 0 {
				t.Fatalf("startup-disabled reset mutated cgroups, writes=%#v", fx.cg.writes)
			}
			// The long-lived dedicated leaves are unchanged.
			if got := fx.cg.cpus[fx.pauseRel]; !got.Equals(fx.mainCPUs) {
				t.Fatalf("pause cpuset = %s, want untouched %s", got.String(), fx.mainCPUs.String())
			}
			if got := fx.cg.cpus[fx.mainRel]; !got.Equals(fx.mainCPUs) {
				t.Fatalf("main container cpuset = %s, want untouched %s", got.String(), fx.mainCPUs.String())
			}
		})
	}
}

// TestGenuineEnabledToDisabledTransitionResetsOwnedRootsButNotExternalLeaves pins
// the true-transition contract: once the plugin has actually owned the topology (an
// enabled epoch), the disabled exit runs the authoritative reset. Only the
// controlled topology-owned pool roots expand to the machine target. External
// dedicated/shared/RNB Pod and pause cgroups are NOT topology-owned and are never
// fanned out to the machine target; the proven main-container leaf converges to its
// canonical DesiredView allocation (0-7), never the whole machine (0-95).
func TestGenuineEnabledToDisabledTransitionResetsOwnedRootsButNotExternalLeaves(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)

	if got := fx.p.Enable(enabledOwnershipContext(fx.in)); !got {
		t.Fatalf("Enable() = false, want true during the owned epoch")
	}
	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true after exit, want false")
	}
	if !fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = false after genuine enabled->disabled, want true")
	}

	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	machine := fx.machineCPUs.String()
	// Controlled topology-owned roots expand to the machine target.
	for _, rel := range []string{"primary", "reclaim", "reclaim/reclaim-0"} {
		if got := fx.cg.writes[rel]; got != machine {
			t.Fatalf("reset cpuset @ %s = %q, want machine %q; writes=%#v", rel, got, machine, fx.cg.writes)
		}
	}
	// External pod cgroup dir and the pause cgroup are not topology-owned: they
	// must not receive the machine reset target (the writes map records every
	// cpuset.cpus write; absence here means the leaf was never touched).
	for _, rel := range []string{"primary/dedicated", fx.pauseRel} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("external pod/pause cgroup @ %s must not receive machine reset write; writes=%#v", rel, fx.cg.writes)
		}
	}
	// The proven main-container leaf converges to its canonical DesiredView
	// allocation and is never bulk-written to the whole machine.
	if got := fx.cg.writes[fx.mainRel]; got != fx.mainCPUs.String() {
		t.Fatalf("main container reset cpuset = %q, want canonical DesiredView %q; writes=%#v",
			got, fx.mainCPUs.String(), fx.cg.writes)
	}

	fx.p.MarkDisabledResetComplete()
	if fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = true after MarkDisabledResetComplete, want false")
	}
}

// TestGenuineTransitionPreserveModeExcludesReclaimAndExternalLeaves pins the
// false-reclaim (preserve) semantics: the controlled primary root resets to the
// machine target, but no reset write may land on the reclaim subtree OR on the
// external pod/pause leaves.
func TestGenuineTransitionPreserveModeExcludesReclaimAndExternalLeaves(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, true)

	if got := fx.p.Enable(enabledOwnershipContext(fx.in)); !got {
		t.Fatalf("Enable() = false, want true during the owned epoch")
	}
	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true after exit, want false")
	}
	if !fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = false after genuine transition, want true")
	}

	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	machine := fx.machineCPUs.String()
	if got := fx.cg.writes["primary"]; got != machine {
		t.Fatalf("reset cpuset @ primary = %q, want machine %q; writes=%#v", got, machine, fx.cg.writes)
	}
	for _, rel := range []string{"primary/dedicated", fx.pauseRel} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("external pod/pause cgroup @ %s must not receive machine reset write; writes=%#v", rel, fx.cg.writes)
		}
	}
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0"} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("preserve-mode reset must not write reclaim subtree @ %s; writes=%#v", rel, fx.cg.writes)
		}
	}
	// Proven main leaf keeps its canonical subset.
	if got := fx.cg.writes[fx.mainRel]; got != fx.mainCPUs.String() {
		t.Fatalf("main container reset cpuset = %q, want canonical %q; writes=%#v",
			got, fx.mainCPUs.String(), fx.cg.writes)
	}
}

// TestInterruptedDisabledResetRetriesUntilMarkedComplete pins the interrupted-reset
// retry contract: unless the manager commits MarkDisabledResetComplete inside the
// generation fence, NeedsDisabledReset stays true across rounds so the reset is
// retried rather than silently dropped.
func TestInterruptedDisabledResetRetriesUntilMarkedComplete(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)

	if got := fx.p.Enable(enabledOwnershipContext(fx.in)); !got {
		t.Fatalf("Enable() = false during owned epoch, want true")
	}
	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true after exit, want false")
	}
	// Round one: the reset is required.
	if !fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = false before first reset attempt, want true")
	}
	// Interrupted: the handler runs but the fence never commits completion.
	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("interrupted reset attempt: %v", err)
	}
	// No MarkDisabledResetComplete => still pending for the next round (retry).
	if !fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = false after an uncommitted reset, want true (retry)")
	}
	// A successful committed completion clears the pending reset.
	fx.p.MarkDisabledResetComplete()
	if fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = true after committed completion, want false")
	}
}

func boolName(v bool, whenTrue, whenFalse string) string {
	if v {
		return whenTrue
	}
	return whenFalse
}

// TestGenuineTransitionNeverWritesRNBLeavesUnderReclaim pins that even in a genuine
// enabled->disabled transition, any RNB pod/pause/main leaves that happen to sit
// under the reclaim subtree are NOT topology-owned and must never inherit the
// machine/parent reset target. Reclaim semantics are preserved only through the
// controlled reclaim root/bucket nodes and (in preserve mode) the traversal
// boundary; dynamic RNB descendants are never fanned out.
func TestGenuineTransitionNeverWritesRNBLeavesUnderReclaim(t *testing.T) {
	t.Parallel()

	for _, preserve := range []bool{false, true} {
		preserve := preserve
		t.Run(boolName(preserve, "preserve_on", "preserve_off"), func(t *testing.T) {
			t.Parallel()
			fx := newOwnershipFixture(t, preserve)
			// An RNB pod + pause + main leaf nested under the reclaim bucket.
			rnbPause := "reclaim/reclaim-0/rnb-pod"
			rnbMain := "reclaim/reclaim-0/rnb-pod/container-rnb"
			fx.cg.existing[rnbPause] = true
			fx.cg.existing[rnbMain] = true
			fx.cg.cpus[rnbPause] = machine.MustParse("48")
			fx.cg.cpus[rnbMain] = machine.MustParse("48")
			fx.cg.children["reclaim/reclaim-0"] = []string{"rnb-pod"}
			fx.cg.children[rnbPause] = []string{"container-rnb"}

			if got := fx.p.Enable(enabledOwnershipContext(fx.in)); !got {
				t.Fatalf("Enable() = false during owned epoch, want true")
			}
			if got := fx.p.Enable(fx.in); got {
				t.Fatalf("Enable() = true after exit, want false")
			}
			if !fx.p.NeedsDisabledReset() {
				t.Fatalf("NeedsDisabledReset() = false after genuine transition, want true")
			}
			if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
				t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
			}
			// The RNB pause/main leaves never receive the machine/parent target.
			for _, rel := range []string{rnbPause, rnbMain} {
				if got, ok := fx.cg.writes[rel]; ok {
					t.Fatalf("RNB leaf @ %s received reset write %q; want untouched; writes=%#v", rel, got, fx.cg.writes)
				}
			}
		})
	}
}

// TestStaleCheckpointContainerIdentityReceivesNoWrite pins the fail-closed contract
// for stale checkpoint / container identity: when the DesiredView names a container
// that the strict fresh pod snapshot no longer proves (pod gone, or container
// generation rotated), buildExpectedCPUSetByRel must not bind it as an expected
// leaf. The disabled reset therefore has no canonical target for that leaf and —
// because it sits under the primary pool — must never stamp the machine reset target
// onto it. The stale leaf receives no write at all.
func TestStaleCheckpointContainerIdentityReceivesNoWrite(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	// The checkpoint still names the dedicated pod, but the strict fresh pod
	// snapshot no longer proves it (pod removed / container generation rotated): the
	// lifecycle classification marks the allocation stale and excludes it from the
	// expected-leaf proof.
	fx.in.MetaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: nil}

	if got := fx.p.Enable(enabledOwnershipContext(fx.in)); !got {
		t.Fatalf("Enable() = false during owned epoch, want true")
	}
	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true after exit, want false")
	}
	if !fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = false after genuine transition, want true")
	}

	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	// Controlled roots still reset to the machine target.
	if got := fx.cg.writes["primary"]; got != fx.machineCPUs.String() {
		t.Fatalf("primary reset = %q, want machine %q; writes=%#v", got, fx.machineCPUs.String(), fx.cg.writes)
	}
	// The stale checkpoint leaf and its pause pod are not proven: they must not be
	// written at all (no canonical target, no parent machine fanout).
	for _, rel := range []string{"primary/dedicated", fx.pauseRel, fx.mainRel} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("stale/unproven leaf @ %s received a reset write; writes=%#v", rel, fx.cg.writes)
		}
	}
}

// TestCgroupV2NoOptInDoesNotCertifyUnperformedReset pins the cgroup-v2 no-op
// completion semantics: on a v2 host without the explicit opt-in, the plugin is
// inert. Enable returns false and hasOwnedTopology is never set, so
// NeedsDisabledReset stays false even if the completion hook is invoked. The
// disabled reset handler must not touch any cgroup; completion cannot be certified
// for an unperformed reset.
func TestCgroupV2NoOptInDoesNotCertifyUnperformedReset(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	fx.cg.version = cgroupclient.CgroupVersionV2
	fx.p.cfg.EnableBulkheadCpusetTopologyOnCgroupV2 = false

	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true on v2 without opt-in, want false")
	}
	if fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = true on inert v2, want false")
	}
	// Even if the manager unconditionally calls the completion hook, without an
	// ownership epoch the reset was never due; completion must not certify an
	// unperformed reset into a done state.
	fx.p.MarkDisabledResetComplete()
	if fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = true after MarkDisabledResetComplete on inert v2, want false")
	}
	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("disabled handler on v2 returned error: %v", err)
	}
	if len(fx.cg.writes) != 0 {
		t.Fatalf("inert v2 reset mutated cgroups, writes=%#v", fx.cg.writes)
	}
}
