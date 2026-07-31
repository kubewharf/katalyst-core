# Ramp-Up Reclaim 合并后方案评估

## 评估基线

本评估基于合并后的实际代码，而不是分别基于两个原分支推演。

Core：

```text
branch: feat/ramp-up-reclaim-bulkhead-integration
merge:  c28cd5bb8 merge: integrate dedicated reclaim isolation
repair: fbe65385e fix(cpu): reconcile dedicated isolation merge
```

API：

```text
branch: feat/ramp-up-reclaim-bulkhead-integration-api
commit: 98b7c64 feat(adminqos): merge dedicated reclaim overlap config
```

集成 worktree：

```text
katalyst-core/.worktrees/ramp-up-reclaim-bulkhead-integration
```

配套 API worktree：

```text
katalyst-api-ramp-up-reclaim-bulkhead-integration
```

## 合并结果

两个 core 分支相对共同基线分别有 94 和 105 个独立提交，直接 merge 产生了 31 个冲突。冲突集中在：

- QRM state 与 checkpoint；
- advisor block materialization；
- allocation handlers；
- source-pool carve；
- bulkhead topology phase pipeline；
- SysAdvisor assembler；
- API 依赖。

集成时以 `eval/pr-1202-on-core-bulkhead-handlers` 的新 bulkhead 架构为冲突结构基线，再语义回放 dedicated isolation。不能使用 feature 分支中的旧 bulkhead 文件覆盖 eval 实现。

语义回放后恢复了：

- CLI dedicated overlap 配置传播；
- QRM state flag、clone、getter/setter 与 checkpoint；
- advisor response 到 QRM state 的 dedicated flag 同步；
- 非独占 DNB 在隔离开启时禁止 fallback 使用 reclaim CPU；
- NUMA-local reclaim block 显式容量校验；
- dedicated isolation allocator 与 assembler 回归测试；
- eval 分支已有的 source-pool、metrics、cgroup client 和 test helper。

## 当前测试

以下测试已通过：

```text
go test ./pkg/config/agent/dynamic/adminqos/advisor
go test ./cmd/katalyst-agent/app/options/dynamic/adminqos/advisor
go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/state
go test ./pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/...
MOCKEY_CHECK_GCFLAGS=false \
  go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy -count=1
```

当前集成分支已经是可评估的功能基线，不再包含 merge 产生的编译断裂。

## 核心目标

启用 `EnableRampUpReclaimHardPartition` 后，独占 DNB ramp-up 必须把每个绑定 NUMA 分成两个调度域：

```text
H_n = hard ramp-up reclaim CPUSet
D_n = exclusive DNB CPUSet
A_n = eligible NUMA CPUSet
```

必须满足：

```text
H_n != empty
D_n != empty
H_n ∩ D_n = empty
H_n ∪ D_n = A_n
|D_n| >= requestInNUMA[n]
```

该不变量必须同时存在于：

- QRM allocation state；
- advisor blocks；
- QRM block materialization；
- bulkhead partition view；
- cgroup 实际结果。

只满足 size、不保持具体 CPU ID，或者由 bulkhead静默执行 `reclaim -= primary`，都不能满足要求。

## 合并后已有基础

### QRM candidate

`planBlocks` 已在 clone state 上计算并返回：

```go
type TargetState struct {
    PodEntries   PodEntries
    MachineState NUMANodeMap
}
```

无需再新建第二套 planner 类型。应扩展 `TargetState`，使其携带：

- 本轮两个 overlap flags；
- hard ramp-up reclaim；
- candidate NUMA headroom，如 handler 确实依赖；
- `ReadonlyState` 所需 getter。

### adjustment handler context

现有 handler 已接受：

```go
type CPUSetAdjustmentHandlerCtx struct {
    State                 state.ReadonlyState
    Topology              *machine.CPUTopology
    RequireFullyConverged bool
    // ...
}
```

无需引入新的 `Preflight/Apply` interface。只需让调用方显式传入 candidate state：

