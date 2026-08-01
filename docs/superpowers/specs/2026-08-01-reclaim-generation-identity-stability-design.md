# Reclaim CPU 身份稳定性最小方案

## 文档状态

- 文档类型：技术设计
- 目标组件：Katalyst QRM Dynamic Policy、Bulkhead CPUSet Topology、SysAdvisor CPU Advisor
- 评审基线：`ramp-up-reclaim-bulkhead-integration` worktree
- 证据来源：`reclaimobs0801` E2E 日志、QRM structured logs、Bulkhead DAG logs、当前代码
- 核心目标：稳定 reclaim CPU 身份，修复局部 NUMA 覆盖和 hard floor 被消费问题，并以最少抽象保证同步事务可靠
- 状态：修订后设计，可进入实现计划

本文收敛以下既有设计中的 reclaim identity、hard target 和 Bulkhead feedback 语义：

- `2026-07-31-ramp-up-reclaim-policy-complete-technical-design.md`
- `2026-07-30-ramp-up-reclaim-complete-design.md`
- `20260729-bulkhead-cpuset-phase-plan.md`

如本文与旧文档在数据模型、同步事务、CPU 身份选择或 hard floor 所有权上冲突，以本文为准。Bulkhead 的 parent bridge、release witness、child containment 和 cgroup 写序仍以 phase plan 为准。

## 结论

首版只保留五个机制：

```text
现有 state.TargetState
    唯一完整策略目标

DynamicPolicy 主锁
    串行 plan、materialize、commit、publish

Bulkhead 显式 target view
    不从共享 state 重建本轮目标

topology.DAGApplyResult
    唯一 convergence result

Manager latestAppliedReclaim
    只保存 identity selector 真正消费的 CPUSet
```

首版不新增：

- `CommitSnapshot` 或另一份完整 state DTO；
- `CandidateCommitter` 独立接口；
- `ReclaimGenerationCoordinator`；
- revision、target digest、active transaction；
- persisted applied state；
- compensation generation；
- 完整 `AppliedView` runtime store；
- `cpusetadjustment` 协议包；
- 新的 `RoundOutcome`；
- queue、worker、epoch 或 lease。

CPU 身份选择顺序：

```text
latest verified applied reclaim
→ current committed ReclaimRaw
→ fresh eligible CPUs
```

它只影响 identity preference，不改变 target size，不参与 ownership、release witness、containment 或安全排除。

## 现场证据

### Stable 与 recreate 的身份跳变

上一把稳定状态：

```text
ReclaimRaw:
17-26,49-55,113-122,145-151

reclaimed-0 observed:
17-26,113-122

reclaimed-1 observed:
49-55,145-151
```

recreate 后 QRM 在不到一秒内多次覆盖 raw：

```text
17-26,49-55,113-122,145-151
→ 1,49,97,145
→ 1,97
→ 1,49,97,145
→ 1,3-10,97,99-106
→ 6-10,102-106
```

根因：

1. hard target 不复用 verified applied identity；
2. NUMA0 局部结果全量覆盖 global reclaim entry；
3. Share/SNB pool 先消费 hard floor，reclaim 最终退化为差集。

### Topology 写入失败

Bulkhead 已从 primary 释放新 target，随后直接将完全不相交的新集合写入 `reclaimed-0/cpuset.cpus`，内核返回 `permission denied`。

Identity reuse 能减少 entering/leaving 集合，但不能替代：

- parent bridge；
- child containment；
- release witness；
- partial apply 后基于 fresh observed 的重规划。

## 设计目标

### 功能目标

1. target size 继续由 ratio、reserve、cap 和 reclaim eligibility 计算。
2. shrink 时优先从 verified applied reclaim 删除最少的完整 physical core。
3. grow 时保留仍 eligible 的 applied reclaim，只补差额。
4. NUMA0 局部更新不改变 NUMA1 old reclaim。
5. pool regeneration 不得消费当前请求的 hard floor。
6. Advisor 使用相同的 applied → raw → fresh 顺序。
7. Hard Partition admission 成功前，fresh observed 必须完整收敛。
8. topology 或 dependent plugin 失败不发布新的 applied reclaim。
9. candidate commit 失败时 live cache 与 checkpoint 均保持 Base。
10. 不因未来 queue 需求增加当前同步路径开销。

### 非目标

- 不改变 `CalculateRampUpReclaimTarget` 数量公式。
- 不实现 queue、worker、waiter 或 retry controller。
- 不持久化 revision、applied hint 或执行 phase。
- 不实现跨 reconcile 限速。
- 不替代 Bulkhead topology phase pipeline。
- 不把完整 global `ReclaimRaw` 当作当前请求 hard floor。

## 第一性原则

```text
Allocate 成功
⇒ topology fresh convergence 成功
且 candidate 已 durable commit
```

```text
FinalReclaimRaw ⊇ EffectiveHardFloor

EffectiveHardFloor =
    committed active RampUp NUMA reclaim
    ∪ current request floor
```

```text
Partial update 不改变未声明 NUMA
```

```text
Applied hint 只来自成功 materialization
```

```text
checkpoint 是重启恢复的唯一策略目标
```

revision、digest 和 transaction checkpoint 都不是这些不变量成立的必要条件。

## 现有代码复用

### 唯一完整目标：`state.TargetState`

当前代码已经存在：

```go
type TargetState struct {
    PodEntries   PodEntries
    MachineState NUMANodeMap
    NUMAHeadroom map[int]float64

    AllowSharedCoresOverlapReclaimedCores      bool
    DisableDedicatedCoresOverlapReclaimedCores bool
}
```

它与旧设计中的 `CommitSnapshot` 字段完全相同，因此不新增并列类型。

`TargetState` 的职责：

- planner 输入和输出；
- durable Base；
- candidate；
- checkpoint 序列化输入；
- Bulkhead view 构建输入。

需要补充：

```go
func (s *TargetState) Clone() *TargetState
```

`CPUPluginCheckpoint` 继续作为 wire DTO，保留 `PolicyName` 和 `Checksum`，不进入 planner 或 transaction。

```text
state.TargetState
    runtime canonical value
        ↓ serialize
CPUPluginCheckpoint
    disk wire format
```

### 内部 live cache

`cpuPluginStateData` 与 `TargetState` 字段相同，但它是 `cpuPluginState` 的私有存储布局，不再赋予新的业务语义。

首版不要求重命名或删除它，只增加两个 package-private primitive：

```go
func (s *cpuPluginState) snapshot() *TargetState

// stateCheckpoint owns next exclusively; do not clone it again.
func (s *cpuPluginState) replaceOwnedTarget(next *TargetState)
```

要求：

- `snapshot()` 一次 `RLock`，一次性 clone 五个字段；
- `replaceOwnedTarget()` 一次 `Lock`，原子替换五个字段；
- 不得通过五个 setter 拼装；
- 不得执行五次 `reflect.DeepEqual`；
- topology/socketTopology 不属于持久化 target。

### State 接口

直接把两个方法加入现有 `state.State`，不增加仅有一个实现者的 `CandidateCommitter`：

```go
type State interface {
    ReadonlyState
    writer

    PrepareDurableTarget() (*TargetState, error)
    CommitTarget(next *TargetState) error
}
```

生产实现仍是 `stateCheckpoint`；`cpuPluginState` 只是私有 cache，不要求实现公开 `State`。

