# Existing Relative Cgroup Paths 设计

## 目标

统一“根据 cgroupfs 实际目录过滤相对 cgroup 路径”的逻辑，修复普通文件系统路径检查错误应用于 cgroup 相对路径的问题，并减少 headroom、pressure suppression 和 CPU idle 同步中的重复实现。

## API

在 `pkg/util/cgroup/common/path.go` 增加两层 API：

```go
func GetExistingRelativeCgroupPaths(relativePaths ...string) []string

func GetExistingRelativeCgroupPathsForSubsys(
	subsys string,
	relativePaths ...string,
) []string
```

默认入口调用显式入口并传入 `DefaultSelectedSubsys`。显式入口对每个非空相对路径调用 `GetAbsCgroupPath`，使用 `general.IsPathExists` 检查绝对 cgroupfs 路径，最终返回原始相对路径。

函数保持输入顺序，不去重；空输入返回 `nil`，空字符串被忽略。

## 迁移范围

本次迁移以下批量过滤场景：

- `HeadroomAssemblerCommon` 的 default/util-based、global/NUMA 路径过滤。
- `CPUPressureSuppression` 的 global/NUMA reclaim 路径过滤。
- `DynamicPolicy.syncCPUIdle` 的 global/NUMA reclaim 路径过滤。

删除 headroom assembler 中仅为错误过滤逻辑增加的私有 `resolveReclaimPathsForDiagnostics`，诊断日志直接使用通用函数结果。

## 非迁移范围

以下位置保持现状：

- Malachite provisioner：需要逐路径日志和逐路径请求。
- CPU advisor `applyCgroupConfigs`：单路径判断使用现有写法更清晰。
- Adapter CGroupMonitor：依赖可注入的 `PathChecker`，迁移会降低可测试性。
- Cgroup manager 读写函数：用途是路径转换，不是批量过滤。
- `general.GetExistingPaths`：继续服务普通文件系统路径，不改变其语义。

## 兼容性

新 API 复用 `GetAbsCgroupPath` 和 `GetCgroupRootPath`，因此沿用现有 cgroup v1/v2 处理：

- cgroup v1：`/sys/fs/cgroup/<subsys>/<relative-path>`
- cgroup v2：`/sys/fs/cgroup/<relative-path>`

下游收到的仍是相对路径，兼容 metric store、Malachite 和 cgroup manager 现有接口。

## 测试

- 通用 API：nil、空字符串、存在/缺失路径、输入顺序、默认与显式 subsystem。
- Headroom 回归：cgroupfs 绝对目录存在时，相对路径不被过滤，metric store 仍按相对路径读取。
- Pressure suppression 和 `syncCPUIdle`：运行对应包已有测试，验证迁移不改变行为。

## 验收

节点部署后，诊断日志应从：

```text
input_paths=[/kubesandbox] resolved_paths=[]
```

变为：

```text
input_paths=[/kubesandbox] resolved_paths=[/kubesandbox]
```

并且不再出现由空路径集合引起的：

```text
no reclaim cgroup paths provided
```
