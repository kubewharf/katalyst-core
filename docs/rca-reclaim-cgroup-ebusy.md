# RCA + Fix: reclaim 池 state 领先 cgroup 一整代导致父 cgroup 收缩确定性 EBUSY

> 复现单：`rerun0728190702`，标准 E2E round2 stable 阶段
> 失败 Pod：`katalyst-e2e-fdbdstdrerun0728190702r2-ded-stable-1`（uid `a3a9cb0f...`）
> 失败现象：`apply cpuset.cpus=11-16,33-39,59-64,81-87 @ kubesandbox: device or resource busy`
> 节点：`fdbd:dc02:27:49::14`（96 核；node0=`0-23,48-71`，node1=`24-47,72-95`）
> 运行时配置：`enableReclaim=true`（动态配置 `dynamicConfig.GetDynamicConfiguration().EnableReclaim`，覆盖 cmdline `--enable-reclaim=false`）；`allowSharedCoresOverlapReclaimedCores=false`

代码基线：`worktree pr-1202-on-core-bulkhead-handlers`，包路径 `pkg/agent/qrm-plugins/cpu/dynamicpolicy`。

---

## 1. 现象与结论（TL;DR）

一个 dedicated numa_binding Pod 准入时，QRM CPU DynamicPolicy 把 reclaim 池的**目标 cpuset**（来自 state）写入父 cgroup `kubesandbox`，而此刻 cgroup 上 `kubesandbox/reclaimed-0/1` 子节点仍停留在**更早一代**的 cpuset，两者在 NUMA0 段**零交集**，违反 cgroup v1「父必须是每个存活子的超集」约束，触发 **EBUSY**；`maxSafeCPUSetWriteAttempts=3` 次重试的子树快照完全相同（1.7ms 内），故必然失败，Pod 被 `UnexpectedAdmissionError` 拒绝。

根因不是单点，而是三处叠加，本质是 **advisor 侧把 reclaim 池的 state 推进了一整代（14 核 → 28 核，且几乎不重叠），但对应的 cgroup 落地并未跟上**，随后进入的 admission 忠实地拿领先的 state 去写落后的 cgroup：

| # | 层 | 缺陷 | 角色 |
| --- | --- | --- | --- |
| RC1 | advisor 一致性 | `applyBlocks` 先改内存 state、再落 cgroup；落地在顶层父 `kubepods` 因 `children_not_ready` 失败后 **state 不回滚** | 断层的**制造者** |
| RC2 | advisor 切核策略 | `generateReclaimBlockCPUSet` 从「剩余核」**从零重选**，不复用上一把 reclaim cpuset | 断层的**放大器**（跳变幅度） |
| RC3 | 拓扑落地鲁棒性 | 单趟 `ApplyDAGDiff` 内，父 `kubesandbox` 收缩前未能把 reclaim 子桶 drain/bridge 到位（因 RC1 使子节点滞留旧值），3 次重试子快照不变 | 断层的**触发者** |

---

## 2. 时序坐实（同一 pid `2124948`，19:09:14 窗口，串行）

日志源：节点 `/data00/tiger/tce/containers/bytedance.katalyst.qrm_resource_plugins/katalyst-qrm-plugins-tjdhn-42xln/toutiao/log/app/agent.INFO.log`（及同目录 `agent.WARNING.log`）。

| # | 时间 | 事件 | reclaim state | cgroup `kubesandbox` 现场 |
| --- | --- | --- | --- | --- |
| T1 | 14.304 | admission 重算 | previous `5-7,29-31,53-55,73-79` → current **`6-7,29-31,54-55,73-79`** | 14.325–14.340 成功写 reclaimed-0/1 + 父，落 `6-7,29-31,54-55,73-79`（子 `6-7,54-55`/`29-31,73-79`） |
| T2 | 14.484–14.486 | **advisor `applyBlocks`** transform | reclaim `6-7,29-31,54-55,73-79`(14核) → **`10-16,33-39,58-64,81-87`(28核)** 写入内存 state | 未写 |
| T3 | 14.554–14.636 | advisor loop 落地：逐个收窄 `kubepods` 下 Pod 子 cgroup | — | 各 Pod 子节点写成功 |
| T4 | **14.637** | advisor loop 走到顶层父 `kubepods` → bulkhead dag **失败** `children_not_ready` | **未回滚**（仍 28 核） | `kubesandbox`/`reclaimed-*` **没轮到写** |
| T5 | 14.641–14.644 | 新 admission(`ded-stable-1`) 进入，dedicated 分到 `result="2,50"` | 读到 T2 的 28 核 state | — |
| T6 | 14.647 | `GetCPUSetForPool(reclaim)` = **`10-16,33-39,58-64,81-87`** → 减法 current `11-16,33-39,59-64,81-87` | — | — |
| T7 | 14.750–14.752 | bulkhead 写父 `kubesandbox`=`11-16,...` | — | 子仍 `6-7,54-55`/`29-31,73-79` → 零交集 → **EBUSY ×3** |
| T8 | 14.850–14.922 | reconcile 后 bridge 超集过渡最终写成功 | — | `kubesandbox` 经 `6-7,11-16,29-31,54-55,59-64,73-79` → … → `11-16,33-39,59-64,81-87` 收敛 |

