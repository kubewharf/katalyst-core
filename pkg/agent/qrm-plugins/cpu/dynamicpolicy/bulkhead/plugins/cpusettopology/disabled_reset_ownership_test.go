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
	stderrors "errors"
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

func isDisabledResetNotConverged(err error) bool {
	var target *disabledResetNotConvergedError
	return stderrors.As(err, &target)
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

// TestStartupDisabledResetsControlledNonLeafRootsButNotLeaves pins the restart
// contract: on a fresh process start (or QRM restart) with topology disabled and
// preserve=false, resetComplete begins false, so the first disabled round performs
// one idempotent reset. The controlled non-leaf DAG nodes (primary, reclaim root,
// reclaim NUMA bucket) return to the whole-machine target. Pod/pause/main-container
// leaves are never written by reset (kubelet converges them). The second round is a
// no-op (idempotent). In preserve mode the reclaim subtree is excluded from the reset.
func TestStartupDisabledResetsControlledNonLeafRootsButNotLeaves(t *testing.T) {
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
			if got := fx.p.ShouldReconcileWhenDisabled(context.Background(), fx.in); got != preserve {
				t.Fatalf("ShouldReconcileWhenDisabled() = %t, want %t", got, preserve)
			}
			// resetComplete starts false: the first disabled round must reset.
			if !fx.p.NeedsDisabledReset() {
				t.Fatalf("NeedsDisabledReset() = false on startup, want true (one idempotent reset due)")
			}

			if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
				t.Fatalf("disabled handler returned unexpected error: %v", err)
			}
			fx.p.MarkDisabledResetComplete()

			machine := fx.machineCPUs.String()
			// Controlled non-leaf nodes reset to the whole-machine target, except in
			// preserve mode where the reclaim subtree is excluded.
			if got := fx.cg.writes["primary"]; got != machine {
				t.Fatalf("reset cpuset @ primary = %q, want machine %q; writes=%#v", got, machine, fx.cg.writes)
			}
			if preserve {
				for _, rel := range []string{"reclaim", "reclaim/reclaim-0"} {
					if _, ok := fx.cg.writes[rel]; ok {
						t.Fatalf("preserve-mode reset must not write reclaim subtree @ %s; writes=%#v", rel, fx.cg.writes)
					}
				}
			} else {
				for _, rel := range []string{"reclaim", "reclaim/reclaim-0"} {
					if got := fx.cg.writes[rel]; got != machine {
						t.Fatalf("reset cpuset @ %s = %q, want machine %q; writes=%#v", rel, got, machine, fx.cg.writes)
					}
				}
			}
			// QoS dirs and the Pod/pause dir are driven to the whole-machine target
			// (kubelet never updates them); only the protected leaf is spared.
			for _, rel := range []string{"primary/dedicated", fx.pauseRel} {
				if got := fx.cg.writes[rel]; got != machine {
					t.Fatalf("reset cpuset @ %s = %q, want machine %q; writes=%#v", rel, got, machine, fx.cg.writes)
				}
			}
			if _, ok := fx.cg.writes[fx.mainRel]; ok {
				t.Fatalf("protected leaf @ %s must not receive a reset write; writes=%#v", fx.mainRel, fx.cg.writes)
			}
			// The protected leaf is byte-for-byte untouched.
			if got := fx.cg.cpus[fx.mainRel]; !got.Equals(fx.mainCPUs) {
				t.Fatalf("main container cpuset = %s, want untouched %s", got.String(), fx.mainCPUs.String())
			}

			// Idempotent: after MarkDisabledResetComplete, a second round is a no-op.
			if fx.p.NeedsDisabledReset() {
				t.Fatalf("NeedsDisabledReset() = true after completion, want false")
			}
			before := len(fx.cg.writes)
			if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
				t.Fatalf("second-round disabled handler: %v", err)
			}
			if len(fx.cg.writes) != before {
				t.Fatalf("second round emitted new writes=%#v", fx.cg.writes)
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
	// The QoS dir and the Pod/pause dir are driven to the machine target (kubelet
	// never updates them).
	for _, rel := range []string{"primary/dedicated", fx.pauseRel} {
		if got := fx.cg.writes[rel]; got != machine {
			t.Fatalf("reset cpuset @ %s = %q, want machine %q; writes=%#v", rel, got, machine, fx.cg.writes)
		}
	}
	// The protected leaf is converged by kubelet UpdateContainerResources and
	// must never be written by the disabled reset.
	if _, ok := fx.cg.writes[fx.mainRel]; ok {
		t.Fatalf("protected leaf @ %s must not receive a reset write; writes=%#v", fx.mainRel, fx.cg.writes)
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
		if got := fx.cg.writes[rel]; got != machine {
			t.Fatalf("reset cpuset @ %s = %q, want machine %q; writes=%#v", rel, got, machine, fx.cg.writes)
		}
	}
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0"} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("preserve-mode reset must not write reclaim subtree @ %s; writes=%#v", rel, fx.cg.writes)
		}
	}
	// The protected leaf is never written by reset (kubelet converges it).
	if _, ok := fx.cg.writes[fx.mainRel]; ok {
		t.Fatalf("protected leaf @ %s must not receive a reset write; writes=%#v", fx.mainRel, fx.cg.writes)
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
			if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil && !isDisabledResetNotConverged(err) {
				t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
			}
			if preserve {
				// preserve mode excludes the whole reclaim subtree.
				if got, ok := fx.cg.writes["reclaim/reclaim-0/rnb-pod"]; ok {
					t.Fatalf("preserve-mode RNB pod dir must be untouched, got write %q; writes=%#v", got, fx.cg.writes)
				}
			} else {
				// Fail-safe matrix: the RNB Pod dir under the reclaim bucket has no
				// positively-identified fresh protected leaf resolving under it
				// (no DesiredView binding), so it must NOT be widened — an
				// unclassifiable dir could be a protected leaf that never
				// self-heals. It stays deferred; a future CRI/container-status positive
				// classifier may widen it once positively confirmed.
				if got, ok := fx.cg.writes["reclaim/reclaim-0/rnb-pod"]; ok {
					t.Fatalf("unclassifiable RNB pod dir must not be widened, got %q; writes=%#v", got, fx.cg.writes)
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

	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil && !isDisabledResetNotConverged(err) {
		t.Fatalf("CPUSetAdjustmentDisabledHandler: %v", err)
	}

	// Controlled roots still reset to the machine target.
	if got := fx.cg.writes["primary"]; got != fx.machineCPUs.String() {
		t.Fatalf("primary reset = %q, want machine %q; writes=%#v", got, fx.machineCPUs.String(), fx.cg.writes)
	}
	// Stale-pod matrix: the QoS dir and Pod/pause dir have no positively-identified
	// fresh protected leaf resolving under them (PodFetcher is empty), so the
	// fail-safe disabled reset must NOT widen them — an unclassifiable dir could be a
	// protected leaf, and a mis-widened protected leaf never self-heals.
	for _, rel := range []string{"primary/dedicated", fx.pauseRel} {
		if got, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("stale/unclassifiable dir @ %s must not be widened, got %q; writes=%#v", rel, got, fx.cg.writes)
		}
	}
}

// TestCgroupV2NoOptInHandlerIsInert pins the cgroup-v2 no-op contract: on a v2
// host without the explicit opt-in, the plugin stays inert. resetComplete starts
// false so NeedsDisabledReset reports pending, but the disabled handler short-
// circuits on the v2 gate and performs zero writes; completion then clears the
// pending reset. The fail-closed allowEmptyTarget protection is untouched.
func TestCgroupV2NoOptInHandlerIsInert(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	fx.cg.version = cgroupclient.CgroupVersionV2
	fx.p.cfg.EnableBulkheadCpusetTopologyOnCgroupV2 = false

	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true on v2 without opt-in, want false")
	}
	if !fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = false on startup, want true (one inert reset due)")
	}
	// The disabled handler short-circuits on the v2 gate: zero cgroup writes.
	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("disabled handler on v2 returned error: %v", err)
	}
	if len(fx.cg.writes) != 0 {
		t.Fatalf("inert v2 reset mutated cgroups, writes=%#v", fx.cg.writes)
	}
	fx.p.MarkDisabledResetComplete()
	if fx.p.NeedsDisabledReset() {
		t.Fatalf("NeedsDisabledReset() = true after MarkDisabledResetComplete, want false")
	}
}

