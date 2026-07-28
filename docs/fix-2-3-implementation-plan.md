# Fix-2 + Fix-3 完整实施方案（Fix-1 暂缓）

> 关联 RCA：`docs/rca-reclaim-cgroup-ebusy.md`
> 目标复现单：`rerun0728190702`，失败 Pod `katalyst-e2e-fdbdstdrerun0728190702r2-ded-stable-1`，现象 `apply cpuset.cpus=11-16,33-39,59-64,81-87 @ kubesandbox: device or resource busy`
> 代码基线：`worktree pr-1202-on-core-bulkhead-handlers`，包路径 `pkg/agent/qrm-plugins/cpu/dynamicpolicy`

## 0. 前提：为什么 Fix-1 暂缓下 Fix-2+Fix-3 仍能兜底

RCA 三处根因的分工：

| 根因 | 角色 | 本方案是否处理 |
| --- | --- | --- |
| RC1 advisor 落地失败不回滚 state | 断层**制造者**（state 领先 cgroup 一整代） | ❌ 暂缓（事务化改造成本高） |
| RC2 `generateReclaimBlockCPUSet` 从零重选 | 断层**放大器**（跳变幅度 14↔28 核几乎零交集） | ✅ Fix-2 |
| RC3 单趟 apply 父收缩撞未换代子桶 | 断层**触发者**（有界重试内不收敛→EBUSY→fail Pod） | ✅ Fix-3 |

不修 RC1，「state 领先 cgroup 一代」的窗口仍会出现。但 EBUSY 是否发生、是否 fail Pod，取决于**领先的那一代 state 与 cgroup 现场的重叠度**（RC2 决定）以及**单趟 apply 撞到未换代子桶时的降级行为**（RC3 决定）：

- Fix-2 把 advisor 侧 reclaim 从「从零重选」改成「优先复用上一把」，使即便 state 领先一代，新旧 reclaim 在 NUMA 内也**最大化交集**（从「几乎零交集」降到「仅让出被抢占核」）。父收缩目标与滞留子桶大概率仍是子集关系或仅差少量核，`shrinkParentWithLiveChildUnion` 的 bridge/drain 在有界重试内即可收敛，不再撞确定性 EBUSY。
- Fix-3 是最后一道兜底：即使仍出现零交集换代，父收缩在本趟无法收敛时**不再裸写硬撞 EBUSY 并 fail Pod**，而是保留父为合法超集、把收敛让给下一轮 reconcile（RCA T8 已证明该路径可达），并让准入路径对这类瞬时拓扑落后**降级不拒绝合法 Pod**。

结论：Fix-2 大幅降低触发概率，Fix-3 把「触发后 fail Pod」降级为「触发后延迟收敛」。二者叠加，即使 RC1 未修，也能消除 Pod `UnexpectedAdmissionError`。**残留风险**：state 与 cgroup 仍可能短暂不一致（由后台 reconcile 收敛），需 §5 观测项确认收敛窗口有界。

---

## 1. Fix-2 — `generateReclaimBlockCPUSet` 引入 preferred 复用

### 1.1 现状（问题定位，带行号）

`generateReclaimBlockCPUSet`（`policy_advisor_handler.go:1076`）:

- NUMA-aware 分支：`:1094` `currentAvailableCPUs := numaAvailableCPUs.Difference(globalNonReclaimableCPUSet)` → `:1118` `calculator.TakeByTopology(machineInfo, currentAvailableCPUs, blockResult, false)`。
- non-NUMA 分支：`:1140` 同样扣除后 → `:1163` `calculator.TakeByNUMABalance(machineInfo, currentAvailableCPUs, blockResult)`。

两者都在「剩余核」上**从头选核**，无「上一把 reclaim cpuset」入参，是跳变放大器。

已有可复用积木 `takeByTieredPreferredCPUs`（`policy_allocation_source_pool.go:73`）：签名 `(availableCPUs, preferredTiers []machine.CPUSet, cpuRequirement int) (taken, remaining, err)`，语义为「先按序从 preferredTiers ∩ available 取，不足再 `TakeByNUMABalance` spill」。Fix-2 直接复用此函数。

