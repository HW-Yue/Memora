# F220：Query Working Set（语义工作集）

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
实现[路线 v3](./roadmap-v3.md) Agent 轨道的多轮记忆，并冻结此前一直未定义的
「Query Workspace」（[ADR-0009](../decisions/0009-memora-owned-agent-loop.md) 留的坑）。

## 命名说明

用户提出时称为「buffer pool」。本文档改称 **Working Set**，因为
`internal/store/buffer` 已经是 Page 级 Buffer Pool；同一代码库里两个「Buffer Pool」
会造成持续混淆。语义、动机完全按用户原意：**缓存相邻查询大概率复用的 Row，
每条带完整语义索引链路，用一点上下文换时间。**

## 唯一主要结果

Query Agent 在一个 Session 内维护一个有界、可淘汰、带 revision 的语义工作集：
每个条目是一条读到过的 Row，附带它从 root 到 Leaf 的完整 Route 链路。
后续 turn 把工作集紧凑渲染进上下文，命中时直接作答或就近横向导航，不重新逐层下钻。

工作集**永远不能**把过期内容当作当前事实：任何 revision 或 commit sequence 不匹配
的条目必须先失效再使用，存疑即丢弃。

## 为什么带完整链路（而不只是 Row）

三个收益，第二个是主要的：

1. **同邻域追问零导航**：下一问命中已缓存 Row，直接回答；
2. **横向导航**：拿到链路后可以从兄弟 Leaf 出发，不必回到 root 重新下钻。
   这是只缓存 Row 拿不到的能力，也是本 Feature 的主要价值；
3. **负向记忆可表达**：「`/work/projects` 这一支查过，没有相关内容」是路径级事实，
   有链路才写得下来。

## 数据结构

现有类型已经够用，不需要新建索引机制：

- `router.Node` 已有 `Path`、`ParentID`、`Kind`、`Revision`、`Deleted`；
- `router.Locator` 已有 `DatabaseID/TableID/RowID/Revision`；
- `IndexedReader.Capture(ctx) (uint64, error)` 提供 commit sequence 水位线。

```text
WorkingSet {
  Watermark   uint64          // 建立/上次校验时的 Capture() 值
  Entries     []Entry         // 正向：读到过的 Row
  Negatives   []Negative      // 负向：探过且为空的路径/查询
  BudgetBytes uint64
}

Entry {
  DatabaseID, TableID, RowID  string
  Revision, CommitSequence    uint64
  RouteChain []{RouteID, Name, Path, Kind, Revision}   // root → leaf 有序
  Columns    map[string]any   // 实际读到的字段，不是整行
  LastUsedTurn uint64
  Pinned     bool             // 已作为已交付答案的 evidence
  Bytes      int
}

Negative {
  Kind  "route_empty" | "select_empty" | "route_missing"
  Path  string        // 或查询的规范摘要
  Turn  uint64
}
```

`Negative` 不存正文，成本极低，但它是阻止模型重复已失败查询的关键——
当前循环没有任何尝试记录，见[已知风险](../development/known-risks.md)第 1 条。

## 失效协议（本 Feature 的正确性核心）

命中过期数据会产出**高置信度的错误答案**，比慢一点的正确答案更糟。因此 fail closed：

1. 每个 turn 复用工作集前，执行一次 `Capture()` 得到 `W'`；
2. `W' == Watermark` → 全集有效，**零额外读取**（个人单用户场景的绝大多数情况）；
3. `W' > Watermark` → 有提交发生。v1 采取**保守全丢**：清空工作集、记一次
   `working_set_invalidated` 指标、本 turn 退化为冷启动；
4. 任何条目的 `Revision`/`CommitSequence` 与回表结果不一致 → 丢弃该条目，不得呈现；
5. Route 链路中任一节点 `Deleted` 或 `Revision` 变化 → 整条链路失效。

**v1 明确不做精确失效**（`ListCommittedChanges(after, snapshot)` 只淘汰受影响条目）。
原因有二：个人单用户在一次查询会话中几乎不写入，保守全丢的实际触发率接近零；
而 `ListCommittedChanges` 当前会抢单写者门（[已知风险](../development/known-risks.md)第 6 条），
每 turn 调用它会引入写竞争。**若将来要做精确失效，必须先修复风险 6。**