// TestProcessRestartReclaimsNarrowBucketsBackToMachine pins the E2E restart
// contract: process1 (topology=true) wrote a narrow reclaimed-* bucket. After a
// QRM restart, process2 is a brand-new plugin instance with topology=false and
// preserve=false and no in-memory ownership proof. The first disabled round must
// drive the controlled reclaim root/bucket back to the whole-machine target while
// leaves stay untouched, and the second round must be a no-op (idempotent).
func TestProcessRestartReclaimsNarrowBucketsBackToMachine(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)

	// process1 left the reclaim bucket narrowed (e.g. a committed per-NUMA reclaim).
	narrow := machine.MustParse("48,49")
	fx.cg.cpus["reclaim/reclaim-0"] = narrow.Clone()
	fx.cg.cpus["reclaim"] = narrow.Clone()

	// process2: a fresh plugin instance (resetComplete=false, no enabled epoch).
	p2 := &CPUSetTopologyPlugin{cfg: fx.p.cfg, cgroup: fx.cg}
	if got := p2.Enable(fx.in); got {
		t.Fatalf("process2 Enable() = true, want false on disabled host")
	}
	if !p2.NeedsDisabledReset() {
		t.Fatalf("process2 NeedsDisabledReset() = false, want true")
	}

	if err := p2.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("process2 disabled reset: %v", err)
	}
	p2.MarkDisabledResetComplete()

	machine := fx.machineCPUs.String()
	// The controlled reclaim root and bucket return to the whole-machine target.
	for _, rel := range []string{"reclaim", "reclaim/reclaim-0"} {
		if got := fx.cg.writes[rel]; got != machine {
			t.Fatalf("after restart reset @ %s = %q, want machine %q; writes=%#v", rel, got, machine, fx.cg.writes)
		}
	}
	// The Pod/pause dir is driven to the whole-machine target; the managed
	// container leaf is spared.
	if got := fx.cg.writes[fx.pauseRel]; got != machine {
		t.Fatalf("after restart reset @ %s = %q, want machine %q; writes=%#v", fx.pauseRel, got, machine, fx.cg.writes)
	}
	if _, ok := fx.cg.writes[fx.mainRel]; ok {
		t.Fatalf("protected leaf @ %s written by restart reset; writes=%#v", fx.mainRel, fx.cg.writes)
	}
	if got := fx.cg.cpus[fx.mainRel]; !got.Equals(fx.mainCPUs) {
		t.Fatalf("main leaf = %s, want untouched %s", got.String(), fx.mainCPUs.String())
	}

	// Idempotent: second round emits no new writes.
	if p2.NeedsDisabledReset() {
		t.Fatalf("process2 NeedsDisabledReset() = true after completion, want false")
	}
	before := len(fx.cg.writes)
	if err := p2.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("process2 second round: %v", err)
	}
	if len(fx.cg.writes) != before {
		t.Fatalf("second round emitted writes=%#v", fx.cg.writes)
	}
}