memory state、fake 和 mock 按编译期接口实现，不允许运行时 type assertion 后回退旧 setter。

### Planner

当前 `planner.CPUStateSnapshot/CPUStateCandidate` 只有一个生产调用点，且 PodEntries 实际经历多次全量 clone：

```text
GetPodEntries clone
→ NewCPUStateCandidate clone
→ Materialize clone
→ SetPodEntries DeepEqual + clone
```

首版删除这两个中间表示，planner 直接使用 `TargetState`：

```go
func PlanRampUpReclaimPoolTarget(
    base *state.TargetState,
    update ReclaimTargetUpdate,
    currentFloor ReclaimHardConstraint,
    topology *machine.CPUTopology,
    hardPartitionEnabled bool,
) (*state.TargetState, error)
```

实现使用与修改粒度一致的 COW：

```text
shallow copy TargetState
→ copy PodEntries outer map
→ copy reclaim ContainerEntries
→ clone reclaim AllocationInfo
→ 仅 clone affected NUMA state
```

未修改字段继续引用 Base owned data，但 planner 返回后不允许任何 consumer 修改这些共享对象。进入 `CommitTarget` 时统一深拷贝一次取得 state 所有权。

## Durable Base 与原子提交

### 与 StoreState 的关系

三个公开方法复用同一个 package-private checkpoint primitive：

```go
func (sc *stateCheckpoint) writeTargetCheckpoint(
    target *TargetState,
) error {
    return sc.checkpointManager.CreateCheckpoint(
        sc.checkpointName,
        sc.checkpointFromTarget(target),
    )
}
```

它们的语义不同：

| 方法 | 输入 | checkpoint 失败后的 live cache | 用途 |
|---|---|---|---|
| `StoreState` | 已发布的当前 cache | cache 可能已被旧路径修改 | 启动兼容、测试和迁移期保存 |
| `PrepareDurableTarget` | 当前 cache 的单次完整快照 | 保持当前 cache，不进入 plan | 建立与 checkpoint 一致的事务 Base |
| `CommitTarget(next)` | 尚未发布的 candidate | 保持 durable Base | checkpoint 成功后原子发布新 target |

关系：

```text
                            writeTargetCheckpoint
                                      ▲
                 ┌────────────────────┼────────────────────┐
                 │                    │                    │
             StoreState      PrepareDurableTarget    CommitTarget
          保存已发布cache       建立durable Base       提交未发布candidate
```

`StoreState` 不能替代另外两个方法：

```text
SetPodEntries(next)
→ SetMachineState(next)
→ StoreState()
```

该顺序会在 checkpoint 写入前发布部分或完整 candidate；写入失败后可能出现：

```text
live cache = next
checkpoint = Base
```

`PrepareDurableTarget` 也不能实现成：

```text
StoreState()
→ 单独Snapshot()
```

两个操作之间无法证明没有状态变化，返回值不一定就是刚写入 checkpoint 的对象。

`CommitTarget` 必须保持：

```text
clone candidate取得所有权
→ writeTargetCheckpoint
→ 成功后replaceOwnedTarget
```

因此生产事务中的替代关系是：

```text
旧：Set* → StoreState

新：PrepareDurableTarget
    → 在owned TargetState上plan
    → materialize
    → CommitTarget
```

`StoreState` 保留为兼容 API，但退出 Allocate、Resize、Remove、Advisor、IRQ 和 periodical 的成功路径。

### Dirty 标记

为避免每次 transaction 无条件写两次 checkpoint，`stateCheckpoint` 增加纯内存标记：

```go
type stateCheckpoint struct {
    sync.RWMutex

    cache        *cpuPluginState
    cacheDurable bool

    // existing fields...
}
```

规则：

- checkpoint restore 成功后 `cacheDurable=true`；
- 首次启动没有旧 checkpoint 时，初始化 checkpoint 成功后同样设置 `cacheDurable=true`；
- 任意 cache mutation 必须在修改前设为 `false`；
- `SetPodEntries`、`SetMachineState`、`SetNUMAHeadroom`、两个 overlap setter、`SetAllocationInfo`、`Delete`、`ClearState` 全部遵守同一规则；
- persist setter 和 `StoreState` 只有 checkpoint 成功后才能设为 `true`；
- checkpoint 写失败保持 `false`；
- `CommitTarget` 成功后设为 `true`。

该标记不是 generation，不持久化，不参与业务判断，只用于避免重复 I/O。

### PrepareDurableTarget

```go
func (sc *stateCheckpoint) PrepareDurableTarget() (
    *TargetState,
    error,
) {
    sc.Lock()
    defer sc.Unlock()

    base := sc.cache.snapshot()
    if sc.cacheDurable {
        return base, nil
    }

    if err := sc.writeTargetCheckpoint(base); err != nil {
        return nil, err
    }
    sc.cacheDurable = true
    return base, nil
}
```

重要合同：

- plan 必须基于该方法返回的 owned Base；
- 不得先 `StoreState()` 再单独 `Snapshot()`；
- 返回值与刚确认的 checkpoint 精确一致；
- 失败时不得进入 plan/materialize。

### CommitTarget

```go
func (sc *stateCheckpoint) CommitTarget(
    next *TargetState,
) error {
    sc.Lock()
    defer sc.Unlock()

    // Take the only full defensive clone before persistence.
    owned := next.Clone()

    if err := sc.writeTargetCheckpoint(owned); err != nil {
        return err
    }

    sc.cache.replaceOwnedTarget(owned)
    sc.cacheDurable = true
    return nil
}
```

### StoreState 兼容实现

```go
func (sc *stateCheckpoint) StoreState() error {
    sc.Lock()
    defer sc.Unlock()

    current := sc.cache.snapshot()
    if err := sc.writeTargetCheckpoint(current); err != nil {
        sc.cacheDurable = false
        return err
    }

    sc.cacheDurable = true
    return nil
}
```

约束：

- `StoreState` 只序列化当前 cache，不替换 cache；
- 成功后设置 `cacheDurable=true`；
- 失败后保持 `cacheDurable=false`；
- 调用方必须接收并传播 error；
- 不得用它提交一个尚未发布的 candidate；
- 不得在业务已返回成功后依赖下一次 `PrepareDurableTarget` 补写。

正确性：

- checkpoint 直接从 owned candidate 构造；
- checkpoint 成功前不修改 live cache；
- 失败时 Base cache 和 checkpoint 均不变；
- 成功后 cache 一次性替换，无中间混合状态；
- `PolicyName` 由 `stateCheckpoint` 提供；
- checksum 继续由 `CPUPluginCheckpoint.MarshalCheckpoint` 生成。

性能：

- candidate 只做一次完整 defensive clone；
- cache 接管 owned target，不再 clone；
- 不调用五个 setter；
- 不执行全量 `reflect.DeepEqual`；
- durable Base 未 dirty 时不重复写 checkpoint。

### 旧 setter 边界

旧 setter 可暂时保留给测试和启动兼容代码，但不得再承载任何会决定运行时业务成功的 mutation。

以下生产路径必须全部改为“基于 `TargetState` 规划 + `CommitTarget`”：

- Allocate，包括本次 container allocation 和 reclaim/pool 调整；
- Resize；
- Remove；
- `GetResourcesAllocation`，该接口会结束 RampUp 并调整 pool，属于 mutation API；
- Advisor applyBlocks/headroom/overlap flags；
- pool regeneration；
- reclaim initialization；
- IRQ Tuner `SetExclusiveIRQCPUSet`；
- `clearResidualState`；
- system-exclusive pool sync；
- resource-package pinned CPUSet sync；
- runtime config 导致的 overlap state 更新。

