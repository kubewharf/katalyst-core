/*
Copyright 2022 The Kubernetes Authors.

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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	preConvergeToken          = "cpuset_topology: admission pre-converge diagnostic"
	admissionViewAppliedToken = "cpuset_topology: admission view applied"
)

// logRecord is one captured sink call, with its severity and verbosity level so a
// test can assert not just that a message appeared but at what severity it appeared.
type logRecord struct {
	severity string // "info" or "error"
	level    int    // verbosity level for InfofV (0 for always-on Infof/Errorf)
	message  string
}

// recordingLogSink is an instance-scoped log sink the plugin writes to. It emulates
// klog verbosity gating: an InfofV(level, ...) call is only recorded when level is at or
// below the configured verbosity, mirroring what general.InfofV does at the process
// default. It holds no global state, so each plugin instance owns its own sink and the
// test runs concurrently with other parallel tests.
type recordingLogSink struct {
	verbosity int
	mu        sync.Mutex
	records   []logRecord
}

func (s *recordingLogSink) append(severity string, level int, format string, args ...interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, logRecord{
		severity: severity,
		level:    level,
		message:  fmt.Sprintf(format, args...),
	})
}

func (s *recordingLogSink) Infof(format string, args ...interface{}) {
	s.append("info", 0, format, args...)
}

func (s *recordingLogSink) InfofV(level int, format string, args ...interface{}) {
	if level > s.verbosity {
		return
	}
	s.append("info", level, format, args...)
}

func (s *recordingLogSink) Errorf(format string, args ...interface{}) {
	s.append("error", 0, format, args...)
}

func (s *recordingLogSink) countContaining(token string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.records {
		if strings.Contains(r.message, token) {
			n++
		}
	}
	return n
}

func (s *recordingLogSink) hasErrorContaining(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r.severity == "error" && strings.Contains(r.message, token) {
			return true
		}
	}
	return false
}

// TestSuccessfulAdmissionLogSeverityContract drives a clean, fully converged admission
// and asserts the logging contract after the pre-Converge planning dump was demoted from
// an always-on general.Errorf to a v4-gated general.InfofV:
//
//   - at default verbosity (v=0) the pre-Converge planning dump must not appear at all
//     (it used to be an ungated Errorf on the success path);
//   - at v=4 the same dump must appear, and structurally as info — never error;
//   - the E2E fence line "admission view applied" must appear exactly once (not merely
//     present) at both v0 and v4, as info.
//
// The sink is injected per plugin instance, so this test neither redirects global klog
// nor takes a global lock; it runs for real under t.Parallel.
func TestSuccessfulAdmissionLogSeverityContract(t *testing.T) {
	t.Parallel()

	newPlugin := func(verbosity int) (*CPUSetTopologyPlugin, *recordingLogSink) {
		cg := &fakeCgroupClient{
			existing: map[string]bool{"primary": true, "reclaim": true},
			cpus: map[string]machine.CPUSet{
				"primary": machine.NewCPUSet(0, 1),
				"reclaim": machine.NewCPUSet(2, 3),
			},
			children: map[string][]string{"": {"primary", "reclaim"}},
		}
		sink := &recordingLogSink{verbosity: verbosity}
		p := &CPUSetTopologyPlugin{
			cfg: bulkheadconfig.BulkheadConfiguration{
				BulkheadPrimaryRelPath:     "primary",
				BulkheadReclaimRelPaths:    []string{"reclaim"},
				AdmissionSafeDuration:      time.Second,
				AdmissionMaxRequiredWrites: 8,
			},
			cgroup:  cg,
			logSink: sink,
		}
		return p, sink
	}

	run := func(t *testing.T, verbosity int) *recordingLogSink {
		t.Helper()
		p, sink := newPlugin(verbosity)
		desired := model.NewDesiredView()
		desired.NonReclaimPool = machine.NewCPUSet(0, 1)
		desired.ReclaimEffective = machine.NewCPUSet(2, 3)

		outcome, err := p.Apply(context.Background(), bulkheadapi.HandlerContext{
			CPUSetAdjustmentHandlerCtx: cpusetutil.CPUSetAdjustmentHandlerCtx{
				Mode:       cpusetutil.CPUSetAdjustmentModeAdmission,
				MetaServer: emptyLifecycleMetaServer(),
				Topology: &machine.CPUTopology{CPUDetails: machine.CPUDetails{
					0: {}, 1: {}, 2: {}, 3: {},
				}},
			},
			DesiredView: desired,
		})
		if err != nil {
			t.Fatalf("Apply(admission) error = %v", err)
		}
		if outcome.Level != bulkheadapi.ConvergenceLevelFull || outcome.View == nil {
			t.Fatalf("admission outcome = %+v, want a fully converged applied view", outcome)
		}
		return sink
	}

	t.Run("v0_preconverge_gated_fence_once", func(t *testing.T) {
		t.Parallel()
		sink := run(t, 0)

		if n := sink.countContaining(preConvergeToken); n != 0 {
			t.Fatalf("pre-Converge planning dump emitted %d times at v0; want 0 (v4-gated)", n)
		}
		if n := sink.countContaining(admissionViewAppliedToken); n != 1 {
			t.Fatalf("admission fence emitted %d times at v0; want exactly 1", n)
		}
	})

	t.Run("v4_preconverge_info_not_error_fence_once", func(t *testing.T) {
		t.Parallel()
		sink := run(t, 4)

		if n := sink.countContaining(preConvergeToken); n != 1 {
			t.Fatalf("pre-Converge planning dump emitted %d times at v4; want exactly 1", n)
		}
		if sink.hasErrorContaining(preConvergeToken) {
			t.Fatalf("pre-Converge planning dump recorded at error severity; want info-only")
		}
		if n := sink.countContaining(admissionViewAppliedToken); n != 1 {
			t.Fatalf("admission fence emitted %d times at v4; want exactly 1", n)
		}
	})
}
