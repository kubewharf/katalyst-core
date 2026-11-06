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

package bulkhead

// This file is the characterization/equivalence suite for the
// manager <-> cpuset_topology boundary. It pins the OBSERVABLE behavior of
// Manager.Apply today, so the decoupling refactor (which moves the 3-boolean
// convergence classification out of the manager and into the topology plugin as
// an opaque ConvergenceLevel) cannot silently change outcomes.
//
// Every assertion here describes behavior the refactor must preserve verbatim.
// If a future change intentionally alters one of these, it must be a separate,
// explicitly-reviewed commit.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// equivPlugin is a recording AdjustmentCapable plugin. Every lifecycle hook
// appends an ordered entry to a shared log so tests can assert the exact call
// sequence the manager produces (characterization point (a)).
type equivPlugin struct {
	name      string
	enabled   bool
	adjustErr error
	log       *[]string
}

func (p *equivPlugin) Name() string { return p.name }

func (p *equivPlugin) Enable(_ bulkheadapi.HandlerContext) bool {
	*p.log = append(*p.log, "enable:"+p.name)
	return p.enabled
}

func (p *equivPlugin) CPUSetAdjustmentHandler(_ context.Context, _ bulkheadapi.HandlerContext) error {
	*p.log = append(*p.log, "adjust:"+p.name)
	return p.adjustErr
}

func (p *equivPlugin) CPUSetAdjustmentDisabledHandler(_ context.Context, _ bulkheadapi.HandlerContext) error {
	*p.log = append(*p.log, "disabled:"+p.name)
	return nil
}

// equivTopologyPlugin is a recording TopologyPlugin. It pins the typed Apply
// contract: the manager must invoke Apply exactly once, and (characterization
// point (e)) the legacy ReportTopologyResult callback handed to Apply must be
// suppressed on the typed path.
type equivTopologyPlugin struct {
	*equivPlugin
	result fakeDAGResult
	err    error
}

func (p *equivTopologyPlugin) Apply(_ context.Context, in bulkheadapi.HandlerContext) (bulkheadapi.TopologyOutcome, error) {
	*p.log = append(*p.log, "apply:"+p.name)
	return p.result.outcome(false), p.err
}

func equivAcceptedAppliedView() *model.AppliedView {
	return &model.AppliedView{
		CPUSetPartitionView: model.CPUSetPartitionView{
			ReclaimEffective: machine.NewCPUSet(2, 3),
		},
	}
}

// ---------------------------------------------------------------------------
// (b) The 3-boolean truth table: (FullyConverged, ParentSafe, FinalSnapshotCurrent)
// fully enumerated against downstream execution, publication and override.
// ---------------------------------------------------------------------------

func TestEquivalenceTopologyOutcomeTruthTable(t *testing.T) {
	t.Parallel()

	type expect struct {
		nonConverged    bool
		returnedReclaim string
		depCalls        int
		publishedView   bool
		periodicalValid bool
		overrideReclaim string
		overrideSource  string
	}

	cases := []struct {
		name string
		fc   bool
		ps   bool
		fsc  bool
		want expect
	}{
		{"not_converged_not_parent_snapshot_stale", false, false, false, expect{true, "", 0, false, false, "", ""}},
		{"not_converged_not_parent_snapshot_current", false, false, true, expect{true, "", 0, false, false, "", ""}},
		{"parent_safe_snapshot_stale", false, true, false, expect{true, "", 0, false, false, "", ""}},
		{"parent_safe_snapshot_current", false, true, true, expect{false, "2-3", 0, true, false, "", ""}},
		{"fully_converged_snapshot_stale", true, false, false, expect{true, "", 0, false, false, "", ""}},
		{"fully_converged_snapshot_current", true, false, true, expect{false, "2-3", 1, true, true, "2-3", "cpuset_topology"}},
		{"both_flags_snapshot_stale", true, true, false, expect{true, "", 0, false, false, "", ""}},
		{"both_flags_snapshot_current", true, true, true, expect{false, "2-3", 1, true, true, "2-3", "cpuset_topology"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			log := &[]string{}
			topologyPlugin := &equivTopologyPlugin{
				equivPlugin: &equivPlugin{name: "cpuset_topology", enabled: true, log: log},
				result: fakeDAGResult{
					FullyConverged:       tc.fc,
					ParentSafe:           tc.ps,
					FinalSnapshotCurrent: tc.fsc,
					AppliedView:          equivAcceptedAppliedView(),
				},
			}
			dependent := &equivPlugin{name: "workqueue", enabled: true, log: log}
			m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
			override := &cpusetutil.CPUSetAdjustmentCommitOverride{}
			in := enabledCPUSetAdjustmentCtx()
			in.CommitOverride = override

			got, err := m.Apply(context.Background(), in)

			var nonConv *NonConvergedError
			isNonConv := errors.As(err, &nonConv)
			if isNonConv != tc.want.nonConverged {
				t.Fatalf("nonConverged=%t (err=%v), want %t", isNonConv, err, tc.want.nonConverged)
			}
			if got.String() != tc.want.returnedReclaim {
				t.Fatalf("returned reclaim=%q, want %q", got.String(), tc.want.returnedReclaim)
			}
			// dependent calls = number of "adjust:workqueue" log entries
			depCalls := 0
			for _, entry := range *log {
				if entry == "adjust:workqueue" {
					depCalls++
				}
			}
			if depCalls != tc.want.depCalls {
				t.Fatalf("dependent adjust calls=%d, want %d (log=%v)", depCalls, tc.want.depCalls, *log)
			}
			if (m.appliedView != nil) != tc.want.publishedView {
				t.Fatalf("publishedView=%t, want %t (m.appliedView=%v)", m.appliedView != nil, tc.want.publishedView, m.appliedView)
			}
			if m.appliedViewValidForPeriodical != tc.want.periodicalValid {
				t.Fatalf("periodicalValid=%t, want %t", m.appliedViewValidForPeriodical, tc.want.periodicalValid)
			}
			if override.ReclaimEffective.String() != tc.want.overrideReclaim {
				t.Fatalf("override reclaim=%q, want %q", override.ReclaimEffective.String(), tc.want.overrideReclaim)
			}
			if override.Source != tc.want.overrideSource {
				t.Fatalf("override source=%q, want %q", override.Source, tc.want.overrideSource)
			}
		})
	}
}

