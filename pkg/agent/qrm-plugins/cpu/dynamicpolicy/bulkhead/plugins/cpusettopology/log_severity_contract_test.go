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
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"k8s.io/klog/v2"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// glog header looks like:  I0106 15:04:05.000000 123 file:line] message
// The severity is the leading byte of the header.
const (
	preConvergeToken          = "cpuset_topology: admission pre-converge diagnostic"
	admissionViewAppliedToken = "cpuset_topology: admission view applied"
)

// TestSuccessfulAdmissionLogSeverityContract drives a clean, fully converged
// admission and asserts the logging contract after the pre-Converge planning
// dump was demoted from an always-on general.Errorf to a v4-gated general.InfofV:
//
//   - at default verbosity (v=0) the pre-Converge planning dump must not appear
//     on the error channel (it used to be an ungated Errorf on the success path);
//   - the E2E fence line "admission view applied" must remain visible at default
//     verbosity (it stays an always-on Infof);
//   - the pre-Converge token, even if surfaced at a higher verbosity, must never
//     ride on an error-severity glog header.
func TestSuccessfulAdmissionLogSeverityContract(t *testing.T) {
	t.Parallel()
	cg := &fakeCgroupClient{
		existing: map[string]bool{"primary": true, "reclaim": true},
		cpus: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(0, 1),
			"reclaim": machine.NewCPUSet(2, 3),
		},
		children: map[string][]string{"": {"primary", "reclaim"}},
	}
	p := &CPUSetTopologyPlugin{
		cfg: bulkheadconfig.BulkheadConfiguration{
			BulkheadPrimaryRelPath:     "primary",
			BulkheadReclaimRelPaths:    []string{"reclaim"},
			EnableAdmissionLeafDefer:   true,
			AdmissionSafeDuration:      time.Second,
			AdmissionMaxRequiredWrites: 8,
		},
		cgroup: cg,
	}
	desired := model.NewDesiredView()
	desired.NonReclaimPool = machine.NewCPUSet(0, 1)
	desired.ReclaimEffective = machine.NewCPUSet(2, 3)

	// Capture klog output. klog defaults to logtostderr=true, so routing must be
	// redirected away from stderr into the buffer; restore both afterwards.
	var buf bytes.Buffer
	klog.SetOutput(&buf)
	klog.LogToStderr(false)
	defer func() {
		klog.LogToStderr(true)
		klog.SetOutput(os.Stderr)
		klog.Flush()
	}()

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
	klog.Flush()
	logs := buf.String()

	// The pre-Converge planning dump is v4-gated; at default verbosity it must be
	// absent entirely (and so cannot pollute the error channel).
	if strings.Contains(logs, preConvergeToken) {
		t.Fatalf("pre-Converge planning dump emitted at default verbosity; logs contain:\n%s", logs)
	}

	// Severity invariant: even at a higher verbosity the demoted dump must never
	// ride on an error-severity glog header.
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, preConvergeToken) && strings.HasPrefix(line, "E") {
			t.Fatalf("pre-Converge planning dump still emitted at error severity: %q", line)
		}
	}

	// The E2E fence line must stay visible at default verbosity.
	if !strings.Contains(logs, admissionViewAppliedToken) {
		t.Fatalf("E2E fence %q missing from default-verbosity logs:\n%s", admissionViewAppliedToken, logs)
	}
}