关键交叉印证：

- T4 失败原文：`E policy_advisor_handler.go:429 ... run cpuset adjustment handler "bulkhead": ... apply bulkhead topology dag: children_not_ready: parent=kubepods target=1-5,8-9,17-23,25-28,32,40-53,56-57,65-72,80,88-95 parked= liveChildUnion=1-5,8-23,25-28,32-53,56-72,80-95`。
- T6 减法原文：`generatePoolsAndIsolation ... preserve current reclaim pool after deducting non-reclaim allocations, previous: 10-16,33-39,58-64,81-87, deducted: 0,3-10,24-32,51-58,73-80, current: 11-16,33-39,59-64,81-87` → 「旧值」`10-16,33-39,58-64,81-87` 与 T2 advisor 写入 state 的值**逐位匹配**。
- T7 marker（`agent.WARNING.log`，125 条 `cpuset_write_failed` 中的一组）：`stage=write_node rel="kubesandbox" role=reclaim attempt=0/1/2 target=11-16,33-39,59-64,81-87 subtree=[parent_now=6-7,29-31,54-55,73-79 r0=6-7,54-55 r1=29-31,73-79]`，3 次快照完全相同。
- T8 中 `33-39`/`81-87` 首次真正落到 `reclaimed-1` cgroup 在 14.895（`reclaimed-1: 29-31,33-39,73-79,81-87`），比 state 侧（T2 14.486）晚 **409ms**，中间恰好夹着 T7 那次必然 EBUSY 的 admission。

> 结论：EBUSY 的直接触发是 admission 拿领先一代的 state 写落后一代的 cgroup 父；「旧值匹配」成立，但匹配对象是一次**已落地失败且 state 未回滚**的 advisor 扩容，而非上一次 admission 的输出。

---

## 3. 根因逐项（带行号）

### RC1 — advisor `applyBlocks`：state 先行、cgroup 后落、落地失败不回滚

- 入口链：`getAdviceFromAdvisorLoop`(`policy_advisor_handler.go:407`) → `allocateByCPUAdvisor`(`:487`) → `applyBlocks`(`:1331`)。
- `applyBlocks` 在 `:1394` 打出 `... cpuset allocation result transform from %s(size:%d) to %s(size:%d)` —— 此处**只更新内存 `newEntries` / state**（T2 的 14→28 核就在这条）。
- 同一 advisor loop 随后进入 cgroup 落地（`runCPUSetAdjustmentHandlers` → bulkhead `cpuset_topology` → `ApplyDAGDiff`）。落地从顶层父 `kubepods` 起按 DAG 序 apply，在 `kubepods` 处判定 `children_not_ready` 直接 `return`（`safe_writer.go:189` / `writer.go:227` converge_shrink 分支），**后续 `kubesandbox`/`reclaimed-0/1` 子树整段未执行**。
- 落地失败沿 `allocateByCPUAdvisor` 上抛（`policy_advisor_handler.go:524-526` `applyBlocks failed ...`；`:429` loop 记 error），但**先前写入的 28 核 state 没有随失败回滚**。因此下一拍 admission 的 `GetCPUSetForPool(reclaim)` 读到的就是这份「已落地失败」的领先 state。

> 缺陷定性：advisor 的「计算→写 state→写 cgroup」不是一个 all-or-nothing 事务；cgroup 写在顶层父就可能整体失败，而 state 已经 commit，形成 **state 领先 cgroup 一整代** 的持久窗口。

### RC2 — `generateReclaimBlockCPUSet`：reclaim 从零重选，不复用旧位置

- `generateReclaimBlockCPUSet`(`policy_advisor_handler.go:1076`) 函数签名 `(reclaimBlocksMap, nodeRemainingCPUs, availableCPUs, globalNonReclaimableCPUSet, blockCPUSet)` —— **没有「上一把 reclaim cpuset」入参**。
- NUMA-aware reclaim block：`:1094` `currentAvailableCPUs := numaAvailableCPUs.Difference(globalNonReclaimableCPUSet)` → `:1118` `calculator.TakeByTopology(machineInfo, currentAvailableCPUs, blockResult, false)`。
- non-NUMA reclaim block：`:1140` 同样从 available 扣除后，`:1163` `calculator.TakeByNUMABalance(machineInfo, currentAvailableCPUs, blockResult)`。
- 二者都是在「扣掉 static/dedicated/share 后的**剩余核**」上从头选核。只要 dedicated/share 占核位置一变，剩余核集合就变，reclaim 位置随之整体跳变（T2 从 `6-7,29-31,54-55,73-79` 跳到 `10-16,33-39,58-64,81-87`，NUMA0 段几乎不重叠）。