### 1.2 目标

advisor 每拍重算 reclaim block 时，**优先落在上一把 reclaim 占据的核位**，只有被 dedicated/share 抢走或核数增长才动用新核，使 advisor 侧期望与 cgroup 现场（及 admission 侧 preserve 减法语义）收敛一致。

### 1.3 改动点

#### (a) 取上一把 reclaim cpuset，按 NUMA 拆 tier

在 `generateReclaimBlockCPUSet` 入口处（`:1082` 函数体开头）新增：从当前 state 读上一把 reclaim 池 cpuset，按 NUMA 预切分。

```go
// 上一把 reclaim 池 cpuset（可能为空：首次分配 / 池刚建）
prevReclaim, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameReclaim)
if err != nil {
    // 池不存在时视为空，走原 spill 路径，不阻断
    prevReclaim = machine.NewCPUSet()
}
```

> `GetCPUSetForPool`（`state/state.go:304`）在池不存在时返回 error，需吞掉降级为空集，保持首次分配/冷启动行为不变。

#### (b) NUMA-aware 分支替换取核（`:1118`）

将：

```go
cpuset, err := calculator.TakeByTopology(machineInfo, currentAvailableCPUs, blockResult, false)
```

替换为「优先复用本 NUMA 上一把 reclaim ∩ 可用」：

```go
// 本 NUMA 的上一把 reclaim 作为首选 tier
prevOnNUMA := prevReclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
preferredTiers := []machine.CPUSet{prevOnNUMA}
cpuset, _, err := p.takeByTieredPreferredCPUs(currentAvailableCPUs, preferredTiers, blockResult)
```

#### (c) non-NUMA 分支替换取核（`:1163`）

将：

```go
cpuset, _, err := calculator.TakeByNUMABalance(machineInfo, currentAvailableCPUs, blockResult)
```

替换为：

```go
// non-NUMA reclaim：整把上一把 reclaim 作为首选 tier
preferredTiers := []machine.CPUSet{prevReclaim}
cpuset, _, err := p.takeByTieredPreferredCPUs(currentAvailableCPUs, preferredTiers, blockResult)
```

### 1.4 语义与不变量校验

- **不越界**：`takeByTieredPreferredCPUs` 的每个 tier 都会 `Intersection(remaining)`，`remaining` 初始为 `currentAvailableCPUs`（已扣除 static/dedicated/share/globalNonReclaimable）。因此复用绝不会选到已被非 reclaim 占用或 pinned 的核。
- **多 block 同 NUMA 去重**：原代码在每个 block 取核后 `currentAvailableCPUs = currentAvailableCPUs.Difference(cpuset)`（`:1124`）。保留该行即可让下一个 block 的 preferred tier 自动排除已被本 NUMA 前一个 block 取走的核，无重复分配。
- **数量守恒**：`takeByTieredPreferredCPUs` 返回 `taken.Size() == cpuRequirement`（tier 不足时 spill 补齐），与原 `TakeByTopology`/`TakeByNUMABalance` 的 requirement 语义一致。
- **NUMA 亲和**：NUMA-aware 分支的 `prevOnNUMA` 已 `∩ CPUsInNUMANodes(numaID)`，spill 也在 `currentAvailableCPUs`（本 NUMA available）内，不会跨 NUMA 注入。
- **对齐差异（需评审确认）**：原 `TakeByTopology(..., alignByL3Caches=false)` 带拓扑感知打包；`takeByTieredPreferredCPUs` 的 spill 走 `TakeByNUMABalance`。当上一把为空（首次）时，取核策略从 `TakeByTopology` 退化为 `TakeByNUMABalance`，**可能改变首次 reclaim 的核形状**。若要求首次行为完全不变，可在 `prevOnNUMA.IsEmpty()` 时回退原 `TakeByTopology`（见 §1.5 兜底分支）。

### 1.5 首次分配保形兜底（可选，推荐）