```go
func (p *DynamicPolicy) runCPUSetAdjustmentHandlersForState(
    ctx context.Context,
    target state.ReadonlyState,
    requireFullyConverged bool,
) error
```

runner 把参数写入 `CPUSetAdjustmentHandlerCtx.RequireFullyConverged`。topology plugin 在该字段为 true 且 `ApplyResult.FullyConverged=false` 时返回明确错误；否则现有 handler 会吞掉 deferred，runner无法判断 candidate是否可提交。

周期性 reconcile 继续传 `p.state` 和 false；advisor/admission candidate传 `TargetState` 和 true。

### bulkhead pipeline

eval 分支已经具备：

- `CPUSetPartitionView`；
- primary/reclaim NUMA bucket；
- phase pipeline；
- release-before-acquire；
- child-before-parent shrink；
- parent-before-child grow；
- read-back convergence；
- safe writer；
- source-pool carve。

不应重写 topology coordinator。新功能只需要补 hard target 输入、fail-closed validation 和 candidate 模式成功条件。

### dedicated isolation

合并后已具备：

- `DisableDedicatedCoresOverlapReclaimedCores` 配置、proto、state；
- non-exclusive DNB strict candidate restriction；
- SysAdvisor stable dedicated capacity隔离；
- QRM NUMA reclaim capacity guard；
- assembler ratio cap；
- reclaim CPUSet stable reuse。

这些逻辑必须保留。

## 需要修订的原方案

### 不新增三态 pending

完整方案文档提出：

```text
RampUp
StablePending
Stable
```

合并后可以更简单地表达：

```text
live AllocationInfo.RampUp=true:
  reservation仍受保护

transitionPeriod到期:
  live state暂不清除RampUp
  advisor request对外报告ramp_up=false

stable candidate:
  candidate中先设置RampUp=false
  更新candidate中的reclaim pool AllocationResult为stable target
  提交candidate为新的live state
  bulkhead基于已提交stable state完全收敛
```

这样不需要 `RampUpReservationPending`。时间戳决定发给 SysAdvisor 的 phase，live `RampUp` 决定 reservation ownership。

### 不新增 handler interface

现有 handler context 已支持任意 `ReadonlyState`。只扩展 runner，不重新设计插件注册和 manager 接口。

### region/policy 改造可收缩

QRM 已经选择并持久化具体 bootstrap reclaim CPU。SysAdvisor 不应重新计算 ratio。

首期不必让 RAMA、DynamicQuota、HeadroomPolicy 分别实现 ratio。Assembler 在 active ramp-up 时覆盖最终 block size即可。

仍需要：

- SNB topology正确传递；
- DNB ramp-up 在 assembler 中优先使用 bootstrap target；
- stable phase恢复正常 policy输出。

如果 assembler可以直接从当前 reclaim pool topology和 ramp-up containers判断 bootstrap phase，就不必新增 `QoSRegion.HasRampUpContainer()` 接口。可通过 `region.GetPods()` 与 metacache helper 聚合。

### generation 取决于通信模式

同步 `GetAdvice` 路径已经保留原 request，并在 QRM lock 内执行 request validation。首期仅支持同步模式时，可以通过“request与当前state精确比对”拒绝 stale response，不增加 generation proto。

如果新能力必须支持 legacy `ListAndWatch`，generation ACK 仍是必须项：

```proto
GetCheckpointResponse.state_generation
ListAndWatchResponse.base_state_generation
```

entries 与 generation 必须来自同一 checkpoint snapshot。否则 SysAdvisor 无法证明异步 response 基于哪一版 `RampUp` 和 reclaim topology。

推荐决策：

```text
首期强制同步 GetAdvice + feature negotiation
后续为 ListAndWatch 增加 generation
```

这能显著收缩首次实现。

## 配置设计

### API

新增到 `QRMPluginConfig.CPUPluginConfig`：

