# Bulkhead Healthz Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development or superpower-executing-plans to implement this plan task-by-task.

**Goal:** Prevent bulkhead E2E checks from hanging indefinitely and identify whether high-churn stalls occur in system-service cgroup migration or another periodical handler.

**Architecture:** Bound healthz observation in the E2E checker and collect thread/cgroup evidence on timeout. Add low-volume timing logs at bulkhead manager, system-service migration, and cgroup PID-attach boundaries without changing cgroup operation or cancellation semantics.

**Tech Stack:** Bash, curl, Python 3, Go testing, Linux cgroup v1, runsv.

---

### Task 1: Bound healthz checks

**Files:**
- Modify: `/Users/bytedance/go/src/github.com/kubewharf/.trae/skills/qrm-bulkhead-e2e/scripts/qrm_node_check.sh`

- [ ] Add `HEALTHZ_CONNECT_TIMEOUT="${HEALTHZ_CONNECT_TIMEOUT:-2}"` and `HEALTHZ_MAX_TIME="${HEALTHZ_MAX_TIME:-8}"`.
- [ ] Replace the unbounded `curl | tee` pipeline with a temporary-file flow:

```bash
healthz_file="${TMPDIR:-/tmp}/qrm_healthz_${$}.json"
if ! curl --connect-timeout "$HEALTHZ_CONNECT_TIMEOUT" --max-time "$HEALTHZ_MAX_TIME" \
  -sS "127.0.0.1:$port/healthz" > "$healthz_file"; then
  echo "HEALTHZ_TIMEOUT_OR_FAIL connect_timeout=${HEALTHZ_CONNECT_TIMEOUT}s max_time=${HEALTHZ_MAX_TIME}s" | tee -a "$out"
  echo "HEALTHZ_EVIDENCE_REQUESTED pid=$agent_pid port=$port" | tee -a "$out"
  ps -L -o pid,tid,ppid,stat,wchan,comm -p "$agent_pid" | tee -a "$out" || true
  for stack in /proc/"$agent_pid"/task/*/stack; do
    echo "STACK_FILE=$stack" | tee -a "$out"
    cat "$stack" 2>/dev/null | head -n 40 | tee -a "$out" || true
  done
  rc=1
else
  cat "$healthz_file" | tee /tmp/qrm_healthz.json
  python3 - /tmp/qrm_healthz.json ...
fi
```

- [ ] Ensure failed curl output is not parsed from stale `/tmp/qrm_healthz.json`.
- [ ] Run `bash -n` on the checker and verify a fake timeout emits `HEALTHZ_TIMEOUT_OR_FAIL`.

### Task 2: Time bulkhead periodical handlers

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/manager.go`
- Test: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/manager_test.go`

- [ ] Add `bulkheadSlowHandlerThreshold = 500 * time.Millisecond`.
- [ ] Measure each `p.PeriodicalHandler` call and log at V(2) only when elapsed time is at least the threshold:

```go
started := time.Now()
pluginErr := p.PeriodicalHandler(ctx, pluginCtx)
elapsed := time.Since(started)
if elapsed >= bulkheadSlowHandlerThreshold {
    general.InfofV(2, "bulkhead periodical slow plugin=%s elapsed=%s", p.Name(), elapsed)
}
```

- [ ] Measure the complete `RunPeriodicalHandlers` pass in the existing defer and preserve healthz/error aggregation.
- [ ] Add deterministic threshold tests without sleeps.
- [ ] Run `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead`.

### Task 3: Time system-service migration

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/systemservice/plugin.go`
- Test: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/systemservice/plugin_test.go`

- [ ] Add `systemServiceSlowAttachThreshold = 200 * time.Millisecond`.
- [ ] Measure every `p.cgroup.AttachPID` call and log slow attaches at V(2), preserving the current race-tolerant continue-on-error behavior.
- [ ] Add elapsed duration to the migration completion log and emit a V(2) slow-sweep log when the total exceeds the threshold.
- [ ] Test that attach errors do not stop later PIDs and that timing classification is deterministic.
- [ ] Run `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/plugins/systemservice`.

### Task 4: Time the cgroup write boundary

**Files:**
- Modify: `pkg/util/cgroup/client/client.go`
- Test: `pkg/util/cgroup/client/client_test.go`

- [ ] Measure the `fmt.Fprintf` write to `cgroup.procs`.
- [ ] At V(2), log `cgroup AttachPID slow rel=%q pid=%d elapsed=%s` for writes at least 200ms.
- [ ] Keep invalid-PID errors, write errors, and direct synchronous write semantics unchanged; do not add goroutine cancellation or force-close behavior.
- [ ] Run `go test ./pkg/util/cgroup/client`.

### Task 5: Build and bounded node reproduction

**Files:**
- Create artifacts under `/Users/bytedance/go/src/github.com/kubewharf/qrm-bulkhead-test-artifacts/`.

- [ ] Run focused Go tests, shell syntax checks, and Python compilation.
- [ ] Build the current Linux amd64 agent using the existing adapter local-replace procedure; record SHA and restore `katalyst-adapter/go.mod`.
- [ ] Back up and deploy the diagnostic agent under the runsv-root path, verify PID and SHA, and upload the bounded checker.
- [ ] Run bounded high-churn:

```bash
NODE_CHECK_STRICT=true HEALTHZ_CONNECT_TIMEOUT=2 HEALTHZ_MAX_TIME=8 \
NODE_CHECK_RETRIES=18 RUN_TAG=<new-tag> PREFIX=<new-prefix> ROUNDS=5 \
./high_churn_5rounds.sh
```

- [ ] On healthz timeout, preserve emitted evidence and perform state-drain-aware final reset.
- [ ] Package logs, run `tar -tzf`, compare local SHA with remote `REMOTE_LOG_SHA`, and report pass only if all five rounds and final reset return zero.