硬约束：

- `PrepareDurableTarget` 必须发生在本次请求第一次修改 live cache 之前；
- 本次 container allocation 也必须写入 `next TargetState`，不得先调用 `updateAllocationInfo(..., false)`；
- 持有 `DynamicPolicy` 主锁期间，禁止其它路径写 transaction-owned 字段；
- 现有 `Allocate` 尾部忽略 `StoreState` 错误的路径必须移除；
- 旧 setter 的 `persist=true` 不得出现在运行时成功路径；
- 所有旧 writer 在任何 mutation 前都必须设置 `cacheDurable=false`；
- `ClearState`、`SetAllocationInfo`、`Delete` 和 `StoreState` 必须有专项测试。

### 并发 writer 收口

`DynamicPolicy` 主锁是所有 CPU target mutation 的唯一串行化边界。

特别处理：

- `SetExclusiveIRQCPUSet` 必须取得同一把主锁；
- IRQ target 在 `TargetState` 上规划并通过一次 `CommitTarget` 提交；
- `GetResourcesAllocation` 取得主锁后先执行 readiness 检查；
- RampUp 完成、pool 调整、PodEntries 和 MachineState 必须合并为一个 target；
- `clearResidualState`、system-exclusive 和 resource-package periodical 在 owned target 上规划；
- 禁止 IRQ、Advisor、periodical 或 query-named mutation API 直接调用持久化 setter；
- 任何未取得主锁的 state writer 都视为实现缺陷。

否则会发生：

```text
Allocate 读取 Base
→ IRQ 并发提交新 interrupt pool
→ Allocate 提交旧 Base 派生 candidate
→ IRQ 更新丢失
```

`cacheDurable` 不能把一个已向调用方返回成功、但 checkpoint 写失败的 mutation 变成可靠事务。可靠性只能来自调用方收到 `CommitTarget` 的 error。

## Full 与 Partial 更新

```go
type ReclaimUpdateMode string

const (
    ReclaimUpdateFull    ReclaimUpdateMode = "full"
    ReclaimUpdatePartial ReclaimUpdateMode = "partial"
)

type ReclaimTargetUpdate struct {
    Mode          ReclaimUpdateMode
    AffectedNUMAs sets.Set[int]
    Target        machine.CPUSet
}
```

merge 规则：

```go
func mergeReclaimTarget(
    old machine.CPUSet,
    update ReclaimTargetUpdate,
    topology *machine.CPUTopology,
) (machine.CPUSet, error) {
    if update.Mode == ReclaimUpdateFull {
        return update.Target.Clone(), nil
    }
    if update.Mode != ReclaimUpdatePartial {
        return machine.NewCPUSet(), ErrUnknownUpdateMode
    }
    if update.AffectedNUMAs.Len() == 0 {
        return machine.NewCPUSet(), ErrMissingAffectedNUMAs
    }

    affectedCPUs := cpusInNUMAs(topology, update.AffectedNUMAs)
    if !update.Target.IsSubsetOf(affectedCPUs) {
        return machine.NewCPUSet(), ErrTargetOutsideAffectedNUMAs
    }

    merged := old.Clone()
    for numaID := range update.AffectedNUMAs {
        numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
        merged = merged.
            Difference(numaCPUs).
            Union(update.Target.Intersection(numaCPUs))
    }
    return merged, nil
}
```

语义：

- affected NUMA 且 target 为空：明确清空；
- 未声明 NUMA：保留 old raw；
- Partial target 含未声明 NUMA CPU：返回错误；
- Full：完整替换；
- 输出始终是完整 global raw。

## Hard floor

```go
type ReclaimHardConstraint struct {
    CPUs          machine.CPUSet
    AffectedNUMAs sets.Set[int]
    OwnerPodUID   string
}
```

不持久化 owner-specific floor。已提交 RampUp 的保护集合从现有 `RampUp`、`TopologyAwareAssignments` 和 committed `ReclaimRaw` 保守派生；本次尚未提交的 floor 继续作为 plan 局部参数。

```go
func effectiveHardFloor(
    base *state.TargetState,
    current ReclaimHardConstraint,
    topology *machine.CPUTopology,
    hardPartitionEnabled bool,
) machine.CPUSet {
    if !hardPartitionEnabled {
        return machine.NewCPUSet()
    }

    activeNUMAs := sets.New[int]()
    hasActiveRampUp := false

    for _, info := range base.PodEntries.AllocationInfos() {
        if !info.RampUp {
            continue
        }
        hasActiveRampUp = true
        for numaID := range info.TopologyAwareAssignments {
            activeNUMAs.Insert(numaID)
        }
    }

    committedRaw := base.PodEntries.
        GetCPUSetForPool(consts.PoolNameReclaim)

    activeFloor := machine.NewCPUSet()
    switch {
    case !hasActiveRampUp:
    case activeNUMAs.Len() == 0:
        // Fail safe when topology metadata is incomplete.
        // Preserve the entire committed reclaim raw set.
        activeFloor = committedRaw
    default:
        activeFloor = committedRaw.Intersection(
            cpusInNUMAs(topology, activeNUMAs),
        )
    }

    return activeFloor.Union(current.CPUs)
}
```

语义：

- 不新增 checkpoint 字段、constraint store 或 lifecycle 状态机；
- 只有 Hard Partition enabled 时才派生 active floor；
- disable/reset 路径强制传入 `false`，即使仍存在 `RampUp=true` owner；
- active RampUp 的事实源是 committed `AllocationInfo.RampUp`；
- active NUMA 从现有 `TopologyAwareAssignments` 提取；
- committed floor 是 active NUMA 上的当前 `ReclaimRaw`；
- current request floor 只补充尚未 commit 的本次 RampUp；
- 所有会 regenerate pool 的 transaction 都必须使用 effective floor；
- 不把未受影响 NUMA 的普通 reclaim 预算升级为 floor。

pool regeneration：

```go
hardFloor := effectiveHardFloor(
    base,
    currentRequestFloor,
    topology,
    hardPartitionEnabled,
)
poolAvailable := availableCPUs.Difference(hardFloor)

candidate := regeneratePools(base, poolAvailable)

if !hardFloor.IsSubsetOf(candidate.ReclaimRaw()) {
    return nil, ErrHardFloorDropped
}
```

生命周期：

```text
RampUp admission commit
    owner AllocationInfo.RampUp=true
    TopologyAwareAssignments记录active NUMA

后续IRQ/resource-package/system-exclusive/Advisor等事务
    保护active NUMA上的committed ReclaimRaw

RampUp expiry
    stable Advisor candidate清除owner的RampUp
    只有该candidate成功materialize并CommitTarget后才释放
```

禁止仅因原 admission 函数返回就释放 floor。

该方案在 RampUp 窗口内可能保护多于原 admission floor 的 CPU，但不会提前释放 Hard Partition；同时保持 checkpoint schema 不变，并天然兼容旧 checkpoint。

## CPU 身份选择

对每个 NUMA：

```go
appliedTier := latestAppliedReclaim.
    Intersection(eligibleInNUMA)

rawTier := currentCommittedRaw.
    Intersection(eligibleInNUMA).
    Difference(appliedTier)

freshTier := eligibleInNUMA.
    Difference(appliedTier).
    Difference(rawTier)
```

