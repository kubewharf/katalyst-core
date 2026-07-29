# Existing Relative Cgroup Paths Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpower-subagent-driven-development (recommended) or superpower-executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 增加可复用的 cgroup 相对路径存在性过滤 API，并迁移 headroom、pressure suppression 和 CPU idle 同步逻辑。

**Architecture:** 默认 API 封装 `DefaultSelectedSubsys`，显式 API 支持任意 subsystem；两者均复用 `GetAbsCgroupPath` 与 `general.IsPathExists`，检查绝对 cgroupfs 路径但返回原始相对路径。调用方只负责业务错误处理和日志，不再实现路径转换循环。

**Tech Stack:** Go、cgroup v1/v2 路径工具、Go 单元测试。

---

### Task 1: 通用相对路径过滤 API

**Files:**
- Modify: `pkg/util/cgroup/common/path.go`
- Modify: `pkg/util/cgroup/common/path_test.go`

- [ ] **Step 1: 写失败测试**

增加默认与显式 subsystem 测试，覆盖 nil、空字符串、存在/缺失路径和顺序：

```go
func TestGetExistingRelativeCgroupPathsForSubsys(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(existing, 0o755))

	patch := gomonkey.ApplyFunc(GetCgroupRootPath, func(string) string {
		return root
	})
	defer patch.Reset()

	got := GetExistingRelativeCgroupPathsForSubsys(
		CgroupSubsysCPU,
		"/existing",
		"",
		"/missing",
	)
	require.Equal(t, []string{"/existing"}, got)
}
```

- [ ] **Step 2: 验证测试失败**

```bash
go test ./pkg/util/cgroup/common -run 'TestGetExistingRelativeCgroupPaths' -count=1
```

预期：因新函数未定义而失败。

- [ ] **Step 3: 实现两层 API**

```go
func GetExistingRelativeCgroupPaths(relativePaths ...string) []string {
	return GetExistingRelativeCgroupPathsForSubsys(DefaultSelectedSubsys, relativePaths...)
}

func GetExistingRelativeCgroupPathsForSubsys(subsys string, relativePaths ...string) []string {
	if len(relativePaths) == 0 {
		return nil
	}

	existingPaths := make([]string, 0, len(relativePaths))
	for _, relativePath := range relativePaths {
		if relativePath == "" {
			continue
		}
		if general.IsPathExists(GetAbsCgroupPath(subsys, relativePath)) {
			existingPaths = append(existingPaths, relativePath)
		}
	}
	return existingPaths
}
```

- [ ] **Step 4: 格式化并运行测试**

```bash
gofmt -w pkg/util/cgroup/common/path.go pkg/util/cgroup/common/path_test.go
go test ./pkg/util/cgroup/common -count=1
```

预期：PASS。

### Task 2: Headroom assembler 迁移

**Files:**
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common.go`
- Modify: `pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler/assembler_common_test.go`

- [ ] **Step 1: 修改回归测试**

将路径诊断测试改为调用通用 API，并保留“绝对目录存在、返回相对路径”的断言。删除私有 `resolveReclaimPathsForDiagnostics` 测试。

- [ ] **Step 2: 迁移四个调用点**

```go
resolvedPaths := cgroupcommon.GetExistingRelativeCgroupPaths(reclaimPaths...)
```

迁移 default/util-based 的 global/NUMA 路径，删除私有包装函数，保留现有结构化日志。

- [ ] **Step 3: 运行测试**

```bash
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler -count=1
```

预期：PASS。

### Task 3: Pressure suppression 与 CPU idle 迁移

**Files:**
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy/pressure_suppression.go`
- Modify: `pkg/agent/qrm-plugins/cpu/dynamicpolicy/policy_async_handler.go`

- [ ] **Step 1: 迁移 pressure suppression**

global 和 NUMA 都使用：

```go
existingPaths := common.GetExistingRelativeCgroupPaths(reclaimPaths...)
```

删除 NUMA 手写循环和后续 `general.GetExistingPaths(existingPaths)` 二次过滤。

- [ ] **Step 2: 迁移 syncCPUIdle**

global：

```go
existingPaths := cgroupcm.GetExistingRelativeCgroupPaths(
	p.reclaimRelativeRootCgroupPaths...,
)
```

NUMA：

```go
existingNUMAPaths := cgroupcm.GetExistingRelativeCgroupPaths(paths...)
```

- [ ] **Step 3: 运行相关测试**

```bash
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy -count=1
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -run 'Test.*CPUIdle|Test.*Async' -count=1
```

预期：PASS；若第二个正则无测试命中，再运行该包全量测试。

### Task 4: 跨包验证与节点验证

**Files:**
- Verify: 上述所有修改文件。

- [ ] **Step 1: 运行格式和单元测试**

```bash
git diff --check
go test ./pkg/util/cgroup/common \
  ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler \
  ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction/strategy \
  ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -count=1
```

- [ ] **Step 2: 构建诊断 agent**

在 adapter 临时 replace 到当前 core worktree，构建 Linux agent，随后恢复 `go.mod` 并校验无残留。

- [ ] **Step 3: 部署并重启 sys_advisor**

通过架构跳板机上传；备份节点当前 agent；原子替换并只重启 `bytedance.katalyst.sys_advisor`；验证运行中 SHA 和 healthz。

- [ ] **Step 4: 验证日志与 KCNR**

预期：

```text
headroom_reclaim_paths_resolved ... input_paths=[/kubesandbox] resolved_paths=[/kubesandbox]
```

确认新 PID 不再出现：

```text
no reclaim cgroup paths provided
```

随后检查 `headroom_computed`、CPU NUMA report 和 KCNR `lastUpdate/reclaimed_millicpu`。