```go
EnableRampUpReclaimHardPartition *bool
    `json:"enableRampUpReclaimHardPartition,omitempty"`

InitialRampUpReclaimCPUSetRatio *float64
    `json:"initialRampUpReclaimCPUSetRatio,omitempty"`
```

开关语义：

```text
EnableRampUpReclaimHardPartition != true:
  legacy，不启用hard partition

EnableRampUpReclaimHardPartition == true:
  启用hard partition planner
```

ratio 语义：

```text
nil      = 动态配置未覆盖，使用启动 flag 或默认配置中的 ratio
0        = 只使用reserve floor
(0, 1]   = 使用ratio和reserve floor较大值
```

API 与 core dynamic config 均保留 enable 指针，避免无法区分“未下发开关”和“显式关闭”。ratio 指针表达动态配置是否下发过比例值；nil 时不得覆盖 flag/default ratio。

CRD 与 CLI 校验：

```text
0 <= ratio <= 1
```

### feature gate

增加必须双向支持的 feature gate，例如：

```text
cpu_ramp_up_reclaim_hard_partition
```

`EnableRampUpReclaimHardPartition == true` 时：

- QRM 与 SysAdvisor 必须都支持；
- 未协商成功时 fail closed；
- 不回退到不支持协商的 legacy 异步路径。

## ratio 与 cap

合并后的 SysAdvisor 已支持 `ReclaimedCPUMaxRatio`，其 cap 使用物理 CPU 数和 `floor`：

```text
cap[n] = floor(maxRatio × physicalCPUCount[n])
```

新 bootstrap target 使用 eligible CPU 数和 `ceil`：

```text
bootstrap[n] =
  max(
    reserveForReclaim[n],
    ceil(initialRatio × eligibleCPUCount[n]),
  )
```

组合约束：

```text
cap[n] > reserveForReclaim[n]
bootstrap[n] <= cap[n]
bootstrap[n] < eligibleCPUCount[n]   // exclusive DNB必须留下非空remainder
```

不满足时拒绝 admission 或 advisor update。不能：

- 把 bootstrap 静默 clamp 到 cap；
- 降低 reserve；
- 跨 NUMA借位；
- fallback 到 dedicated/reclaim overlap。

## reserve floor

QRM 当前静态 `reservedReclaimedCPUSet` 与 SysAdvisor 动态 `reservedForReclaim` 算法并不等价。

必须抽取共用纯函数：

```go
func CalculateReservedForReclaimByNUMA(
    topology *machine.CPUTopology,
    minReclaimed resource.Quantity,
    numaMinReclaimed resource.Quantity,
    numaMinRatio resource.Quantity,
) (map[int]int, error)
```

保持现有 SysAdvisor配置语义：

- reserve ratio仍以物理 NUMA CPU数为分母；
- initial ramp-up ratio以 eligible CPU数为分母；
- QRM与SysAdvisor使用同一floor结果。

## AllocationInfo 与 reclaim pool entry

不新增 `AllocationInfo` 字段，复用现有 `AllocationResult`：

- main container 的 `AllocationResult` 继续表达 workload CPUSet；
- `PoolNameReclaim/FakedContainerName` 的 `AllocationResult` 表达 QRM 已提交的 concrete reclaim target；
- initial ramp-up planner 通过更新 reclaim pool entry 持久化 hard reclaim；
- legacy checkpoint 通过 `RampUp=true` + reclaim pool invariant 校验 fail closed，不依赖新增 planned bool。

sidecar同步：

- `RampUp`
- `InitTimestamp`
- workload CPUSet
- topology

sidecar不复制 reservation ownership，避免重复聚合；reservation ownership 来自 reclaim pool entry。

## QRM planner

复用并扩展现有 `TargetState`。

建议新增纯 helper：

```go
func (p *DynamicPolicy) planInitialRampUpAllocation(
    entries state.PodEntries,
    machineState state.NUMANodeMap,
    req *pluginapi.ResourceRequest,
    ratio float64,
    reserveByNUMA map[int]int,
) (*state.TargetState, error)
```

