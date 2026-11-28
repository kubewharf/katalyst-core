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
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Diagnostics are invocation-local and do not authorize writes or replans.
// rollbackVerification is included in rollback; forward excludes final proof
// and rollback. The last trace identity is diagnostic, not publication authority.
type admissionStageDiagnostics struct {
	started                                              time.Time
	compile, reservation, preflight, forward, finalProof time.Duration
	rollback, rollbackVerification                       time.Duration
	compileCalls, preflightCalls, rollbackCalls          int
	traceID, failureStage                                string
	snapshotID                                           SnapshotID
	entries, operations, phases                          int
}

func (d admissionStageDiagnostics) log(ctx context.Context, err error, res *ConvergenceResult) {
	if err != nil && d.failureStage == "" {
		d.failureStage = "unknown"
	}
	format := "cpuset_topology: admission stages finished failure_stage=%s duration=%s compile=%s reservation=%s preflight=%s forward=%s final_proof=%s rollback=%s rollback_verification=%s compile_calls=%d preflight_calls=%d rollback_calls=%d last_trace=%s last_snapshot=%x last_entries=%d last_operations=%d last_phases=%d forward_attempts=%d rollback_attempts=%d published=%t context_err=%v error_type=%T"
	args := []interface{}{d.failureStage, time.Since(d.started), d.compile, d.reservation, d.preflight, d.forward, d.finalProof, d.rollback, d.rollbackVerification, d.compileCalls, d.preflightCalls, d.rollbackCalls, d.traceID, d.snapshotID, d.entries, d.operations, d.phases, res.forwardWriteAttempts, res.rollbackWriteAttempts, res.Published, ctx.Err(), err}
	if err != nil {
		general.Infof(format, args...)
	} else {
		general.InfofV(4, format, args...)
	}
}
