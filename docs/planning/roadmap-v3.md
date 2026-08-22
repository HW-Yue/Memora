# 路线 v3：按最高准则重排

状态：**2026-08-22 生效**。取代[路线 v2](../archive/planning/roadmap-v2.md)。

> **要派发工作请用[执行计划](./execution-plan.md)。** 本文说明**为什么是这个顺序**；
> 执行计划是编号工作队列，每项带前置、改动范围、RED 与完成判据。

前置阅读：[写入形态](../product/write-model.md)、[查询形态](../product/query-model.md)、
[架构原则](../product/architecture-principles.md)——这三份是最高准则，本文的依据。

## 为什么重排

v2 建立的四层文档结构是好的，**保留不动**：

| 文档 | 回答 |
| --- | --- |
| [系统能力](../product/system-capabilities.md) | 现在是什么 |
| [已知风险](../development/known-risks.md) | 哪里有问题 |
| [架构审计](../development/architecture-audit-2026-08.md) | 某一时点的实测清单 |
| 本文档 | 接下来做什么、**为什么是这个顺序** |
| [执行计划](./execution-plan.md) | **派发什么工单** |
| [Feature 状态](./feature-status.md) | 某能力的历史证据 |

变的是**排序依据**。v2 写于 2026-08-11，依据是「AI-native 的五个差距」。
三份最高准则 2026-08-22 才确立，于是出现一个明显的错位：

> **最高准则说了要什么，工作队列派发的是别的东西。**
> v2 的 14 项工单里**没有一项**来自这三份准则。

v3 把排序依据换成「准则符合性 + 架构审计」，并把 v2 的五个差距整体收进
Agent 轨道一节——**它们仍然有效，只是不再排在最前**。

## 两条轨道

### 引擎侧：准则直接要求的（当前优先）

三份准则要求的结构，逐条核实后**全部未做**。详见
[存储层总览「已知偏差」](../storage/README.md)与执行计划的 E 阶段。

已经做到的部分要说清楚，免得清单只剩抱怨：查询形态 §1–§5 的有界导航链路已成立；
**§7 删除的契约刚刚闭合**（`a3a6eaf`，`AS OF` 是最后一个漏的读面）；
写入形态 §4 的 fanout 上限（F223）与一叶一活跃行（F169）已实现；
架构原则 §1 的耦合**方向**本来就是对的——写路径不 import 任何检索包，
只经 `PageAuthority` 接口，真正的耦合点集中在 `pagestoremigration` 一处。

### Agent 侧：v2 的五个差距（转后，保留）

1. **Agent 不会导航**——循环只有一步记忆，第一条 SELECT（哪怕零行）就终止；
2. **没有跨轮与跨会话的记忆身份**——追问无法工作；
3. **写入决策没有反馈回路，且不可逆**——原文回收后语义分解不可重建；
4. **Route 结构建了但不自治维护**——10k Row 时的 fan-out 行为未定义；
5. **AI 是自带的**——Skill-first 的**有意选择**，不是缺陷。

## 为什么引擎优先

三条理由，按分量排：

1. **准则是最高优先级，而引擎侧是它直接要求的。** 让队列继续派发准则之外的东西，
   等于把"最高准则"降格成一份没人执行的文档；
2. **地基改完再动 Agent，才不会白写。** 引擎侧要动 Row 结构、语义索引挂载方式、
   RowID 形态与 history 的存法——Agent 侧的工作集、导航与写入约束全都建立在这些之上。
   顺序反过来，Agent 侧要返工两遍；
3. **`internal/agent` 不在产品二进制里。** 它 14,078 行，是仓库最大的单个包，
   却**不在 `cmd/memora` 的依赖图内**——只被四个评测/发布二进制引用
   （判据与复现命令见[架构审计](../development/architecture-audit-2026-08.md) §四）。
   加上 [ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)
   已把批量评测降优先级，这条轨道当前不构成产品阻塞。

**Agent 侧一项不删**，只改前置与顺序。差距 1 仍然是真实的产品缺陷，
只是它的修复不该排在地基之前。

## 引擎侧为什么是这个顺序

| 位置 | 理由 |
| --- | --- |
| WAL 回收接线排最前 | 清单里**唯一随时间恶化**的一条：WAL 从不滚段、从不 checkpoint、从不回收。代码已写已测、文档已冻结，只差接线 |
| 检索只给路径紧随其后 | 设计已写、小而独立，一次拿下一整条准则。`routelexical` 甚至已经读到了路径又丢掉 |
| 派生索引解耦放在结构改动之前 | 有现成模板（`authorityChangeTree.reconcile`）。先做它，`PublishMutation` 少三棵树四个 checkpoint，**后面每一项都更好做** |
| 每表一棵树 + history 成表合并 | 它们是同一套机制，分开做等于把「按表切分」写两遍、升两次 generation |
| 三份日志排最后 | 风险最高（动恢复），且 binlog 应当记录**定型后**的结构，而不是记完再改 |

一处必须重排的依赖：**F224「Row 必须可导航」要放到「叶子直挂 RowID」之后**——
它的判据是「有没有 live membership」，而 membership 正要被取消，
判据要改成「有没有叶子指向它」，由行上的 `route_leaf_ids` 回答。

## 明确不做

沿用 v2 的边界，并补充本轮已裁定的：

- 不因为 F 编号显示「已完成」就认为对应能力已验证——
  WAL 那三份文档写着「已完成」却零调用方，是这条的实例；
- 不把 OCR 引擎、本地 embedding 权重或浏览器运行时并入主安装包；
- 不以扩大评测样本量替代修复导航缺陷；
- **不改导出面对已删除 Row 的过滤行为**（2026-08-22 裁定可接受，只记录）；
- **不废弃向量检索**（方向保留，缺的是生产发布方）；
- 保留 v2「不在当前路线」的全部条目：F226 Stage 2、大语料批量评测、
  OCR/视觉运行时、内置 `memora ask` 产品化、Compaction／Secondary Index／
  Advanced MVCC／Replication／PITR／多设备同步。

## 关联

- [写入形态](../product/write-model.md)、[查询形态](../product/query-model.md)、
  [架构原则](../product/architecture-principles.md)
- [执行计划](./execution-plan.md)、[架构审计](../development/architecture-audit-2026-08.md)
- 迁移设计：[叶子直挂 RowID](../storage/leaf-rowid-v1.md)、
  [每表一棵树](../storage/per-table-tree-v1.md)、
  [候选预测器只给路径](../query/predictor-path-only-v1.md)
- [路线 v2](../archive/planning/roadmap-v2.md)（已归档）