### 普通 shared

整机 target：

```text
target =
  max(
    sum(reserveByNUMA),
    ceil(ratio × machineEligibleCPUCount),
  )
```

实际 CPUSet 选择仍须逐 NUMA满足 floor。

`DisableSharedCoresRampUp=true` 时忽略 ratio，保持 direct-pool 逻辑。

### SNB

每个绑定 NUMA：

```text
target[n] =
  max(
    reserveByNUMA[n],
    ceil(ratio × eligibleCPUCount[n]),
  )
```

只改变绑定 NUMA，不跨 NUMA借位。

SNB 必须设置 `RampUp=true`，并在 advisor request中发送绑定 topology。

### 非独占 DNB

```text
hard reclaim = target[n]
DNB size      = requestInNUMA[n]
DNB candidate = eligible[n] - hard reclaim[n]
```

不允许 preferred CPU不足后再进入 reclaim。

### 独占 DNB

```text
hard reclaim = target[n]
DNB           = eligible[n] - hard reclaim[n]
```

DNB绑定整个 remainder，不只是 request。

## advisor request

active ramp-up：

```text
request.ramp_up = true
```

transitionPeriod到期，但 stable candidate尚未应用：

```text
live AllocationInfo.RampUp = true
request.ramp_up = false
```

同步 response回来后，重新从当前 state和当前配置生成规范化 `GetAdviceRequest`，并与原 request做完整结构比较。比较范围必须覆盖：

- 全部 Pod/container metadata；
- QoS、request quantity 和 annotations；
- request phase和owner pool；
- DNB/SNB topology；
- reclaim pool bootstrap topology；
- resource package config；
- wanted feature gates和所有影响advice的动态配置快照。

不能只比较字段子集。in-place resize即使不改变topology，也必须使旧response失效。当前 `ValidateRequest` 仅验证部分 SNB 信息，需要扩展为完整 request snapshot validator。

如果规范化后的完整 request不相等，拒绝response并等待下一轮同步 `GetAdvice`。

## SysAdvisor

### active phase

从 QRM request中的 reclaim pool topology读取 bootstrap target。

SysAdvisor只计算 size与block关系，不选择具体 CPU ID。

### exclusive DNB

输出：

```text
dedicated.result = eligibleSize - bootstrapSize
reclaim.result   = bootstrapSize
```

两个 block：

```text
block IDs不同
overlap targets为空
```

当前 stable strict isolation 中：

```text
dedicated NUMA-exclusive + reserve > 0 => error
```

该分支仅适用于 stable 旧语义。active bootstrap phase应允许NUMA二分。

### policy output

active phase最终 block size由bootstrap override。RAMA、DynamicQuota和Headroom可以继续计算观测结果，但不能覆盖 bootstrap target。

## block materialization

当前顺序是：

```text
dedicated/share first
reclaim last
```

新模式必须变为：

1. 检测 active ramp-up main containers。
2. 从 reclaim pool entry 的 `AllocationResult` 按 active ramp-up scope 得到 `HardReclaim[n]`。
3. 验证 reclaim block result不小于hard size。
4. 将 hard CPU精确预绑定到 reclaim block。
5. 从 available CPU中扣除hard CPU。
6. 再 materialize source pool、dedicated和shared blocks。
7. 最后补齐 reclaim block的非hard部分。
8. 验证最终 reclaim包含完整hard set。

首期建议限制：

```text
每个NUMA只有一个non-overlap reclaim block
```

避免 hard set在多个 block之间产生不稳定拆分。

## source-pool carve

eval 分支新增的 source-pool helpers必须基于 candidate entries，不得内部读取 live `p.state`：

```go
deriveAdvisorIsolationSourcePool(block, candidateEntries)
```

hard reclaim CPU 必须从所有 source carve候选中排除。

## candidate apply

### TargetState

扩展 `TargetState` 并实现完整 `ReadonlyState`：