// (a) Call order: enable runs for every plugin before any apply/adjust, the
// topology plugin Apply runs before the dependent plugin adjust, and a
// fully-converged result authorizes the dependent plugin.
func TestEquivalencePluginCallOrder(t *testing.T) {
	t.Parallel()

	log := &[]string{}
	topologyPlugin := &equivTopologyPlugin{
		equivPlugin: &equivPlugin{name: "cpuset_topology", enabled: true, log: log},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          equivAcceptedAppliedView(),
		},
	}
	dependent := &equivPlugin{name: "workqueue", enabled: true, log: log}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}

	if _, err := m.Apply(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	want := []string{
		"enable:cpuset_topology",
		"enable:workqueue",
		"apply:cpuset_topology",
		"adjust:workqueue",
	}
	if !reflect.DeepEqual(*log, want) {
		t.Fatalf("call order=%v, want %v", *log, want)
	}
}

// (e) The legacy ReportTopologyResult callback has been removed from
// HandlerContext entirely, so a plugin cannot trigger an observable publication
// before Apply returns. Publication happens only after Apply returns, via
// publishApply, on the returned TopologyOutcome.
func TestEquivalenceTypedApplySuppressesLegacyCallback(t *testing.T) {
	t.Parallel()

	log := &[]string{}
	topologyPlugin := &equivTopologyPlugin{
		equivPlugin: &equivPlugin{name: "cpuset_topology", enabled: true, log: log},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          equivAcceptedAppliedView(),
		},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}

	if _, err := m.Apply(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if m.appliedView == nil {
		t.Fatal("fully-converged typed Apply did not publish applied view after return")
	}
}

// (c) Generation fence at publish (manager.go:568): when the generation fence
// rejects the publish commit, the manager marks the result non-current, returns
// a NonConvergedError and commits nothing to shared state.
func TestEquivalencePublishGenerationFenceRejectsWithoutCommit(t *testing.T) {
	t.Parallel()

	log := &[]string{}
	topologyPlugin := &equivTopologyPlugin{
		equivPlugin: &equivPlugin{name: "cpuset_topology", enabled: true, log: log},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          equivAcceptedAppliedView(),
		},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	fenceCalls := 0
	in.CommitIfGenerationCurrent = func(_ uint64, commit func()) bool {
		fenceCalls++
		// The publish commit is the last fence call of the round.
		// Count fence calls and fail the final one (publish).
		// We cannot know the total up front, so fail only a call that happens
		// after Apply has run (i.e. after apply:cpuset_topology was logged).
		if containsEntry(*log, "apply:cpuset_topology") && fenceCalls > 3 {
			return false
		}
		commit()
		return true
	}

	got, err := m.Apply(context.Background(), in)
	var nonConv *NonConvergedError
	if !errors.As(err, &nonConv) {
		t.Fatalf("Apply() error=%v, want NonConvergedError on publish fence", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("returned reclaim=%s, want empty on rejected publish", got.String())
	}
	if m.appliedView != nil {
		t.Fatalf("rejected publish committed applied view=%v", m.appliedView)
	}
	if !m.LatestAppliedReclaim().IsEmpty() {
		t.Fatalf("rejected publish committed reclaim=%s", m.LatestAppliedReclaim().String())
	}
}

// (d) Emitter payload snapshot for a full-converged apply: the manager emits a
// success result metric for the topology plugin and a view-changed metric.
func TestEquivalenceEmitterPayloadSnapshot(t *testing.T) {
	t.Parallel()

	log := &[]string{}
	topologyPlugin := &equivTopologyPlugin{
		equivPlugin: &equivPlugin{name: "cpuset_topology", enabled: true, log: log},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          equivAcceptedAppliedView(),
		},
	}
	dependent := &equivPlugin{name: "workqueue", enabled: true, log: log}
	emitter := &capturingEmitter{}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()
	in.Emitter = emitter

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	// The topology plugin success result metric must be emitted exactly once.
	if !hasMetricTags(emitter.records, metricBulkheadHandlerResult,
		"phase", "cpuset_adjustment", "plugin", "cpuset_topology", "status", "success") {
		t.Fatalf("missing topology success result metric: %#v", emitter.records)
	}
	// The dependent plugin success result metric must be emitted.
	if !hasMetricTags(emitter.records, metricBulkheadHandlerResult,
		"phase", "cpuset_adjustment", "plugin", "workqueue", "status", "success") {
		t.Fatalf("missing dependent success result metric: %#v", emitter.records)
	}
	// view_changed is emitted on every apply.
	if !hasMetric(emitter.records, metricBulkheadViewChanged) {
		t.Fatalf("missing view_changed metric: %#v", emitter.records)
	}
}

func containsEntry(log []string, want string) bool {
	for _, entry := range log {
		if strings.Contains(entry, want) {
			return true
		}
	}
	return false
}