为避免改变冷启动/首次 reclaim 形状，NUMA-aware 分支保留原路径作为空 tier 回退：

```go
prevOnNUMA := prevReclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
var cpuset machine.CPUSet
if prevOnNUMA.IsEmpty() {
    cpuset, err = calculator.TakeByTopology(machineInfo, currentAvailableCPUs, blockResult, false)
} else {
    cpuset, _, err = p.takeByTieredPreferredCPUs(currentAvailableCPUs, []machine.CPUSet{prevOnNUMA}, blockResult)
}
```

### 1.6 Fix-2 单测（新增 `policy_advisor_handler_test.go` 用例）

| 用例 | 构造 | 断言 |
| --- | --- | --- |
| reclaim 就地复用 | state 存在上一把 reclaim `6-7,29-31,54-55,73-79`，dedicated 抢走其中 `6`（NUMA0） | 新 reclaim ⊇ `7,29-31,55,73-79`（未被抢核全保留），仅让出 `6`，交集最大化 |
| dedicated 释放后回填 | 上一把 reclaim 缺一段，本拍 available 变大 | 优先补回原相邻核（tier 命中），再 spill |
| 首次分配 | state 无 reclaim 池 | 不 panic，走 spill/`TakeByTopology` 兜底，requirement 满足 |
| 跨 NUMA 隔离 | 上一把含 NUMA0+NUMA1 | NUMA-aware 分支各 NUMA 只复用本 NUMA 段，无跨 NUMA 注入 |

---

## 2. Fix-3 — 父收缩换代鲁棒性 + 准入 EBUSY 降级

Fix-3 分两层：**(A) 写入层** 让换代式父收缩在本趟无法收敛时不硬撞 EBUSY 而是延迟；**(B) 准入层** 让 admission 遇到这类瞬时拓扑落后时不 fail Pod。

### 2.1 现状（问题定位，带行号）

- 父收缩入口 `shrinkParentWithLiveChildUnion`（`safe_writer.go:150`）：先 bridge（`:166` `bridgeTarget := target.Union(liveChildUnion)`，`:172` `writeBridgeNode`），再 `shrinkLiveChildrenToParent`（`:176`），最后 `writeNode(node, effectiveTarget)`（`:199`）。
- `writeNode`（`:609`）对 EBUSY 做 `maxSafeCPUSetWriteAttempts=3`（`:36`）次紧邻重试，每次间 `reconcileLiveChildrenBeforeRetry`（`:786`，best-effort）。
- **失败点**：当 `reclaimed-0/1` 子桶滞留旧代（`6-7,54-55`/`29-31,73-79`），父目标 `11-16,59-64` 与之零交集，子桶换到新 NUMA 段需要跨窗口的 advisor 受控过渡，本趟 3 次重试子快照不变 → 第 3 次 `attempt == maxSafeCPUSetWriteAttempts-1` 直接 `return err`（`:634`）→ EBUSY 上抛。
- 该 err 沿 `ApplyDAGDiff` → `runCPUSetAdjustmentHandlers` → `adjustPoolsAndIsolatedEntries`（`policy_allocation_handlers.go:1237`）上抛，最终 Pod 判 `UnexpectedAdmissionError`。

### 2.2 (A) 写入层：换代不收敛时返回「未收敛」而非硬撞 EBUSY

#### 设计原则

`shrinkParentWithLiveChildUnion` 在最终 `writeNode` 前已经计算了 `effectiveTarget := target.Union(parked)`（`:183`）并校验 `refreshedChildUnion.IsSubsetOf(effectiveTarget)`（`:188`）。问题在于：当存在**滞留旧代但不属于「已 park 的跨 NUMA 物理桶」**的受控子桶（reclaimed-N 是 DAG 内受控节点，不走 park 分支），`shrinkLiveChildrenToParent` 会尝试把它 clamp 到父 target，但子桶自身是零交集换代，clamp 结果为空→回退父 target（`:494-496`）注入外 NUMA 核，或子桶写入本趟不生效，导致最终裸 `writeNode` 撞 EBUSY。