```go
type TargetState struct {
    PodEntries   PodEntries
    MachineState NUMANodeMap

    AllowSharedCoresOverlapReclaimedCores      bool
    DisableDedicatedCoresOverlapReclaimedCores bool
}
```

candidate getter不能回退到live state，避免混合快照。

### handler runner

复用现有 handler context：

```go
func (p *DynamicPolicy) runCPUSetAdjustmentHandlersForState(
    ctx context.Context,
    target state.ReadonlyState,
    requireFullyConverged bool,
) error
```

candidate 流程：

```text
plan target
→ hooks
→ regenerate MachineState
→ validate hard partition
→ run bulkhead with candidate state and RequireFullyConverged=true
→ commit PodEntries/MachineState/flags
→ StoreState
```

apply失败或deferred时不得提交candidate。

## bulkhead

### Partition view

增加：

```go
HardReclaim        machine.CPUSet
HardReclaimPerNUMA map[int]machine.CPUSet
```

从 planned main containers聚合。

builder需要返回error并校验：

```text
HardReclaim ⊆ ReclaimRaw
HardReclaim ⊆ ReclaimEffective
HardReclaim ∩ Dedicated = empty
```

### normalization

保留普通 transient overlap的：

```text
reclaim -= primary
```

但 hard target必须在扣减前后验证：

```text
HardReclaim ∩ Primary = empty
HardReclaim ⊆ normalized reclaim
```

不能被静默扣除。

### candidate convergence

当前 periodical reconcile可以接受 deferred。

candidate 模式必须：

```text
FullyConverged=false => error
```

否则 handler返回成功后，candidate会在物理partition尚未建立时被提交。

### safe writer

保留现有 phase pipeline。

对 controlled hard reclaim bucket禁止：

- empty fallback；
- boundary fallback；
- 跨 NUMA target；
- primary占用hard CPU。

## lifecycle

### active

```text
AllocationInfo.RampUp=true
reclaim pool AllocationResult满足hard partition invariant
```

### 到期

`GetResourcesAllocation` 不直接清理新模式 reservation。

同步 advisor request报告 `ramp_up=false`，live state仍保持active ownership。

### stable candidate

SysAdvisor生成stable blocks。

QRM执行：

```text
clone live state as stable candidate
→ candidate中RampUp=false
→ candidate中更新reclaim pool AllocationResult为stable target
→ 基于该candidate materialize stable blocks
→ regenerate MachineState并校验stable不变量
→ bulkhead基于stable candidate fully converged
→ commit并StoreState
```

## checkpoint

不新增 `AllocationInfo` 字段后，不再因为 hard reclaim 引入新的 checkpoint schema字段。

发布要求：

- 开启 `SkipCPUStateCorruption`；
- `RampUp=true` 但 reclaim pool invariant 不满足时按legacy entry fail closed；
- restore成功后立即重写新checkpoint；
- rollback旧binary同样需要skip corruption或checkpoint转换。

不必首期增加schema version，但必须把迁移行为写入发布说明。

## 实施范围

### 第一组：配置与 state

- API `CPUPluginConfig`
- CRD/deepcopy
- core dynamic config
- feature gate
- shared reserve helper
- reclaim pool entry复用与checkpoint校验
- checkpoint迁移
- sidecar同步

### 第二组：QRM initial planner

- shared
- SNB
- non-exclusive DNB
- exclusive DNB
- `GetResourcesAllocation`
- advisor request phase

全局 hard partition 开关打开前，shared、SNB、非独占 DNB和独占 DNB必须全部实现。当前开关没有 workload维度，不能只实现exclusive DNB后启用同一个全局字段。

### 第三组：SysAdvisor 与 blocks

- bootstrap target读取
- exclusive DNB二分
- stable/ramp-up分支
- block ID和overlap约束
- ratio cap组合校验

### 第四组：materialization 与 bulkhead

- hard-first block materialization
- candidate source-pool
- TargetState ReadonlyState
- candidate handler runner
- partition hard fields
- normalization fail-closed
- fully-converged gate