// TestNonQRMPodClassifiedAndWidened pins fresh-pod classification: a long-lived
// non-QRM pod (no DesiredView entry) with Status.QOSClass set, a main container and
// an init container whose current and LastTerminationState IDs are prefixed
// containerd://, must have its Pod dir and QoS dir widened to the parent target while
// every container dir (current and lastState) gets zero writes, and the round must
// converge so MarkDisabledResetComplete may be called.
func TestNonQRMPodClassifiedAndWidened(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	// Replace the fixture's dedicated pod with a non-QRM burstable pod.
	const (
		nonQRMUID  = "non-qrm-pod"
		mainID     = "non-qrm-main"
		initID     = "non-qrm-init"
		oldInitID  = "non-qrm-init-old"
		mainRel    = "primary/burstable/pod-x/container-main"
		initRel    = "primary/burstable/pod-x/container-init"
		oldInitRel = "primary/burstable/pod-x/container-init-old"
		podDir     = "primary/burstable/pod-x"
	)
	for _, h := range []cgcommon.RelativeCgroupPathHandler{
		cgcommonRelativeCgroupHandler(nonQRMUID, mainID, mainRel),
		cgcommonRelativeCgroupHandler(nonQRMUID, initID, initRel),
		cgcommonRelativeCgroupHandler(nonQRMUID, oldInitID, oldInitRel),
	} {
		registerRelativeCgroupPathHandlerForTest(t, h)
	}
	for _, rel := range []string{mainRel, initRel, oldInitRel} {
		fx.cg.existing[rel] = true
		fx.cg.cpus[rel] = machine.MustParse("0-7")
	}
	fx.cg.existing[podDir] = true
	fx.cg.cpus[podDir] = machine.MustParse("0-7")
	fx.cg.existing["primary/burstable"] = true
	fx.cg.cpus["primary/burstable"] = machine.MustParse("0-95")
	fx.cg.children["primary"] = []string{"burstable"}
	fx.cg.children["primary/burstable"] = []string{"pod-x"}
	fx.cg.children[podDir] = []string{"container-main", "container-init", "container-init-old"}

	fx.in.MetaServer.MetaAgent.PodFetcher = &pod.PodFetcherStub{PodList: []*v1.Pod{{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID(nonQRMUID)},
		Spec:       v1.PodSpec{Containers: []v1.Container{{Name: "main"}}},
		Status: v1.PodStatus{
			QOSClass: v1.PodQOSBurstable,
			ContainerStatuses: []v1.ContainerStatus{{
				Name: "main", ContainerID: "containerd://" + mainID,
			}},
			InitContainerStatuses: []v1.ContainerStatus{{
				Name:        "init",
				ContainerID: "containerd://" + initID,
				LastTerminationState: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{
					ContainerID: "containerd://" + oldInitID,
				}},
			}},
		},
	}}}
	// No DesiredView binding for this pod.
	fx.in.DesiredView.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{}

	if got := fx.p.Enable(fx.in); got {
		t.Fatalf("Enable() = true on disabled host, want false")
	}
	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("disabled handler: %v", err)
	}
	machine := fx.machineCPUs.String()
	for _, rel := range []string{"primary/burstable", podDir} {
		if got := fx.cg.writes[rel]; got != machine {
			t.Fatalf("pod dir @ %s = %q, want %q; writes=%#v", rel, got, machine, fx.cg.writes)
		}
	}
	for _, rel := range []string{mainRel, initRel, oldInitRel} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("protected leaf @ %s must not be written; writes=%#v", rel, fx.cg.writes)
		}
	}
}