#### 改动

1. **识别「本趟不可收敛的换代收缩」**：在 `shrinkParentWithLiveChildUnion` 最终 `writeNode`（`:199`）前，增加一次显式判定——若受控 reclaim 子桶的当前 cgroup 值与其目标零交集（换代），且经 `shrinkControlledChildrenToTargets`（`:157`）后子桶实际值仍未进入父 target，则**不进入裸 `writeNode`**，而是：
   - 保持父为合法超集：写 `bridgeTarget = target ∪ liveChildUnion`（当前子并集），确保父仍覆盖所有存活子（cgroup v1 合法）。
   - 返回一个可识别的**非致命 sentinel error**（如 `errDeferConvergence`），标记「本趟未收敛，交下一轮 reconcile」。

2. **新增 sentinel error 与判定** （`safe_writer.go` 顶部，`:38` 附近）：

```go
// errDeferConvergence: 一个换代式收缩在本趟有界重试内无法让子桶换段，
// 但父已保持为合法超集，收敛留给下一轮周期 reconcile。非致命。
var errDeferConvergence = errors.New("bulkhead cpuset convergence deferred to next reconcile")

func isDeferConvergenceError(err error) bool {
    return errors.Is(err, errDeferConvergence)
}
```

3. **`writeNode` EBUSY 兜底转译**（`:634`）：当 `attempt == maxSafeCPUSetWriteAttempts-1` 且仍是 EBUSY 时，若该节点是父收缩换代场景（子桶零交集且已写过 bridge 保住超集），返回 `errDeferConvergence` 而非原始 EBUSY：

```go
if !isCgroupBusyError(err) || attempt == maxSafeCPUSetWriteAttempts-1 {
    if isCgroupBusyError(err) && w.parentSupersetHeld(node, target) {
        // 父已是所有存活子的合法超集，仅最终收窄未成 → 延迟收敛，不 fail 上层
        general.Warningf("topo_dag_writer: defer_convergence rel=%q target=%s err=%v", node.Rel, target.String(), err)
        return errDeferConvergence
    }
    return err
}
```

其中 `parentSupersetHeld(node, target)` 校验 `liveChildUnion(node.Rel).IsSubsetOf(currentParentCPUSet)`——即父当前值确实覆盖所有存活子（bridge 已生效），只是没收窄到最终 target。**只有满足该不变量才允许降级**，否则仍返回硬 EBUSY（避免掩盖真实的父<子非法态）。

4. **`ApplyDAGDiff` 结果传播**：`convergeControlledNodes`（`writer.go:209`）的 `firstErr` 聚合需区分 `errDeferConvergence`：
   - `errDeferConvergence` **不计入** `firstErr`（不让 `ApplyDAGDiff` 整体失败），而是累加到 `res` 的一个新计数字段 `res.Deferred++` 并置 `res.FullyConverged=false`。
   - 其余 error 仍照原逻辑上抛。

```go
if err := writer.shrinkParentWithLiveChildUnion(n, target); err != nil {
    if isDeferConvergenceError(err) {
        res.Deferred++
    } else if firstErr == nil {
        firstErr = err
    }
}
```

`DAGApplyResult`（`writer.go:47`）增字段：

```go
type DAGApplyResult struct {
    Attempted      int
    Applied        int
    Skipped        int
    Failed         int
    Deferred       int   // 新增：换代收缩延迟到下一轮 reconcile 的节点数
    FullyConverged bool
    ConvergenceReport ConvergenceReport
}
```

#### 不变量（写入层安全底线）

- 降级路径必须先保证父是所有存活子的超集（`parentSupersetHeld`），绝不允许出现「父 < 某存活子」的非法 cgroup 态。
- 降级仅适用于「最终收窄」这一步；bridge 扩容、ensureParentContains 等仍必须成功，失败照旧上抛。
- 下一轮周期 reconcile（bulkhead periodical）复用同一 `shrinkParentWithLiveChildUnion` 路径；届时若 advisor 已把子桶换段（Fix-2 让这更快发生），`effectiveTarget` 收窄成功，`Deferred` 归零。RCA T8 即此路径的实证。