复用现有 tiered helper：

```go
selected, _, err := p.takeByTieredPreferredCPUs(
    eligibleInNUMA,
    []machine.CPUSet{
        appliedTier,
        rawTier,
    },
    target,
)
```

### Shrink

```text
old applied NUMA0: 17-26,113-122
target size:       18
selected:          17-25,113-121
removed:           26,122
added:             empty
```

### Grow

保留所有仍 eligible 的 applied CPU，只从 raw/fresh 补差额。

### SMT

完整 physical core 优先。无法由完整 core 满足时允许 partial core，但必须：

- 精确满足 target size；
- 最小化被拆分的 physical core；
- 使用 topology sibling 关系；
- 覆盖 SMT1、SMT2、SMT4。

## Bulkhead 最小接口

### View 构建

保留现有 `CPUSetPartitionView`，但增加直接消费 target 的函数：

```go
func BuildCPUSetPartitionViewFromTarget(
    target *state.TargetState,
    topology *machine.CPUTopology,
    opts CPUSetPartitionViewOptions,
) *CPUSetPartitionView
```

不新增只有一个实现的 builder interface。

实现要求：

- 直接只读 `TargetState` 公有字段；
- 不通过 `ReadonlyState.GetPodEntries()` 再做全量 clone；
- 每个 transaction 只构建一次；
- QRM 构建后将 view 所有权转移给 Manager；
- QRM 不再读取或修改该 view；
- Manager 不从 shared state 重建 view。

### HandlerContext

复用现有 `bulkhead/api.HandlerContext`，删除重复的 `dynamicpolicy/util.CPUSetAdjustmentHandlerCtx`：

```go
type HandlerContext struct {
    CoreConf    *config.Configuration
    DynamicConf *dynamicconfig.Configuration
    Emitter     metrics.MetricEmitter
    MetaServer  *metaserver.MetaServer
    Topology    *machine.CPUTopology

    View *bulkheadutils.CPUSetPartitionView
}
```

不新增 request/result envelope、revision 或 digest。

### Topology result

直接复用：

```go
type DAGApplyResult struct {
    Attempted         int
    Applied           int
    Skipped           int
    Failed            int
    Deferred          int
    FullyConverged    bool
    ConvergenceReport ConvergenceReport
}
```

Topology plugin：

```go
type TopologyPlugin interface {
    Plugin
    ReconcileTopology(
        context.Context,
        HandlerContext,
    ) (topology.DAGApplyResult, error)
}
```

`Manager.Apply` 的统一成功合同是：

```text
error == nil
⇒ DAGApplyResult.FullyConverged == true
且所有 dependent plugin 成功
```

不存在 best-effort 调用模式，因此不增加 `RequireConverged` 开关。`FullyConverged=false` 必须转换为 typed error，禁止继续使用当前 `return nil`。

统一 helper：

```go
func requireFullyConverged(
    result topology.DAGApplyResult,
    err error,
) error {
    if err != nil {
        return err
    }
    if !result.FullyConverged {
        return &ErrTopologyNotConverged{
            Report: result.ConvergenceReport,
        }
    }
    return nil
}
```

该 helper 在 Manager 内统一调用。调用方只判断 `error`，不得自行决定 non-converged 是否可接受。

### Complete controlled-rel 验证

当前 DAG convergence 只能证明 DAG 中实际出现的节点已收敛。空 per-NUMA bucket 若在 target 构建时被跳过，不能据此宣称完整收敛。

每次 `Manager.Apply` 都必须：

1. 枚举完整 controlled rel 集，包括期望为空的 reclaimed bucket；
2. fresh read 每个 rel；
3. 逐 rel 比较 observed 与 target；
4. 任何 missing、extra、unreadable 或 mismatch 都使 `FullyConverged=false`；
5. dynamic descendant 仍按 phase plan 的 complete snapshot/identity 规则处理。

### Manager

```go
func (m *Manager) Apply(
    ctx context.Context,
    in bulkheadapi.HandlerContext,
) (
    appliedReclaim machine.CPUSet,
    result topology.DAGApplyResult,
    err error,
)
```

执行顺序：

```text
唯一 enabled TopologyPlugin
→ ReconcileTopology
→ 强制 FullyConverged
→ dependent plugins 消费本轮 owned View
→ 全部成功后返回 verified reclaim CPUSet
```

任一 plugin 失败：

- 不发布 applied hint；
- 调用方恢复 Base；
- 返回带 plugin name 的 typed error。

### Applied hint

Selector 只消费 reclaim identity，因此 Manager 只保存：

```go
type Manager struct {
    // existing fields...

    appliedMu            sync.RWMutex
    latestAppliedReclaim machine.CPUSet
    appliedValid         bool
}

func (m *Manager) PublishAppliedReclaim(
    cpus machine.CPUSet,
)

func (m *Manager) LatestAppliedReclaim() (
    machine.CPUSet,
    bool,
)
```

要求：

- publish 时 clone 一次；
- getter clone 一次；
- 不保存完整 `CPUSetPartitionView`；
- 不持久化；
- restart 后首次 successful reconcile 才 valid；
- selector 始终与 current eligible 求交。

## 同步事务

### 总体流程

```text
DynamicPolicy 主锁
    ↓
PrepareDurableTarget
    ↓
Plan next TargetState
    ↓
BuildCPUSetPartitionViewFromTarget 一次
    ↓
Manager.Apply
    ↓
要求 DAGApplyResult.FullyConverged
且 dependent plugins 全成功
    ↓
CommitTarget
    ↓
PublishAppliedReclaim
    ↓
Advisor post-commit notification
    ↓
Allocate success
```

### 伪代码

Allocate 入口必须先建立 Base，再做任何本次请求的计算：

```go
func (p *DynamicPolicy) Allocate(
    ctx context.Context,
    req *pluginapi.ResourceRequest,
) (*pluginapi.ResourceAllocationResponse, error) {
    p.Lock()
    defer p.Unlock()

    if !p.cpuPolicyReady() {
        return nil, ErrCPUPolicyRecovering
    }

    base, err := p.state.PrepareDurableTarget()
    if err != nil {
        return nil, err
    }

    // Do not call updateAllocationInfo, SetPodEntries, SetMachineState,
    // or any other live-state writer before planning completes.
    next, response, err := p.planAllocationOnTarget(base, req)
    if err != nil {
        return nil, err
    }

    if err := p.materializeCommitAndPublish(ctx, base, next); err != nil {
        return nil, err
    }

    p.notifyAdvisorAfterCommit(req)
    return response, nil
}
```

`planAllocationOnTarget` 必须把以下内容放入同一个 `next`：

- 本次 container allocation；
- MachineState；
- reclaim Full/Partial merge；
- 其它 pools；
- NUMAHeadroom；
- overlap flags。

不得在事务中途通过旧 setter 发布任一中间态。

核心事务：