### 第五组：异步兼容

如果必须支持 ListAndWatch：

- state generation
- checkpoint response generation
- list/watch response base generation
- SysAdvisor snapshot绑定
- stale response rejection

## 测试范围

### 集合不变量

每个独占 DNB NUMA：

```text
hard reclaim非空
dedicated非空
交集为空
并集等于eligible
dedicated大小不小于request
```

### candidate 原子边界

- planner失败不改live state；
- source carve失败不改live state；
- hard validator失败不写cgroup；
- topology deferred不提交candidate；
- fully converged后一次提交。

### ratio/cap

- reserve高于initial ratio；
- initial高于reserve；
- bootstrap高于max cap时拒绝；
- max cap小于等于reserve时拒绝；
- exclusive remainder为空时拒绝。

### lifecycle

- active request；
- 到期后request报告stable、live仍保护hard；
- stale同步response被request validator拒绝；
- stable apply成功后才清理hard；
- sidecar不重复持有reservation。

### bulkhead

- view不吞hard reclaim；
- normalization不扣hard；
- source carve不使用hard；
- cgroup v1 reclaim非空；
- candidate模式deferred返回error；
- fake writer每次写后检查sibling disjoint和child属于parent。

## 风险

| 风险 | 严重度 | 控制 |
|---|---|---|
| candidate source-pool读取live state | 高 | source helper显式传candidate entries |
| block先分dedicated抢占hard CPU | 高 | hard reclaim优先预绑定 |
| normalization吞掉hard target | 高 | 扣减前后双重校验 |
| deferred被当成功 | 高 | candidate模式要求fully converged |
| bootstrap超过max cap | 高 | QRM与SysAdvisor共用组合validator |
| stable transition提前释放hard | 高 | stable candidate应用成功后才清理 |
| API依赖分裂 | 高 | 使用配套API集成分支，后续发布pseudo-version |
| checkpoint checksum失配 | 中 | skip corruption迁移并立即重写 |
| legacy ListAndWatch stale advice | 高 | 首期禁用；后续增加generation |

### 发布阻断项

当前 core `go.mod` 使用本机绝对路径：

```text
replace github.com/kubewharf/katalyst-api =>
  /Users/bytedance/go/src/github.com/kubewharf/katalyst-api-ramp-up-reclaim-bulkhead-integration
```

该形式只用于本地合并验证，不能进入 CI 或发布。完成标准必须包括：

1. 推送配套 API 集成分支。
2. 获取可下载的 API pseudo-version 或正式版本。
3. core `require/replace` 指向远端可获取版本。
4. 删除本机绝对路径 replace。
5. 在不挂载本地 API worktree 的环境重新运行编译和测试。

## 最终判断

合并后的代码已经提供大部分底层执行能力，方案不再需要大规模重构 bulkhead。

必须实现的核心闭环是：

```text
QRM选择并持久化hard reclaim
→ SysAdvisor输出独立合法blocks
→ QRM先预绑定hard CPU
→ QRM提交candidate为新的目标state
→ bulkhead基于已提交state校验和收敛
```

原完整方案中的以下方向保留：

- QRM是具体CPUSet的唯一owner；
- SysAdvisor不重新选择CPU ID；
- hard target不可被normalization修正；
- exclusive DNB/reclaim必须非空、互斥、覆盖NUMA；
- checkpoint恢复必须fail closed。

以下设计不再作为首期前置：

- 新的 handler interface；
- `RampUpReservationPending` 三态；
- 全量 region接口扩展；
- RAMA/DynamicQuota/Headroom逐层实现ratio；
- 同步GetAdvice的generation字段；
- 重写现有phase pipeline。

用户目标明确要求 shared、SNB、非独占DNB和独占DNB均支持该 ratio，因此全局配置启用前必须按本文前四组一次覆盖四类 workload。异步 `ListAndWatch` 可以作为第五组后续兼容；在完成 generation前，feature gate必须强制使用同步 `GetAdvice`。