### 2.3 (B) 准入层：`runCPUSetAdjustmentHandlers` 失败降级不拒绝 Pod

#### 现状

`adjustPoolsAndIsolatedEntries`（`policy_allocation_handlers.go:1187`）在 `:1237-1238` 调 `runCPUSetAdjustmentHandlers`，其返回 error 会向上冒泡到 `Allocate`，最终使 Pod `UnexpectedAdmissionError`。

#### 改动

区分「拓扑落地瞬时不收敛（可延迟）」与「真实分配错误」：

1. 让 bulkhead handler 把 `res.Deferred>0 && firstErr==nil` 的情况表达为一个**可识别的非致命错误**（沿用 `errDeferConvergence`，或在 handler 边界包一层 `TopologyDeferredError`），一路透传到 `adjustPoolsAndIsolatedEntries`。

2. 在 `:1237` 调用点区分处理：

```go
if err := p.runCPUSetAdjustmentHandlers(...); err != nil {
    if isTopologyDeferredError(err) {
        // 拓扑落地本轮未完全收敛，但 state 分配有效、父 cgroup 已是合法超集。
        // 接受本次准入，cgroup 交后台 bulkhead 周期 reconcile 收敛，不 fail Pod。
        general.Warningf("cpuset adjustment deferred, accept admission and rely on periodical reconcile: %v", err)
        // 不 return err；继续正常返回分配结果
    } else {
        return err
    }
}
```

#### 安全边界（准入层降级的前置条件）

- 降级**仅**接受 `errDeferConvergence` 这一 sentinel；任何其他 error（分配算法失败、真实 cgroup 权限错误、父<子非法）必须照旧 fail，绝不放宽。
- 因为写入层已保证「父是所有存活子的合法超集」，被降级接受的 Pod 其容器 cgroup（叶子）仍在合法父范围内，不会出现容器跑在非法 cpuset 上。
- 降级后必须有后台 reconcile 兜底收敛（bulkhead periodical 已存在），并由 §5 观测项确认收敛窗口有界（否则退化为静默不一致）。

### 2.4 Fix-3 单测（新增 `safe_writer_test.go` / `writer_test.go` 用例）

| 用例 | 构造（fake cgroup client） | 断言 |
| --- | --- | --- |
| 换代收缩延迟而非 EBUSY | 父 `kubesandbox` 目标 `11-16,59-64`，子 `reclaimed-0/1` 滞留 `6-7,54-55`/`29-31,73-79`（零交集），fake client 对最终父收窄持续返 EBUSY | `shrinkParentWithLiveChildUnion` 返回 `errDeferConvergence`；父当前值为 `target ∪ liveChildUnion` 合法超集；`res.Deferred==1`，`FullyConverged==false` |
| 父<子非法态不降级 | 构造 bridge 未生效、父不覆盖子 | 返回原始 EBUSY（非 defer），`parentSupersetHeld` 为 false |
| 下一轮 reconcile 收敛 | 上一用例后，把子桶改为已换段 `11-16`/`59-64`，再跑一次 | `writeNode` 收窄成功，返回 nil，`Deferred==0` |
| 准入降级仅认 sentinel | `runCPUSetAdjustmentHandlers` 返回 `errDeferConvergence` vs 普通 error | 前者不 fail Pod、后者 fail |
| 非换代普通 EBUSY 仍上抛 | 子快照可收敛但 client 返一次瞬时 EBUSY 后成功 | 重试后成功，不误降级 |

---

## 3. 改动文件清单