```go
func (p *DynamicPolicy) materializeCommitAndPublish(
    ctx context.Context,
    base *state.TargetState,
    next *state.TargetState,
) error {
    view := bulkheadutils.BuildCPUSetPartitionViewFromTarget(
        next,
        p.machineInfo.CPUTopology,
        p.bulkheadViewOptions(),
    )

    appliedReclaim, _, err := p.bulkheadManager.Apply(
        ctx,
        bulkheadapi.HandlerContext{
            CoreConf:         p.conf,
            DynamicConf:      p.dynamicConf,
            Emitter:          p.emitter,
            MetaServer:       p.metaServer,
            Topology:         p.machineInfo.CPUTopology,
            View:             view,
        },
    )
    if err != nil {
        return errors.Join(
            err,
            p.restoreCommittedTarget(ctx, base),
        )
    }

    if err := p.state.CommitTarget(next); err != nil {
        return errors.Join(
            err,
            p.restoreCommittedTarget(ctx, base),
        )
    }

    p.bulkheadManager.PublishAppliedReclaim(appliedReclaim)
    return nil
}
```

### Base 恢复

```go
func (p *DynamicPolicy) restoreCommittedTarget(
    ctx context.Context,
    base *state.TargetState,
) error {
    view := bulkheadutils.BuildCPUSetPartitionViewFromTarget(
        base,
        p.machineInfo.CPUTopology,
        p.bulkheadViewOptions(),
    )

    appliedReclaim, _, err := p.bulkheadManager.Apply(
        ctx,
        bulkheadapi.HandlerContext{
            // same context...
            View: view,
        },
    )
    if err != nil {
        p.blockCPUPolicy()
        return err
    }

    p.bulkheadManager.PublishAppliedReclaim(appliedReclaim)
    return nil
}
```

恢复语义：

- Base 已是 durable checkpoint，不修改 state；
- 不创建 compensation generation；
- 不保存 active marker；
- 恢复失败时阻止整个 CPU policy；
- restart 继续向 checkpoint target 收敛。

### 崩溃窗口

```text
materialize前崩溃
    checkpoint = Base

materialize后、commit前崩溃
    checkpoint = Base
    restart恢复Base

commit后、publish前崩溃
    checkpoint = next
    restart收敛next并重建applied hint

publish后崩溃
    checkpoint = next
    runtime hint丢失但不影响安全
```

无需 transaction checkpoint。

### Advisor post-commit notification

`advisorClient.AddContainer` 是通知，不属于 CPUSet 安全事务：

- 失败不得改判 admission；
- 不得调用 `removeContainer` 或独立 `StoreState`；
- 当前进程 bounded retry；
- restart 从 committed `PodEntries` 重建 registration。

如果未来 Advisor ack 必须成为 admission 前置条件，需要单独设计可回滚 external operation。

## Startup recovery

```go
type CPUPolicyState string

const (
    CPUPolicyRecovering CPUPolicyState = "recovering"
    CPUPolicyReady      CPUPolicyState = "ready"
    CPUPolicyBlocked    CPUPolicyState = "blocked"
)
```

该状态由 `DynamicPolicy` 主锁保护：

- 构造完成后初始为 `recovering`；
- 只有 `recoverReclaim` 完整成功后切换为 `ready`；
- 任一运行期 Base 恢复失败切换为 `blocked`；
- `blocked` 不自动回到 `ready`，只能由一次完整 recovery 成功恢复；
- 所有 CPU mutation 入口在取得主锁后统一调用 `requireCPUPolicyReady()`。

```go
func (p *DynamicPolicy) recoverReclaim(
    ctx context.Context,
) error {
    p.Lock()
    defer p.Unlock()

    p.cpuPolicyState = CPUPolicyRecovering

    target, err := p.state.PrepareDurableTarget()
    if err != nil {
        p.cpuPolicyState = CPUPolicyBlocked
        return err
    }

    view := bulkheadutils.BuildCPUSetPartitionViewFromTarget(
        target,
        p.machineInfo.CPUTopology,
        p.bulkheadViewOptions(),
    )

    appliedReclaim, _, err := p.bulkheadManager.Apply(
        ctx,
        bulkheadapi.HandlerContext{
            View: view,
            // other context...
        },
    )
    if err != nil {
        p.cpuPolicyState = CPUPolicyBlocked
        return err
    }

    p.bulkheadManager.PublishAppliedReclaim(appliedReclaim)
    // Stay in recovering. Start publishes ready only after all workers start.
    return nil
}
```

恢复是整个 CPU policy 的 readiness barrier。在 `recoverReclaim` 返回真实 `FullyConverged=true` 前：

- 不注册或不开放 Allocate、Resize、Remove；
- 不开放 dedicated/shared/SNB admission；
- 不启动 Advisor apply；
- 不执行会修改 PodEntries/MachineState 的 periodical；
- health/readiness 保持 fail closed。

统一门禁覆盖：

```text
GetTopologyHints / future GetPodTopologyHints
Allocate / Resize / Remove / GetResourcesAllocation
Advisor apply
system-exclusive / residual / pool regeneration periodical
resource-package pinned CPUSet periodical
IRQ Tuner SetExclusiveIRQCPUSet
runtime overlap config mutation
reclaim init/reset retry
```

已经启动的 periodical 取得主锁后发现非 `ready`，必须跳过本轮且不得修改 state。Advisor callback 同样返回 typed unavailable error。

`GetTopologyHints` 虽不修改 state，但属于 admission 前置步骤：

- 必须在读取/计算 hint 前检查 readiness；
- 门禁必须位于 init/debug Pod 的提前返回之前；
- `recovering/blocked` 返回 typed unavailable；
- 禁止先返回可用 hint，再由 Allocate 拒绝。

仅只读诊断接口可以开放。

### 启动顺序

Recovery 必须在任何并发 writer 或 mutation API 开放前执行。现有 constructor 中的 `cleanPools/initReservePool/initReclaimPool/initInterruptPool` 不得在 durable target recovery 前直接修改 live state。

固定顺序：

```text
1. restore checkpoint
2. construct DynamicPolicy with state=recovering
3. do not register/open mutation RPC
4. do not start IRQ Tuner, Advisor or periodical
5. build one bootstrap TargetState when schema/config migration is required
6. Manager.Apply bootstrap/committed target
7. require FullyConverged
8. CommitTarget only when bootstrap target differs from checkpoint
9. PublishAppliedReclaim
10. set state=ready
11. register/open mutation RPC
12. start IRQ Tuner, Advisor and periodical
```

Bootstrap 规则：

- 无迁移时直接 materialize restored target，不重写 checkpoint；
- pool init/cleanup 必须在 owned `TargetState` 上完成；
- 所有 bootstrap 变化一次 `CommitTarget`；
- bootstrap apply/commit 失败时 constructor/Start 返回错误，或保持进程存活但 `blocked`；
- 不允许默认 `ready`；
- runtime reclaim init/reset retry 只能在 `ready` 后走普通事务。

启动入口必须有一个唯一调用点：

```go
func (p *DynamicPolicy) Start() error {
    if err := p.bootstrapAndRecover(p.startContext()); err != nil {
        p.setCPUPolicyState(CPUPolicyBlocked)
        return err
    }

    started, err := p.startBackgroundWorkers()
    if err != nil {
        stopInReverseOrder(started)
        p.setCPUPolicyState(CPUPolicyBlocked)
        return err
    }

    // The wrapper serves mutation RPCs only after Start returns nil.
    p.setCPUPolicyState(CPUPolicyReady)
    return nil
}
```

该签名保持现有 `skeleton.GenericPlugin` 合同。`bootstrapAndRecover` 内部完成上述状态转换；禁止其它代码直接设置 `cpuPolicyState=ready`。