**与 admission 侧的策略不对称**（这是问题核心）：

| 路径 | reclaim 复用机制 | 行为 |
| --- | --- | --- |
| admission `generatePoolsAndIsolation` | `:1914-1930` `enableReclaim && !allowOverlap` 分支：`poolsCPUSet[reclaim] = GetCPUSetForPool(reclaim).Difference(allocatedNonReclaimCPUs)`（preserve 减法） | 以旧 reclaim 为基准只减被抢走的核 → **平滑**（14→仅差 2 核） |
| advisor `generateReclaimBlockCPUSet` | `:1118` `TakeByTopology` / `:1163` `TakeByNUMABalance`（从剩余核重选） | 不看旧位置 → **大跳变** |

> admission 侧本已稳（复用旧位置），但 advisor 每一拍都可能用「从零重选」的结果覆写 state，把 reclaim 推到与 cgroup 现场无重叠的新位置。RC2 决定了跳变**幅度**，把 RC1/RC3 的危害从「偶发」放大到「几乎必然零交集」。

### RC3 — 拓扑落地：单趟 `ApplyDAGDiff` 内父收缩前子桶未 drain/bridge

- bridge 机制**已存在**：`shrinkParentWithLiveChildUnion`(`safe_writer.go:150`) 先算 `bridgeTarget := target.Union(liveChildUnion)`(`:166`)、必要时 `writeBridgeNode`(`:172`)，再 `shrinkLiveChildrenToParent`(`:176`)，最后 `writeNode(node, effectiveTarget)`(`:199`)。`ApplyDAGDiff` 的 converge 阶段按 shrink/expand 分派到该函数（`writer.go:227`/`:251`）。
- 但 T7 失败 marker 是 `stage=write_node rel="kubesandbox"`，即最终裸写父那一步 EBUSY，且 3 次重试子快照不变。原因是：此趟 admission-triggered `ApplyDAGDiff` 面对的 `reclaimed-0/1` 子桶**滞留在 T1 的旧 cpuset**（`6-7,54-55`/`29-31,73-79`，RC1 的直接后果），要把它们「drain」到能被新父目标 `11-16,59-64` 覆盖，本质是让子桶换到一个与现值零交集的新 NUMA 段——在同一趟、`maxSafeCPUSetWriteAttempts=3` 次紧邻重试（`safe_writer.go:617`）内无法完成，`reconcileLiveChildrenBeforeRetry`(`:786`) 是 best-effort、当拍未能改变子快照。
- 反证机制可行：T8（后续 reconcile 轮）正是用「union-first 超集过渡再收窄」成功写入同一父与目标，证明目标 `11-16,33-39,59-64,81-87` 本身可达，只要子桶先被 bridge/drain 到位。

> 缺陷定性：RC3 更准确说是 **RC1 的下游放大**——当 cgroup 子节点被 advisor 落地失败留在旧代，任何在窗口内进入的 admission 都会在单趟 apply 里撞上「子桶尚未换代」而无法在有界重试内收敛的父收缩。

---

## 4. 修复方案

三处联动修复，缺一不可。RC1 消除断层来源，RC2 收敛跳变幅度，RC3 提升单趟落地鲁棒性作为兜底。

### Fix-1（RC1，最高优先级）— advisor 落地失败时回滚 state / 或 cgroup 成功后才 commit

目标：让 advisor 的「写 state」与「写 cgroup」成为 all-or-nothing，消除「state 领先 cgroup 一整代」的持久窗口。二选一：

- 方案 A（推荐，事务化）：`applyBlocks`/`allocateByCPUAdvisor`(`policy_advisor_handler.go:487`) 在写 state 前对受影响 entries 做 snapshot；`runCPUSetAdjustmentHandlers`(cgroup 落地) 返回错误时，将 state 回滚到 snapshot，并让本轮 advice 整体失败（不 persist checkpoint）。
- 方案 B（延迟 commit）：调整为「先构造 `newEntries` → 先落 cgroup → 落地成功后再 `SetPodEntries`/`SetMachineState`/persist」。风险是 cgroup 写通常需要 state 已描述目标；实现成本高于方案 A。

验收：注入一次 `children_not_ready` 落地失败后，`GetCPUSetForPool(reclaim)` 仍返回失败前的旧值（不出现领先一代的 28 核 state）。

