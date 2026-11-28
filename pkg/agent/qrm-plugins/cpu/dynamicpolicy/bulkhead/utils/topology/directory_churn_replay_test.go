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

package topology

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Model the recorded v1/SMT2 CPU transfer, not an exact reconstruction of all
// live directories or their syscall latencies. Reclaim stays whole-core and
// nonempty on both physical NUMA nodes throughout this fixture.
func newDirectoryChurnReplay(t *testing.T) (*admissionTraceFixture, *CompleteSnapshot) {
	f := newAdmissionTraceFixture(t)
	f.driver.capabilities = cgroupV1Policy.capabilities(true)
	f.round.allowEmptyTarget = false
	for cpu := 0; cpu < 96; cpu++ {
		f.cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: (cpu % 48) / 24, SocketID: (cpu % 48) / 24, CoreID: cpu % 48}
	}
	oldPrimary := "7-23,31-47,55-71,79-95"
	newPrimary := "7-11,13-23,31-47,55-59,61-71,79-95"
	f.addPrimary("kubepods", oldPrimary, "0-1")
	f.targetByRel["kubepods"] = machine.MustParse(newPrimary)
	for i := 0; i < 12; i++ {
		p := fmt.Sprintf("kubepods/pod-%02d", i)
		f.addDynamicDescendant(p, oldPrimary, "0-1")
		f.addDynamicDescendant(p+"/container", oldPrimary, "0-1")
		f.targetByRel[p] = machine.MustParse(newPrimary)
		f.dynamicByRel[p] = machine.MustParse(newPrimary)
		f.targetByRel[p+"/container"] = machine.MustParse(newPrimary)
		f.dynamicByRel[p+"/container"] = machine.MustParse(newPrimary)
	}
	f.addReclaim("reclaimed", "0-6,24-30,48-54,72-78", "0-1")
	f.targetByRel["reclaimed"] = machine.MustParse("0-6,12,24-30,48-54,60,72-78")
	f.requireCPUSet("reclaimed", f.targetByRel["reclaimed"].String())
	f.addReclaim("reclaimed/numa-0", "0-6,48-54", "0")
	f.addReclaim("reclaimed/numa-1", "24-30,72-78", "1")
	f.specs[len(f.specs)-2].Role = TopoNodeRoleReclaimNUMABucket
	f.specs[len(f.specs)-2].Metadata = map[string]string{"numa": "0"}
	f.specs[len(f.specs)-1].Role = TopoNodeRoleReclaimNUMABucket
	f.specs[len(f.specs)-1].Metadata = map[string]string{"numa": "1"}
	f.targetByRel["reclaimed/numa-0"] = machine.MustParse("0-6,12,48-54,60")
	return f, f.snapshot()
}

type directoryChurnDriver struct {
	HierarchyDriver
	live     *fakeHierarchyDriver
	rel      string
	injected bool
	expire   func()
}

func (d *directoryChurnDriver) ReadEntryWithActivity(ctx context.Context, rel string, expected CgroupIdentity) (EntryState, error) {
	return d.live.ReadEntryWithActivity(ctx, rel, expected)
}

func (d *directoryChurnDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	if !d.injected && rel == d.rel && len(d.live.writes) >= 4 && !isAdjustmentRecoveryContext(ctx) {
		for name := range d.live.nodes {
			if name == d.rel || strings.HasPrefix(name, d.rel+"/") {
				delete(d.live.nodes, name)
			}
		}
		d.injected = true
	}
	if d.injected && d.expire != nil && isAdjustmentRecoveryContext(ctx) {
		d.expire()
		d.expire = nil
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func TestDirectoryChurnReplay(t *testing.T) {
	t.Parallel()

	for _, expiry := range []bool{false, true} {
		expiry := expiry
		t.Run(fmt.Sprintf("deadline_during_recovery_%v", expiry), func(t *testing.T) {
			t.Parallel()
			f, base := newDirectoryChurnReplay(t)
			ctx := newTriggeredDeadlineContext(context.Background())
			b := NewAdjustmentBudget(ctx, ConvergenceBudget{DeadlineDuration: -1, MaxPlanOperations: 10000})
			f.round.adjustmentBudget = b
			f.round.recoveryContextFactory = deterministicRecoveryContext
			d := &directoryChurnDriver{HierarchyDriver: f.driver, live: f.driver, rel: "kubepods/pod-09"}
			if expiry {
				d.expire = ctx.expire
			}
			f.round.driver = d
			initial := f.driver.snapshot()
			res := &ConvergenceResult{}
			_, err := f.round.executeParentSafeAdmission(ctx, base, res, nil, nil)
			require.True(t, d.injected, "deletion must occur after a nonzero live prefix; writes=%d err=%v", len(f.driver.writes), err)
			require.ErrorIs(t, err, ErrCoordinatorPlanStale)
			require.False(t, res.Published)
			for rel, node := range f.driver.snapshot() {
				require.Equal(t, initial[rel], node, "survivor must be restored: %s", rel)
			}
			require.Equal(t, ReplanSafeAfterVerifiedRollback, res.ReplanDisposition)
			require.NotContains(t, f.driver.nodes, d.rel)
			require.NotContains(t, f.driver.nodes, d.rel+"/container")
			for _, rel := range []string{"reclaimed/numa-0", "reclaimed/numa-1"} {
				require.False(t, f.driver.nodes[rel].cpus.IsEmpty(), "reclaim remains nonempty")
			}
			require.Zero(t, reservedWritesCount(b))
			t.Logf("forward=%d rollback=%d cumulative=%d stale=%v", res.forwardWriteAttempts, res.rollbackWriteAttempts, b.CumulativeWrites(), err)
			if expiry {
				require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
				require.ErrorIs(t, b.ConsumeReplan(err), ErrAdjustmentDeadlineExceeded)
				return
			}
			require.NoError(t, ctx.Err())
			require.NoError(t, b.ConsumeReplan(err))
			// Rebuild the caller's inventory from surviving entries; never reuse the
			// missing Pod identity or turn a replacement generation into that old Pod.
			specs := f.specs[:0]
			for _, s := range f.specs {
				if s.Rel == d.rel || strings.HasPrefix(s.Rel, d.rel+"/") {
					delete(f.targetByRel, s.Rel)
					delete(f.dynamicByRel, s.Rel)
					continue
				}
				specs = append(specs, s)
			}
			f.specs = specs
			before := len(f.driver.writes)
			fresh := f.snapshot()
			second := &ConvergenceResult{}
			_, err = f.round.executeParentSafeAdmission(ctx, fresh, second, nil, nil)
			require.NoError(t, err)
			require.True(t, second.Published)
			require.True(t, second.FinalSnapshotCurrent)
			require.Zero(t, reservedWritesCount(b))
			t.Logf("fresh recompile forward=%d cumulative=%d", len(f.driver.writes)-before, b.CumulativeWrites())
		})
	}
}