Wrapper 可能执行 `Stop → Start` 或重试失败的 `Start`，因此：

- `bootstrapAndRecover` 必须可重入、幂等；
- 若已 `ready` 且 target/worker 状态完整，可安全 no-op；
- 若上次停在 `recovering/blocked`，重新从 durable target 完整恢复；
- `startBackgroundWorkers` 返回已成功启动组件的 stopper；
- 后续组件启动失败时逆序 stop；
- 全部 worker 成功后才发布 `ready`；
- 后台 worker 不得重复注册或启动；
- `Stop` 必须关闭 worker 并将 runtime readiness 退出 ready。

不可重用组件必须由 `Start` 创建新实例，不能复用已关闭 channel、context 或 goroutine。

## Advisor 物化

逐 NUMA：

```text
current request hard floor
→ verified applied reclaim
→ committed desired raw
→ fresh eligible
```

```text
deficit[n] = requiredFloor[n] - currentReclaim[n].Size
```

禁止：

- 使用所有 NUMA global union 判空；
- union 静态 reserved seed；
- 把 global raw 当作当前请求 hard floor。

Advisor 产生的 PodEntries、MachineState、NUMAHeadroom 和 overlap flags 必须合并到同一个 `TargetState`，一次 `CommitTarget`，不得拆成多次 checkpoint。

## Disable 与 reset

Disable/reset 继续同步执行：

```text
build reset TargetState
→ Manager.Apply(reset view)
→ 强制 FullyConverged
→ CommitTarget
→ PublishAppliedReclaim(reset reclaim)
```

reset partial/error：

- 不提交 target；
- 不发布 applied hint；
- 恢复 Base；
- 下次启动或 periodical 重试。

## 未来异步扩展

当前只预留所有权边界，不预留字段：

```text
TargetState = durable latest desired
Manager.Apply = materializer
DAGApplyResult = convergence evidence
latestAppliedReclaim = runtime hint
```

未来引入 capacity-1 queue 时：

```text
CommitTarget(latest desired)
→ non-blocking notify
→ single worker读取latest TargetState
→ Manager.Apply
→ publish applied reclaim
```

届时再增加进程内 revision。只有跨进程 completion、持久化 waiter 或多 writer 时才增加 epoch/fencing。

不提前增加 revision/digest 的原因：

- 当前同步调用栈天然绑定 request/result；
- 回显 revision/digest 不能证明 plugin 使用了目标；
- fresh convergence evidence 更强；
- 对完整 view 做 digest 会重复 map 排序和 CPUSet canonicalization；
- 内部接口未来可安全演进。

## 本次修改点

| 修改 | 调整后 |
|---|---|
| 删除 `RequireConverged` | `Manager.Apply` 没有 best-effort 模式；`error == nil` 强制蕴含 `FullyConverged=true` |
| 收口 convergence 判断 | `requireFullyConverged` 仅由 Manager 内部调用，业务调用方只判断 error |
| 删除 `RampUpHardReclaim` | 不修改 `AllocationInfo` 和 checkpoint schema |
| active floor 派生 | 使用 active RampUp 的 `TopologyAwareAssignments` 定位 NUMA，再与 committed `ReclaimRaw` 相交 |
| metadata 缺失 | 存在 RampUp 但无法确定 NUMA 时，fail safe 保护全部 committed `ReclaimRaw` |
| current request floor | 继续作为纯 plan 参数，与 committed active floor 取 union |
| floor 释放 | owner 的 `RampUp` 经 stable transaction 成功提交后自然释放对应 NUMA |
| 配置边界 | Hard Partition disabled 或 reset 时 effective floor 为空 |
| 明确 `StoreState` 定位 | 仅保存已发布 cache，保留给启动兼容、测试和迁移期代码 |
| 建立事务 Base | 使用 `PrepareDurableTarget`，不再采用 `StoreState + Snapshot` |
| 提交 candidate | 使用 checkpoint-first 的 `CommitTarget`，替代 `Set* → StoreState` |
| 统一写盘实现 | 三个方法共用 package-private `writeTargetCheckpoint` |
| 接口集中 | 新增“最终接口定义”章节，集中给出 State、Planner、Bulkhead 和 readiness API |

## 最终接口定义

### State

复用现有 `state.TargetState`，不新增等价 DTO：

```go
type State interface {
    ReadonlyState
    writer // Includes StoreState, retained only as a compatibility API.

    // PrepareDurableTarget returns an owned snapshot matching the checkpoint.
    PrepareDurableTarget() (*TargetState, error)

    // CommitTarget writes the checkpoint before atomically replacing live cache.
    CommitTarget(next *TargetState) error
}

func (s *TargetState) Clone() *TargetState
```

`cpuPluginState` 仅提供 package-private 原语：

```go
func (s *cpuPluginState) snapshot() *TargetState
func (s *cpuPluginState) replaceOwnedTarget(next *TargetState)
```

`stateCheckpoint` 共享内部写盘原语：

```go
func (sc *stateCheckpoint) writeTargetCheckpoint(
    target *TargetState,
) error
```

State 方法的最终使用边界：

```text
StoreState
    启动兼容 / 测试 / 迁移期保存当前cache

PrepareDurableTarget
    每个runtime transaction开始时建立Base

CommitTarget
    每个runtime transaction唯一提交入口
```

### Planner

```go
type ReclaimUpdateMode string

const (
    ReclaimUpdateFull    ReclaimUpdateMode = "full"
    ReclaimUpdatePartial ReclaimUpdateMode = "partial"
)

type ReclaimTargetUpdate struct {
    Mode          ReclaimUpdateMode
    AffectedNUMAs sets.Set[int]
    Target        machine.CPUSet
}

type ReclaimHardConstraint struct {
    // Represents only the current request floor that is not committed yet.
    CPUs          machine.CPUSet
    AffectedNUMAs sets.Set[int]
    OwnerPodUID   string
}

func PlanRampUpReclaimPoolTarget(
    base *state.TargetState,
    update ReclaimTargetUpdate,
    currentFloor ReclaimHardConstraint,
    topology *machine.CPUTopology,
    hardPartitionEnabled bool,
) (*state.TargetState, error)
```

Planner 内部通过下列纯函数合并 committed active floor 和当前 request floor：

```go
func effectiveHardFloor(
    base *state.TargetState,
    current ReclaimHardConstraint,
    topology *machine.CPUTopology,
    hardPartitionEnabled bool,
) machine.CPUSet
```

### Bulkhead view

```go
func BuildCPUSetPartitionViewFromTarget(
    target *state.TargetState,
    topology *machine.CPUTopology,
    opts CPUSetPartitionViewOptions,
) *CPUSetPartitionView
```

```go
type HandlerContext struct {
    CoreConf    *config.Configuration
    DynamicConf *dynamicconfig.Configuration
    Emitter     metrics.MetricEmitter
    MetaServer  *metaserver.MetaServer
    Topology    *machine.CPUTopology

    // Manager owns View for the duration of this call.
    View *bulkheadutils.CPUSetPartitionView
}
```

### Bulkhead plugin 与 Manager

```go
type TopologyPlugin interface {
    Plugin

    ReconcileTopology(
        context.Context,
        HandlerContext,
    ) (topology.DAGApplyResult, error)
}
```

```go
type ErrTopologyNotConverged struct {
    Report topology.ConvergenceReport
}
```

