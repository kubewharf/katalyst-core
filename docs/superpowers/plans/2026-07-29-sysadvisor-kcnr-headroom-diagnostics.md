# Sys Advisor 与 KCNR Headroom 诊断日志 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development (recommended) or superpower-executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `reclaim consumer -> headroom assembler -> sysprobe adapter -> CNR reporter` 数据链增加可复用的结构化诊断日志，不改变现有计算和上报行为。

**Architecture:** core 只记录通用路径解析与 headroom 计算边界，不感知 sandbox；adapter 在 consumer、策略选择和 CNR 转换边界补充组件特定上下文。正常周期日志使用 `V(4).InfoS`，异常状态使用 warning，避免默认日志级别持续刷屏。

**Tech Stack:** Go、`k8s.io/klog/v2`、Katalyst `general.InfoS`、Go 单元测试。

---

## 文件结构

- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common.go`
  - 记录通用 reclaim path 输入、过滤结果和 headroom 输出。
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common_test.go`
  - 验证路径诊断摘要，不改变原有 headroom 测试行为。
- Modify: `katalyst-adapter/pkg/agent/reclaim/sandbox/sandbox.go`
  - 在 consumer 初始化完成时记录配置和生成路径。
- Modify: `katalyst-adapter/pkg/agent/sysadvisor/qosaware/cpu/assembler/headroom_sysprobe_adapter_policy.go`
  - 记录 wrapped/sysprobe 策略选择、输入和输出。
- Modify: `katalyst-adapter/pkg/agent/reporter/plugin/cnr/cnrplugin.go`
  - 记录 KCNR allocatable 到 BestEffortResourceAllocatable 的转换边界。
- Test: `katalyst-adapter/pkg/agent/reclaim/sandbox/sandbox_test.go`
- Test: `katalyst-adapter/pkg/agent/reporter/plugin/cnr/cnrplugin_test.go`

### Task 1: Core 路径和 headroom 诊断

**Files:**
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common_test.go`

- [ ] **Step 1: 为路径诊断摘要写失败测试**

在测试文件增加针对纯函数的表驱动测试：

```go
func TestResolveReclaimPathsForDiagnostics(t *testing.T) {
    t.Parallel()
    existing := t.TempDir()
    missing := filepath.Join(t.TempDir(), "missing")

    resolved, inputCount, resolvedCount := resolveReclaimPathsForDiagnostics([]string{existing, missing})

    require.Equal(t, []string{existing}, resolved)
    require.Equal(t, 2, inputCount)
    require.Equal(t, 1, resolvedCount)
}
```

- [ ] **Step 2: 运行测试并确认失败**

Run:

```bash
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler -run TestResolveReclaimPathsForDiagnostics -count=1
```

Expected: FAIL，提示 `resolveReclaimPathsForDiagnostics` 未定义。

- [ ] **Step 3: 实现纯函数和结构化日志**

在 `assembler_common.go` 增加：

```go
func resolveReclaimPathsForDiagnostics(paths []string) ([]string, int, int) {
    resolved := general.GetExistingPaths(paths)
    return resolved, len(paths), len(resolved)
}
```

在 global 和 NUMA 两处调用该函数。正常路径使用：

```go
klog.V(4).InfoS("headroom_reclaim_paths_resolved",
    "component", "headroom_assembler",
    "policy", "common",
    "scope", "numa",
    "numa_id", numaID,
    "input_paths", reclaimPaths,
    "resolved_paths", resolvedPaths,
    "input_count", inputCount,
    "resolved_count", resolvedCount,
    "cpuset", cpuSet.String())
```

当 `inputCount > 0 && resolvedCount == 0` 时使用：

```go
klog.Warningf("headroom_reclaim_paths_resolved component=%s policy=%s scope=%s numa_id=%d input_paths=%v resolved_paths=%v input_count=%d resolved_count=%d",
    "headroom_assembler", "common", "numa", numaID, reclaimPaths, resolvedPaths, inputCount, resolvedCount)
```

计算成功后用 `klog.V(4).InfoS("headroom_computed", ...)` 记录 total 和 NUMA 输出。

- [ ] **Step 4: 格式化并运行 core 测试**

Run:

```bash
gofmt -w pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common.go pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common_test.go
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交 core 变更**

```bash
git add pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common.go \
  pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common_test.go
git commit -m "chore(sysadvisor): add headroom path diagnostics"
```

### Task 2: Adapter consumer 和策略诊断

**Files:**
- Modify: `pkg/agent/reclaim/sandbox/sandbox.go`
- Modify: `pkg/agent/sysadvisor/qosaware/cpu/assembler/headroom_sysprobe_adapter_policy.go`
- Test: `pkg/agent/reclaim/sandbox/sandbox_test.go`

- [ ] **Step 1: 在 sandbox consumer 初始化边界记录生成结果**

在 `NewSandboxConsumer` 完成路径生成后增加：

```go
klog.InfoS("reclaim_consumer_initialized",
    "component", "reclaim_consumer",
    "policy", ConsumerName,
    "scope", "node",
    "root_path", consumer.cgroupPath,
    "numa_path_prefix", sandboxConfiguration.NumaBindingCgroupPathPrefix,
    "numa_paths", consumer.numaBindingCgroupPaths,
    "numa_count", len(consumer.numaBindingCgroupPaths))
```