| 文件 | 改动 |
| --- | --- |
| `policy_advisor_handler.go` | `generateReclaimBlockCPUSet`（1076-1181）：入口读 prevReclaim；`:1118`/`:1163` 换 `takeByTieredPreferredCPUs`（含空 tier 兜底） |
| `bulkhead/utils/topology/safe_writer.go` | 新增 `errDeferConvergence`/`isDeferConvergenceError`/`parentSupersetHeld`；`shrinkParentWithLiveChildUnion`(150) 换代识别；`writeNode`(609) EBUSY 兜底转译 |
| `bulkhead/utils/topology/writer.go` | `DAGApplyResult`(47) 加 `Deferred`；`convergeControlledNodes`(209) 区分 defer 不计 firstErr |
| `policy_allocation_handlers.go` | `adjustPoolsAndIsolatedEntries`(1187) `:1237` 调用点：`isTopologyDeferredError` 降级不 fail |
| `*_test.go` | Fix-2 / Fix-3 单测（§1.6、§2.4） |
| 可观测性（配套，RCA §4） | `logCPUSetSubtreeOnWriteFailure`(707) marker 增补触发 Pod uid/name |

---

## 4. 落地顺序（建议）

1. **Fix-2 先行**（低风险、独立）：只改 advisor 取核，不改写入/准入语义。单独部署即可显著降低跳变幅度与 EBUSY 触发率，可先做一轮 E2E 观察触发率下降。
2. **Fix-3(A) 写入层**：引入 `errDeferConvergence` + `parentSupersetHeld` + `Deferred` 计数，先只在 handler 内部消化（暂不改准入），观察 defer 事件是否如预期在下一轮收敛。
3. **Fix-3(B) 准入层**：确认写入层 defer 语义稳定后，再开准入降级，彻底消除 fail Pod。
4. 每步之间跑 §5 验证，避免一次性放开准入降级掩盖潜在真实错误。

---

## 5. 验证计划

### 5.1 单测

- Fix-2：`generateReclaimBlockCPUSet` 复用/回填/首次/跨 NUMA 四类（§1.6）。
- Fix-3：换代延迟、父<子不降级、下一轮收敛、准入仅认 sentinel、普通 EBUSY 不误降级（§2.4）。
- 全量 `go test ./pkg/agent/qrm-plugins/cpu/dynamicpolicy/...` 通过。

### 5.2 节点 E2E（`qrm-bulkhead-e2e` skill）

复跑标准 3 轮 + high-churn 5 轮，验收：

- `FULL_E2E_DONE rc=0 final_reset_rc=0`，每个 `PHASE_DONE ... rc=0`。
- stable/recreate 阶段无业务 Pod `Failed`（Fix-3(B) 生效）。
- `agent.WARNING.log` 中 `cpuset_write_failed stage=write_node rel="kubesandbox"` 的**确定性 EBUSY 消失**；若仍偶发，应转为 `defer_convergence` 且在下一周期内 `Deferred` 归零（收敛窗口有界）。
- `preserve current reclaim pool ...` 与 advisor `applyBlocks transform` 的 reclaim 前后值交集显著变大（Fix-2 生效：从「几乎零交集」到「仅让出被抢占核」）。

### 5.3 观测指标（灰度期重点）

- `res.Deferred` 事件频次与每次的收敛延迟（下一轮 reconcile 是否清零）。
- 准入降级次数：`cpuset adjustment deferred, accept admission ...` 日志计数；应随 Fix-2 部署时间衰减。
- 无 `STATE=OVERLAP`、schedstat `OVERLAP`、`NODE_CHECK_FAIL` 终态。

---

## 6. 残留风险与后续（依赖 Fix-1）

- **不修 RC1 的根本残留**：state 仍可能领先 cgroup 一代，Fix-3 把它从「fail Pod」降级为「延迟收敛」，但**依赖后台 reconcile 一定能收敛**。若 advisor 持续每拍都推进新一代 state（极端高频重算），理论上可能出现 defer 长期不清零。Fix-2 通过稳定 reclaim 位置大幅降低此概率，但不能从原理上根除。
- 因此本方案定位为**缓解 + 兜底**，最终仍建议补 Fix-1（advisor 写 state / 写 cgroup 事务化，落地失败回滚 state），从源头消除断层。届时 Fix-3 的 `Deferred` 应恒为 0，成为纯粹的防御性代码。