```go
func (m *Manager) Apply(
    ctx context.Context,
    in bulkheadapi.HandlerContext,
) (
    appliedReclaim machine.CPUSet,
    result topology.DAGApplyResult,
    err error,
)

// Strong Apply contract:
// err == nil implies full convergence and successful dependent plugins.
```

```go
func (m *Manager) PublishAppliedReclaim(
    cpus machine.CPUSet,
)

func (m *Manager) LatestAppliedReclaim() (
    machine.CPUSet,
    bool,
)
```

### DynamicPolicy readiness

```go
type CPUPolicyState string

const (
    CPUPolicyRecovering CPUPolicyState = "recovering"
    CPUPolicyReady      CPUPolicyState = "ready"
    CPUPolicyBlocked    CPUPolicyState = "blocked"
)

func (p *DynamicPolicy) Start() error
func (p *DynamicPolicy) Stop() error
func (p *DynamicPolicy) requireCPUPolicyReady() error
```

上述接口是同步首版的完整新增面，不包含 queue、revision、digest、`RequireConverged` 或 `RampUpHardReclaim`。

## 新旧方案差异

| 维度 | 旧 generation 方案 | 上一版最小方案 | 当前代码优先方案 |
|---|---|---|---|
| 完整目标类型 | generation payload | `CommitSnapshot` + planner snapshot | 复用 `state.TargetState` |
| state 内部 | 新 commit DTO | `CommitSnapshot` | `cpuPluginStateData` 保持私有布局 |
| planner | generation candidate | `CPUStateSnapshot/CPUStateCandidate` | 直接输入输出 `TargetState` |
| planner COW | 声称分片 COW | PodEntries 多次全量 clone | 只复制 reclaim 分片和 affected NUMA |
| state API | generation repository CAS | `CandidateCommitter` | `State.PrepareDurableTarget/CommitTarget` |
| `StoreState` | generation store取代旧接口 | 定位不明确 | 保留兼容保存，不进入runtime transaction |
| durable Base | generation checkpoint | 每次强制 checkpoint | `cacheDurable` 避免重复写 |
| commit | generation envelope | `CommitSnapshot` clone/replace | 一次 defensive clone + ownership transfer |
| request identity | persisted generation | memory revision + digest | 同步调用栈，无额外字段 |
| digest | committed/view digest | view digest | 删除，复用 fresh convergence |
| handler 包 | coordinator protocol | 新 `cpusetadjustment` 包 | 复用 `bulkhead/api` |
| handler context |多层 context | request/context/result | 单个 `bulkhead/api.HandlerContext` |
| result |新 `RoundOutcome` | `cpusetadjustment.Result` | 复用 `DAGApplyResult` |
| convergence 模式 | generation phase 可 deferred | `RequireConverged` 开关 | Manager 成功统一等价于 fully converged |
| target view | Manager 从 state 构建 | immutable clone 多次传递 | QRM 构建一次并转移所有权 |
| applied state | persisted AppliedView |完整 runtime AppliedView store | 只保存 `machine.CPUSet` reclaim hint |
| active RampUp floor | 独立 constraint 状态 | `AllocationInfo.RampUpHardReclaim` | active NUMA 上的 committed ReclaimRaw |
| plugin failure | phase/compensation | unpublished result + Base restore | typed error + Base restore |
| Advisor state | generation payload |完整 candidate | 一个 `TargetState` 一次提交 |
|异步扩展 |首版预建 |预留 revision/digest |只预留 owner 边界，按需加字段 |

## 文件级修改

| 文件 | 修改 |
|---|---|
| `state/target_state.go` | 增加 `Clone`，明确 canonical target |
| `state/state.go` | 不增加 RampUp 字段；`State` 增加 prepare/commit |
| `state/state_mem.go` | 增加 `snapshot/replaceOwnedTarget` |
| `state/state_checkpoint.go` | 三个方法复用 `writeTargetCheckpoint`；增加 `cacheDurable` 和原子 prepare/commit |
| `planner/ramp_up_reclaim_admission.go` | 直接输入输出 `TargetState`；Full/Partial merge |
| `planner/snapshot.go` | 删除 |
| `planner/candidate.go` | 删除 |
| `policy_allocation_handlers.go` | hard floor、pool 保护、applied/raw/fresh selector |
| `policy_advisor_handler.go` | 单 target 物化、per-NUMA deficit、一次 commit |
| `policy_irq_tuner.go` | IRQ mutation 获取主锁、readiness gate、单 target commit |
| `policy_async_handler.go` | residual/system-exclusive writer 改为单 target 事务 |
| `policy_resource_package.go` | resource-package writer 改为单 target 事务 |
| `bulkhead/utils/view.go` | 增加 `BuildCPUSetPartitionViewFromTarget` |
| `bulkhead/api/types.go` | context 只增加 owned view，不增加模式开关 |
| `bulkhead/plugins/cpusettopology/plugin.go` | 返回 `DAGApplyResult`，不吞 non-converged |
| `bulkhead/manager.go` | `Apply`、dependent plugin 顺序、reclaim hint store |
| `dynamicpolicy/util/cpuset_adjustment.go` | 删除重复 handler context |
| `cpuset_adjustment_handler.go` | 删除 generic registry，直接调用 Manager |
| `policy.go` | 同步事务、`GetResourcesAllocation` 事务化、bootstrap recovery、统一 readiness gate |

## 性能预算

### 允许

- `PrepareDurableTarget`：一次完整 snapshot clone；
- dirty Base：最多一次额外 checkpoint write；
- `CommitTarget`：一次完整 defensive clone；
- 每 transaction 构建一次 target view；
- publish/get applied reclaim 各一次 CPUSet clone。

### 禁止

- 通过五个 getter 构造 Base；
- 通过五个 setter 提交 candidate；
- 同一 transaction 多次构建 target view；
- request/result 每层 clone 完整 view；
- 对完整 view 做 digest；
- 保存完整 AppliedView 只为读取 reclaim CPUSet；
- Advisor 分字段多次 checkpoint；
- 未 dirty 时重复 prepare checkpoint。

### 指标

```text
state_prepare_durable_seconds
state_commit_target_seconds
state_checkpoint_bytes
state_checkpoint_write_total{reason,result}
reclaim_target_view_build_seconds
reclaim_apply_seconds{mode,result}
reclaim_identity_added_cpu_count
reclaim_identity_removed_cpu_count
reclaim_applied_hint_age_seconds
```

首版保持 state 写锁覆盖 checkpoint I/O，以正确性优先。只有实际观测到锁等待瓶颈后，才考虑 version/CAS；不预先引入。

## 测试矩阵

### State

| 场景 | 预期 |
|---|---|
| `TargetState.Clone` | 五字段深拷贝隔离 |
| cache snapshot | 单一代际完整快照 |
| cache replace | 五字段原子替换 |
| clean Prepare | 不写 checkpoint |
| 首次启动无旧 checkpoint | 初始化成功后标记 durable，随后 Prepare 不重写 |
| dirty Prepare | 写一次，返回同一 target |
| Prepare 写失败 | 不进入 plan |
| Commit 写失败 | cache/checkpoint 保持 Base |
| Commit 成功 | checkpoint/cache 同一 target |
| caller 修改 next | 不影响 committed cache |
| 任一旧 setter 修改 | mutation 前设置 `cacheDurable=false` |
| `SetAllocationInfo/Delete` | dirty 规则生效 |
| `ClearState` | dirty 规则生效，失败不伪装 durable |
| `StoreState` 成功/失败 | 分别置 true/保持 false |
| `StoreState` 写入内容 | 精确等于调用时已发布 cache |
| `StoreState` 后单独 snapshot 并发变化 | 不得作为 durable Base 合同使用 |
| clean Prepare 后 plan | Base 与 checkpoint 精确一致 |
| `Set* → StoreState` production scan | runtime成功路径中不存在 |