machine info 为空或 prefix 为空时保留现有行为，并记录 warning，字段中明确 `reason`。

- [ ] **Step 2: 在 sysprobe adapter 记录策略选择**

把现有：

```go
klog.V(4).Infof("[qosaware-cpu] in-use policy switched: %v", p.useWrapped)
```

替换为：

```go
klog.InfoS("headroom_policy_selected",
    "component", "sysprobe_adapter",
    "policy", util.CPUHeadroomAssemblerSysProbeAdapter,
    "source", map[bool]string{true: "wrapped", false: "sysprobe"}[p.useWrapped],
    "use_wrapped", p.useWrapped)
```

在 wrapped 返回前记录 `headroom_computed`；在 sysprobe 分支读取 shared/reclaimed cpuset 和 last reclaimed CPU 后记录 `headroom_input_observed`；最终记录 total 和 per-NUMA 结果。

- [ ] **Step 3: 运行 adapter consumer 测试**

Run:

```bash
gofmt -w pkg/agent/reclaim/sandbox/sandbox.go pkg/agent/sysadvisor/qosaware/cpu/assembler/headroom_sysprobe_adapter_policy.go
go test ./pkg/agent/reclaim/sandbox -count=1
go test ./pkg/agent/sysadvisor/qosaware/cpu/assembler -count=1
```

Expected: PASS；如果 sysadvisor assembler 包没有测试文件，命令仍应编译通过。

- [ ] **Step 4: 提交 adapter consumer/策略变更**

```bash
git add pkg/agent/reclaim/sandbox/sandbox.go \
  pkg/agent/sysadvisor/qosaware/cpu/assembler/headroom_sysprobe_adapter_policy.go
git commit -m "chore(sysadvisor): add consumer and policy diagnostics"
```

### Task 3: Reporter 转换诊断

**Files:**
- Modify: `pkg/agent/reporter/plugin/cnr/cnrplugin.go`
- Modify: `pkg/agent/reporter/plugin/cnr/cnrplugin_test.go`

- [ ] **Step 1: 扩展 reporter 测试输入**

在 `Test_cnrPlugin_GetReportContent` 的 node-level `Resources.Allocatable` 增加：

```go
consts.ReclaimedResourceMilliCPU: resource.MustParse("19000"),
```

并在 `GetReportContent` 返回后验证 `BestEffortResourceAllocatable` report field 中 CPU 值等于 `19`，证明日志加入前后的转换行为保持不变。

- [ ] **Step 2: 运行测试确认当前转换断言**

Run:

```bash
go test ./pkg/agent/reporter/plugin/cnr -run Test_cnrPlugin_GetReportContent -count=1
```

Expected: PASS；该步骤建立修改前基线。

- [ ] **Step 3: 增加转换边界日志**

在 `getCNRBestEffortAllocatable` 中，输入为空时记录：

```go
klog.Warningf("cnr_allocatable_transformed component=%s scope=%s source=%s reason=%s",
    "cnr_reporter", "node", "kcnr_status", "empty_allocatable")
```

转换成功后记录：

```go
klog.V(4).InfoS("cnr_allocatable_transformed",
    "component", "cnr_reporter",
    "scope", "node",
    "source", "kcnr_status",
    "input_value", *cnr.Status.Resources.Allocatable,
    "output_value", bestEffortResourceAllocatable)
```

若输入包含 `reclaimed_millicpu` 但输出没有 CPU，则记录 warning，并带 `input_value`、`output_value` 和 `reason`。

- [ ] **Step 4: 格式化并运行 reporter 测试**

Run:

```bash
gofmt -w pkg/agent/reporter/plugin/cnr/cnrplugin.go pkg/agent/reporter/plugin/cnr/cnrplugin_test.go
go test ./pkg/agent/reporter/plugin/cnr -count=1
```

Expected: PASS。

- [ ] **Step 5: 提交 reporter 变更**

```bash
git add pkg/agent/reporter/plugin/cnr/cnrplugin.go pkg/agent/reporter/plugin/cnr/cnrplugin_test.go
git commit -m "chore(reporter): add allocatable transform diagnostics"
```

### Task 4: 跨仓库验证

**Files:**
- Verify: core 与 adapter 上述全部修改文件。

- [ ] **Step 1: 检查格式和 diff**

Run:

```bash
git diff --check
```

分别在 core worktree 和 adapter 仓库执行。

- [ ] **Step 2: 运行相关测试**

Core:

```bash
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler -count=1
```

Adapter:

```bash
go test ./pkg/agent/reclaim/sandbox ./pkg/agent/sysadvisor/qosaware/cpu/assembler ./pkg/agent/reporter/plugin/cnr -count=1
```

Expected: 全部 PASS。

- [ ] **Step 3: 检查日志事件完整性**

Run:

```bash
grep -R -nE 'reclaim_consumer_initialized|headroom_reclaim_paths_resolved|headroom_policy_selected|headroom_input_observed|headroom_computed|cnr_allocatable_transformed' \
  pkg/agent
```

Expected: 六类事件均存在；core 文件不包含 `sandbox` 特定判断。

- [ ] **Step 4: 检查工作区隔离**

Run:

```bash
git status --short
```

确认没有暂存或提交用户原有的 bulkhead 改动及 adapter `go.mod` 改动。