## 上下文预算

用户的取舍是「牺牲一点上下文换时间」，因此这个取舍必须是显式且可测的，不能隐式挤占。

- 工作集独立预算 `WorkingSetUTF8Bytes`，与 `BootstrapBudget.FrameUTF8Bytes`
  （默认 12 KiB、上限 64 KiB）分开计量，两者之和有明确总上限；
- 超预算时按 LRU 淘汰，`Pinned` 条目最后淘汰；
- **紧凑渲染是硬要求**：当前循环把整个 envelope `json.Marshal` 后塞进上下文
  （`query_agent.go` 的 `previousResult`），冗余极大。工作集必须渲染为
  紧凑表格式而非逐行 JSON；仅此一项预计就有 3–5 倍压缩。
- 淘汰不能只看 LRU：无关 Row 挤占上下文会**降低**准确率。需要同时考虑与当前问题的
  相关性，具体策略在 RED 阶段用对照确定，不预先冻结。

## 必须测量的东西

「相邻的查询大概率是一样的」目前是**假设，不是事实**。它对个人库很可能成立，
但必须从第一天就有数据，否则无法判断这个取舍是否划算：

- `working_set_hit` / `working_set_miss` / 命中率；
- `provider_calls_saved`、`turns_saved`、端到端延迟差；
- `working_set_bytes` 实际占用；
- `working_set_invalidated` 触发次数。

指标经 F204 Hook 与 F207 报告链路输出，不新建观测通道。

**冷启动 vs 预热**天然构成一组同模型、同语料、只变一个变量的对照，
正好符合 [ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md) 要的架构对照证据。

## 分两阶段交付

**Stage 1（v0，即路线 v2 的 A1）**：正向条目 + 链路、in-session、保守失效、
LRU + 预算、紧凑渲染、命中率指标。这一阶段本身就是多轮记忆缺陷的修复，
**不是会被丢弃的中间产物**。

**Stage 2（v1）**：负向记忆、相关性淘汰、（若指标支持）精确失效。

跨 Session 持久化不在本 Feature 范围，属路线 v2 阶段 B 的 topic 身份。

## 明确不做

- 不缓存最终答案。事实会变，答案缓存对个人库是错的；
- 不把工作集写入 Memora Database，它是 Session 内存态；
- 不因为有缓存就放宽 evidence 要求：答案仍必须由真实 SELECT 支撑；
- 不在 Stage 1 引入精确失效（见上）；
- 不用工作集绕过 Policy：缓存条目的可见性在写入工作集时确定，
  授权变化时整集失效。

## RED 与完成门

- RED 先证明当前循环无法跨两个以上 turn 保留任何 Row 或路径记忆；
- 同邻域追问在预热工作集下 provider calls 严格少于冷启动，且答案一致；
- 横向导航：命中兄弟 Leaf 不产生从 root 开始的下钻 MSQL；
- 写入使 `Capture()` 前进后，下一 turn 必须失效并冷启动，
  **不得**返回过期 Row；该用例必须有专门 golden；
- Row 被删除、Route 被 reshape、Route 别名变更三种情形各自 fail closed；
- 预算耗尽时 LRU 淘汰确定、`Pinned` 最后淘汰、渲染字节数不超预算；
- 授权变化后工作集不得跨权限泄漏任何条目；
- 命中率与节省指标出现在 Hook 快照与 F207 报告中；
- 目标测试、`-race`、Agent import allowlist 与完整 CI 全绿。

## 依赖与顺序

- **前置**：路线 v2 的 A2（零行 SELECT 终止导航的修复）。
  必须先修，否则循环只能走一跳，没有第二跳可以加速，命中率数字没有意义；
- **并列**：[F219](./f219-deterministic-answer-scoring.md) 确定性评分，
  用来验证预热没有以正确性换速度；
- **后续**：路线 v2 阶段 B 的跨会话 topic 身份。

## 关联

- [路线 v3](./roadmap-v3.md)
- [已知风险](../development/known-risks.md) 第 1、2、6 条
- [ADR-0009 Memora-owned 薄 Agent Loop](../decisions/0009-memora-owned-agent-loop.md)
- [ADR-0010 小规模高质量评测](../decisions/0010-small-scale-high-quality-evaluation.md)