### Planner

| 场景 | 预期 |
|---|---|
| Partial NUMA0 | NUMA1 保留 |
| Partial NUMA0 清空 | NUMA0 清空，NUMA1 保留 |
| Full |完整替换 |
| target 越界 |返回错误 |
| hard floor 冲突 | pool 改选或 fail closed |
| active RampUp floor | active NUMA 与 committed ReclaimRaw 相交 |
| 两个 active RampUp owner | active NUMA 取 union 后派生 floor |
| 一个 owner expiry | 只释放不再有 active owner 的 NUMA |
| RampUp topology metadata 空 | fail safe 保护 global committed ReclaimRaw |
| Hard Partition disabled + 普通 RampUp | effective floor 为空 |
| enabled → disabled | 下一事务不再派生 active floor |
| disable/reset + RampUp owner | reset 不受 active floor 阻塞 |
| admission 后 resource-package sync | active floor 仍为 FinalReclaimRaw 子集 |
| admission 后 system-exclusive sync | active floor 仍为 FinalReclaimRaw 子集 |
| admission 后 GetResourcesAllocation | active floor 仍为 FinalReclaimRaw 子集 |
| RampUp expiry + stable Advisor commit | commit 后才允许 floor 解除 |
| base mutation | planner 不修改 Base |
| candidate 完整性 | headroom/flags 保留 |

### Selector

| 场景 | 预期 |
|---|---|
| applied 足够 shrink | selected 是 applied 子集 |
| applied 不足 | applied 后 raw |
| applied + raw 不足 |只补 fresh 差额 |
| applied 不 eligible |只替换失效部分 |
|完整 core 足够 |不拆 core |
| SMT1/2/4 |按真实 sibling topology |

### Bulkhead

| 场景 | 预期 |
|---|---|
| `FullyConverged=false` | Manager 必须返回 typed error |
| topology error |不运行 dependent plugin |
| dependent plugin error |不返回 applied hint |
|全部成功 |返回 verified reclaim CPUSet |
| Manager 不读 shared state | contract test |
| view ownership transfer |无额外 full-view clone |
| union 相同但 per-rel 不同 |不得判定收敛 |
| controlled rel 缺失 |不得被空 bucket 跳过 |

### Transaction/recovery

| 场景 | 预期 |
|---|---|
| materialize 失败 |恢复 Base |
| Commit 失败 |恢复 Base |
| `false,nil` convergence |返回 typed error，绝不成功 |
|恢复 `false,nil` |返回 typed error并阻止整个 CPU policy |
|恢复失败 |阻止整个 CPU policy |
| materialize 后崩溃 |restart 恢复 checkpoint Base |
| commit 后崩溃 |restart 收敛 committed next |
| Advisor通知失败 | admission仍成功，异步重试 |
| reset失败 |不提交，不发布 |
| allocation前先写live state | contract test失败 |
| recovery未完成 |所有CPU mutation API fail closed |
| recovery成功、worker未全启动 | 保持 `recovering` |
| recovery和全部worker成功 | `recovering → ready` |
| startup recovery失败 | `recovering → blocked` |
| runtime Base恢复失败 | `ready → blocked` |
| blocked periodical | 跳过且不修改state |
| blocked Advisor callback | typed unavailable |
| blocked IRQ update | typed unavailable，不修改state |
| blocked GetResourcesAllocation | typed unavailable，不修改state |
| blocked GetTopologyHints | typed unavailable，不返回可用hint |
| IRQ 与 Allocate 并发 | 主锁串行，无 lost update |
| GetResourcesAllocation checkpoint失败 | 不返回伪成功 |
| residual/system-exclusive sync | 不调用旧 setter/StoreState |
| resource-package sync | 不调用旧 setter/StoreState |
| Start 顺序 | recovery成功前不启动 writer |
| Start 重试 | bootstrap/recovery 幂等，不重复启动worker |
| worker N 启动失败 | 逆序停止 1..N-1，保持 blocked |
| worker N 启动失败窗口 | 任何时刻均不可观察到 ready |
| Stop → Start | 从durable target恢复后再ready |
| bootstrap migration | 一个 TargetState、至多一次 CommitTarget |

### Writer inventory

增加静态 contract test 或 lint：

```text
production DynamicPolicy code
    禁止直接调用:
        State.SetPodEntries
        State.SetMachineState
        State.SetAllocationInfo
        State.Delete
        State.ClearState
        State.StoreState

允许位置:
    state package内部
    tests
    明确标注的bootstrap compatibility shim
```

每次新增 state writer 必须通过统一的 `TargetState` transaction review。

### E2E

```text
old applied NUMA0: 17-26,113-122
new target size:   18
new selected:      17-25,113-121
addedFresh:        empty
removedApplied:    26,122
```

同时满足：

- NUMA1 old reclaim 保留；
- SNB 不消费 hard floor；
- Allocate 成功前 topology fully converged；
- failure 不提交 target、不发布 hint；
- 50ms 采样窗口内无 overlap、empty cpuset 或 containment breach。

## 兼容与回滚

- 外部 QRM API 不变；
- SysAdvisor proto 不新增 generation；
- checkpoint schema 不变；
- 已有 checkpoint 可直接读取，无迁移步骤；
- ratio、reserve、duration 和 overlap 配置不变。

短期 feature gate：

```text
EnableReclaimIdentityStability
```

关闭后只允许退化 identity preference，不得关闭：

- Partial merge；
- hard floor subset 校验；
- topology containment；
- full convergence。

这些属于安全修复，不保留长期双路径。

## 方案评审

### Verdict

**PASS，可进入实现计划。**

通过条件：

1. `state.TargetState` 是唯一完整 runtime target。
2. 不新增 `CommitSnapshot`、generation 或 transaction DTO。
3. planner 直接输入输出 `TargetState`，不扩展现有通用 candidate。
4. state commit 只做一次 defensive clone 和一次 cache ownership transfer。
5. clean Base 不重复 checkpoint。
6. target view 每 transaction 只构建一次。
7.同步首版无 revision/digest。
8.直接复用 `DAGApplyResult`。
9. Manager 不提供 best-effort 模式，成功必然 fully converged。
10. active floor 只从现有 RampUp/NUMA/raw 派生，不增加 checkpoint 字段。
11. Manager 只保存 reclaim CPUSet hint。
12. pre-commit failure 恢复 durable Base。
13. Advisor 通知失败不回滚 committed allocation。
14. future queue 只复用 owner 边界，不提前增加字段。

## 最终不变量

```text
Allocate成功
⇒ FullyConverged
且candidate已durable commit
```

```text
FinalReclaimRaw ⊇ EffectiveHardFloor
```

```text
Partial update不改变未声明NUMA
```

```text
Published reclaim hint
⇒ 本轮所有plugin成功
且对应target已durable commit
```

```text
last-applied只影响identity preference
```

```text
checkpoint是restart恢复的唯一策略目标
```

```text
同步首版不持久化执行过程
```