type erroringPodListFetcher struct {
	pod.PodFetcherStub
	err error
}

func (f *erroringPodListFetcher) GetPodList(_ context.Context, _ func(*v1.Pod) bool) ([]*v1.Pod, error) {
	return nil, f.err
}

// TestGetPodListErrorWithholdsReset pins that a fresh-pod fetch failure makes the
// disabled round return an error (not converge) with zero writes, so the next round
// retries; once the fetcher works it converges and marks complete.
func TestGetPodListErrorWithholdsReset(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	fx.in.MetaServer.MetaAgent.PodFetcher = &erroringPodListFetcher{err: stderrors.New("kubelet down")}

	err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in)
	if err == nil {
		t.Fatalf("expected error when fresh-pod fetch fails, got nil")
	}
	if len(fx.cg.writes) != 0 {
		t.Fatalf("zero writes expected on fetch error, got=%#v", fx.cg.writes)
	}
	if fx.p.NeedsDisabledReset() != true {
		t.Fatalf("NeedsDisabledReset() = false after fetch error, want true (retry)")
	}
}

// TestQRMBinnedAndSidecarContainersBothUnwritten pins that a QRM-protected leaf
// (in DesiredView) and a non-QRM sidecar under the same Pod both receive zero
// writes while the Pod dir is widened.
func TestQRMBinnedAndSidecarContainersBothUnwritten(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	const (
		sidecarID  = "sidecar-id"
		sidecarRel = "primary/dedicated/pod-x/container-sidecar"
	)
	registerRelativeCgroupPathHandlerForTest(t, cgcommonRelativeCgroupHandler("ownership-dedicated-pod", sidecarID, sidecarRel))
	fx.cg.existing[sidecarRel] = true
	fx.cg.cpus[sidecarRel] = machine.MustParse("0-7")
	fx.cg.children[fx.pauseRel] = []string{"container-main", "container-sidecar"}

	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("disabled handler: %v", err)
	}
	for _, rel := range []string{fx.mainRel, sidecarRel} {
		if _, ok := fx.cg.writes[rel]; ok {
			t.Fatalf("protected leaf @ %s must not be written; writes=%#v", rel, fx.cg.writes)
		}
	}
	if got := fx.cg.writes[fx.pauseRel]; got != fx.machineCPUs.String() {
		t.Fatalf("pod dir @ %s = %q, want machine; writes=%#v", fx.pauseRel, got, fx.cg.writes)
	}
}

// TestLeftoverUnknownDirSkippedWithoutBlockingConvergence pins that a leftover dir
// under a controlled root matching no fresh pod is not written and does NOT block
// convergence (UnclassifiedSkipped, not Deferred).
func TestLeftoverUnknownDirSkippedWithoutBlockingConvergence(t *testing.T) {
	t.Parallel()

	fx := newOwnershipFixture(t, false)
	leftover := "primary/dedicated/leftover"
	fx.cg.existing[leftover] = true
	fx.cg.cpus[leftover] = machine.MustParse("0-7")
	fx.cg.children["primary/dedicated"] = []string{"pod-x", "leftover"}

	if err := fx.p.CPUSetAdjustmentDisabledHandler(context.Background(), fx.in); err != nil {
		t.Fatalf("disabled handler: %v", err)
	}
	if _, ok := fx.cg.writes[leftover]; ok {
		t.Fatalf("unknown leftover dir @ %s must not be written; writes=%#v", leftover, fx.cg.writes)
	}
}
