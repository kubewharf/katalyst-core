# Sys Advisor 与 KCNR Headroom 诊断日志设计

## 目标

为 `sys_advisor -> headroom assembler -> reporter -> KCNR` 数据链补充只读诊断日志，定位以下问题：

- reclaim consumer 是否生成了正确的全局和 NUMA cgroup 相对路径。
- `HeadroomAssemblerCommon` 是否错误过滤了有效的 cgroup 相对路径。
- `HeadroomSysProbeAdapter` 实际走 wrapped assembler 还是 sysprobe adapter，并记录其输入和输出。
- reporter 转换 KCNR allocatable 时是否携带最新的 reclaimed CPU 值。

本次变更只增加日志，不修改 headroom 算法、路径过滤行为、配置默认值或上报语义。

## 日志点

### Reclaim consumer 初始化

位置：各 reclaim consumer 的公共初始化边界；adapter 的 sandbox consumer 作为首个接入点。

事件名：`reclaim_consumer_initialized`

记录字段：

- consumer 名称。
- consumer 配置的 root path 和 NUMA path prefix。
- machine info 中的 NUMA ID。
- 最终生成的全局 reclaim paths。
- 最终生成的 NUMA binding paths。

该日志仅在 consumer 初始化时打印一次。

### Headroom assembler 路径过滤

位置：core 的 `HeadroomAssemblerCommon`。

事件名：`headroom_reclaim_paths_resolved`

每次计算记录：

- component、policy 和 scope（global 或 NUMA）。
- NUMA ID；global scope 时省略。
- reclaim pool CPUSet。
- 输入的 cgroup 相对路径。
- 路径解析或过滤后的结果。
- 输入路径数、结果路径数和被过滤路径数。

当输入非空但结果为空时使用 warning；正常情况使用 `V(4).InfoS`，避免周期性刷屏。core 层日志不出现 `sandbox` 等具体 consumer 名，只记录 consumer 提供的通用路径集合。

### Sysprobe adapter 分支

位置：adapter 的 `HeadroomSysProbeAdapter`。

事件名：

- `headroom_policy_selected`
- `headroom_input_observed`
- `headroom_computed`

记录字段：

- kubelet `QoSResourceManager` feature gate 检查结果。
- `useWrapped` 的切换结果。
- wrapped assembler 的名称。
- OCNR 中读取到的上一次 reclaimed CPU。
- sysprobe 读取到的 shared/reclaimed cpuset。
- 最终 total 和 per-NUMA headroom。
- wrapped 分支返回的 total 和 per-NUMA headroom。

分支切换日志使用 info；周期性输入输出使用 `V(4).InfoS`；失败沿用现有 warning/error。

### CNR reporter 转换

位置：adapter reporter 的 `getCNRBestEffortAllocatable`。

事件名：`cnr_allocatable_transformed`

记录字段：

- 输入 `cnr.Status.Resources.Allocatable`。
- 输入中的 `reclaimed_millicpu`。
- 转换后的 `BestEffortResourceAllocatable`。
- 转换结果是否包含 CPU。

输入为空或转换结果不包含 reclaimed CPU 时使用 warning；正常转换使用 `V(4).InfoS`。

## 通用字段

所有新增日志使用结构化键值字段，不拼接整段对象。字段名在不同组件间保持一致：

| 字段 | 含义 |
| --- | --- |
| `component` | `reclaim_consumer`、`headroom_assembler`、`sysprobe_adapter` 或 `cnr_reporter` |
| `policy` | 当前 headroom policy 或 consumer 名 |
| `scope` | `global`、`numa` 或 `node` |
| `numa_id` | NUMA scope 的节点编号 |
| `input_paths` | 输入的 cgroup 相对路径 |
| `resolved_paths` | 解析或过滤后的路径 |
| `input_count` | 输入路径数量 |
| `resolved_count` | 有效路径数量 |
| `resource` | `cpu` 或 `memory` |
| `input_value` | 转换或计算前的资源值 |
| `output_value` | 转换或计算后的资源值 |
| `source` | 数据来源，例如 `wrapped`、`sysprobe`、`kcnr_status` |

通用事件可由其他 reclaim consumer、headroom policy 和 reporter 插件复用，不绑定当前节点、sandbox 命名或 KCNR 的某个固定数值。

## 日志约束

- 不打印 Pod、容器或请求级大对象。
- 不打印完整 CNR/KCNR 对象，只打印相关 resource list。
- 周期性成功日志使用 `klog.V(4).InfoS`。
- 配置缺失、路径由非空过滤为空、关键字段缺失使用 `klog.Warningf`。
- 所有日志使用固定事件名，便于节点侧统一检索：

```text
reclaim_consumer_initialized
headroom_reclaim_paths_resolved
headroom_policy_selected
headroom_input_observed
headroom_computed
cnr_allocatable_transformed
```

## 验证方式

本地验证：

- 相关包通过格式化和编译。
- 现有单元测试通过。
- 如日志代码引入辅助函数，为路径过滤异常和 reporter 字段缺失补充单元测试。

节点验证：

1. 部署诊断版 agent，同时重启 sys_advisor 与 reporter。
2. 确认 healthz 与现有版本基线一致。
3. 检索结构化事件名并串联以下数据：

```text
consumer 配置
-> consumer 生成路径
-> assembler 原始/过滤后路径
-> wrapped/sysprobe 分支
-> total/NUMA headroom
-> reporter 输入 allocatable
-> reporter 输出 BestEffortResourceAllocatable
-> KCNR lastUpdate/reclaimed_millicpu
```

4. 诊断完成后根据证据决定修复位置，不在本次日志变更中顺带修改行为。

## 判定标准

日志应能明确区分以下根因：

- consumer 未生成路径。
- consumer 已生成路径，但 `GetExistingPaths` 将 cgroup 相对路径过滤为空。
- sysprobe adapter 实际仍走 wrapped assembler。
- sys_advisor 已产生新 headroom，但 reporter 输入仍是旧 KCNR。
- reporter 输入已更新，但转换输出或后续 patch 丢失。