### Fix-2（RC2）— `generateReclaimBlockCPUSet` 引入 preferred 复用，对齐 admission 语义

- 从 `p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameReclaim)`（或 machineState reclaim 分配）取上一把 reclaim cpuset，按 NUMA 拆成 per-NUMA `preferredTiers`。
- 把 `:1118` `TakeByTopology` / `:1163` `TakeByNUMABalance` 的裸取核替换为「**先从旧 reclaim ∩ currentAvailableCPUs 取，不足再 spill**」的 tiered-preferred 逻辑，直接复用现成 `takeByTieredPreferredCPUs`(`policy_allocation_source_pool.go:73`) 思路。
- 效果：advisor 扩/缩 reclaim 时优先「就地扩、就地缩」，使 advisor 侧与 admission 侧对 reclaim 期望位置收敛一致，最大限度减少与 cgroup 现场的跳变。

验收：dedicated Pod 占核变化后，advisor 重算的 reclaim 与上一把在 NUMA 内交集最大化（跳变从「几乎不重叠」降到「仅让出被抢占核」）。

### Fix-3（RC3，兜底鲁棒性）— 父收缩换代时强制 bridge/drain，且跨窗口可续

- 当父目标与子桶现值在某 NUMA 段零交集（换代式收缩）时，`shrinkParentWithLiveChildUnion` 需保证在写最终 `writeNode(effectiveTarget)`(`safe_writer.go:199`) 前，reclaim 子桶已通过其自身受控过渡（`shrinkReclaimBucketWithDescendants:234`）换到新段；无法在本趟有界重试内完成时，**不应停在裸 `write_node` 硬撞 EBUSY**，而应保留父为合法超集（`target ∪ parked` 已有 `:183` 语义），把收敛留给下一轮 reconcile（即 T8 的成功路径），并向上返回「未收敛/需重试」而非 admission fail。
- 准入路径降级：`adjustPoolsAndIsolatedEntries`(`policy_allocation_handlers.go:1237`) `runCPUSetAdjustmentHandlers` 因父收缩 EBUSY 失败时，考虑不直接把 Pod 判成 `UnexpectedAdmissionError`，而是接受 state 分配、cgroup 交后台 reconcile 收敛（与 bulkhead 周期对齐），避免一次瞬时拓扑落后拒绝合法 Pod。

### 可观测性（配套）

- `logCPUSetSubtreeOnWriteFailure`(`safe_writer.go:707`) 的 `cpuset_write_failed` marker 目前 `rel` 是被写节点 cgroup 相对路径，池级（`kubesandbox`）不含 Pod uid，导致按 uid/name 过滤的诊断包（`bulkhead_common.sh:273-274`）**丢失全部池级 marker**。建议 marker 增补触发本次 apply 的 Pod uid/name 字段，保证诊断包可归因。

---

## 5. 验证计划

1. 单测：在 `safe_writer_test.go` 增「父目标与子桶零交集、子桶滞留旧代」用例，断言不产生裸 `write_node` EBUSY 而是走 bridge/drain 或返回未收敛。
2. advisor 事务性单测：注入 `runCPUSetAdjustmentHandlers` 失败，断言 state 回滚、`GetCPUSetForPool(reclaim)` 不前进。
3. 节点 E2E（`qrm-bulkhead-e2e` skill）：复跑 `rerun` 标准 3 轮 + high-churn 5 轮，验收 `FULL_E2E_DONE rc=0 final_reset_rc=0`，且 stable/recreate 阶段无 Pod `Failed`、无 `cpuset_write_failed` 终态、无 `device or resource busy`。

---

## PR summary (EN, for description)

**Root cause.** A dedicated numa_binding Pod admission writes the reclaim pool target from state onto the parent cgroup `kubesandbox`, while `kubesandbox/reclaimed-0/1` children still hold a previous-generation cpuset with zero intersection on NUMA0, violating the cgroup v1 parent-superset rule and yielding a deterministic EBUSY (3 identical-snapshot retries). The generation gap is created by the advisor: `applyBlocks` commits the 14→28 core reclaim expansion into state, but the same advisor loop fails to land cgroups at the top parent `kubepods` (`children_not_ready`) and does **not** roll back state; the next admission then faithfully writes the leading state onto lagging cgroups.

**Fixes.** (1) Make advisor state/cgroup writes all-or-nothing (rollback state on landing failure). (2) `generateReclaimBlockCPUSet` reuses the previous reclaim cpuset via tiered-preferred taking, matching the admission-side preserve-subtraction semantics, to minimize churn. (3) Harden the topology writer so a cross-generation parent shrink bridges/drains reclaim child buckets first and defers to the next reconcile instead of hard-failing the Pod; also tag `cpuset_write_failed` markers with the triggering Pod uid/name.
