# F227：Row / Table / Database 的删除

状态：候选（2026-08-20）。当前 Table 与 Database **完全无法删除**，
而 SKILL.md 让 Agent 以为可以——这是能力与承诺不一致，不是缺失的优化项。

## 实测现状：三层里两层是空的

| 对象 | 入口 | 语义 | 结论 |
|---|---|---|---|
| Row | `DELETE FROM db.t WHERE …` | `State=deleted`、`Revision++`、Route 归属清空为 `[]`、追加 `history.OperationDelete`、fulltext 投影为 `StateDeleted`；`RESTORE` 可回来 | **已完备，不改** |
| Route | `DELETE ROUTE :id` → `router.DeleteNodeIn`（`service.go:344`） | 递归 `deleteSubtree` 打 `Deleted=true`，反向归属解开，root 拒绝删除 | 已有，缺删除后的补偿 |
| Column | `PLAN SCHEMA CHANGE` → `APPLY SCHEMA CHANGE`（`ActionDrop`） | `Impact.Destructive=true, Reversible=false`，不产出 compensation | 已有 |
| Table | 无 | 无 `catalog.DropTable`，MSQL 无 `DROP` 分支 | **本 Feature** |
| Database | 无 | 无 `catalog.DropDatabase`，CLI 28 个子命令无一涉及 | **本 Feature** |

`DROP` 已在 `lexer/token.go:52` 保留为关键字，但 `parser.go:73` 的语句分发里没有它。
`DROP DATABASE w` 报 `unsupported_statement`，`DELETE DATABASE w` 报 `expected FROM`。

## 设计主张

### 1. 删除是状态迁移，不是物理擦除

三条硬约束决定了这一点，不是保守偏好：

- MVCC 读者持 statement snapshot，durable-then-publish／no-steal 纪律里**没有**
  从活跃快照下抽走 Page 的机制；
- History 是产品承诺，`SHOW HISTORY` 必须仍能解释发生过什么；
- F151 Compaction 已延后，立即擦除也不会还给用户磁盘。

所以形状只能是 **tombstone → 保留期 → PURGE**。

### 2. 容器的可见性来自容器自身，不下沉到每一行

Table/Database 打上 `state=dropped` 后，其中的 Row **一行都不重写**。
`SHOW DATABASES`／`SHOW TABLES`／Catalog Atlas／Bootstrap Frame／
`visibleLexicalDatabaseIDs`（`executor/lexical_locations.go:52`）／Route 解析
统一按容器状态过滤。

理由是硬的：5,000 行的 Table 不该为一次 drop 产生 5,000 次 Row 修订；
而 F226 之后 poison 按库收敛，这种批量写正是最容易毒化该库的写入形态。

### 3. 容器删除走两阶段审批，不给一行式 DDL

不引入 `DROP TABLE x` / `DROP DATABASE x` 这种单句 DDL。对一个 Agent 驱动的系统，
一个绑错的参数就抹掉一整个知识域，这个 affordance 本身就是缺陷。沿用 Schema Change
已经验证过的信封：

```text
PLAN DROP TABLE work.notes USING :proposal      → memora.object-drop-plan/v1   (L0)
PLAN DROP DATABASE work USING :proposal         → memora.object-drop-plan/v1   (L0)
APPLY DROP PLAN :plan                           → memora.object-drop-receipt/v1 (L2)
```

- proposal 为 `memora.object-drop-proposal/v1`，含 `expected_revision`；
- `APPLY` 绑定 `authorization.approval.action=APPLY_OBJECT_DROP` 与
  `subject_sha256`（plan hash `sha256:` 之后的 64 位十六进制）；
- **Database drop 额外要求 `confirm_name` 与目标库名逐字符相等**。参数绑错是
  Agent 场景真实的失败模式，这一条专门拦它；
- `ReadOnly=true` 或 `PackageSHA256 != ""` 的 Database 拒绝 drop——那属于包卸载，
  本 Feature 不做。

### 4. PURGE 是独立操作，且对空间回收说实话

```text
PURGE DROPPED TABLE work.notes                  (L2)
PURGE DROPPED DATABASE work                     (L2)
```

前置：对象已处于 `dropped`，且已过 `lifecycle_policy.drop_retention`（默认 7 天）。
`FORCE` 可跳过保留期，但需要独立审批。

回执必须写明：PURGE 释放的是 key 与 Page，Page 进入 F152 空闲页复用，
**文件不会变小**，直到 F151 Compaction 落地。不允许在任何文案里暗示"删除会释放磁盘"。

## 各层级的副作用表

| | Row DELETE（已有） | Table DROP | Database DROP |
|---|---|---|---|
| Catalog | — | `state=dropped` + `dropped_at` | 同左 |
| Row | 单行 tombstone | 不动，随容器隐藏 | 不动，随容器隐藏 |
| Route | 归属清空为 `[]` | 整棵树按容器隐藏；PURGE 时删除。`root cannot be deleted` 仅在容器 drop 内部解除 | 该库全部树 |
| Lexical/fulltext | 投影 `StateDeleted` | 从可见集过滤；PURGE 时清 posting | 同左 |
| Relation | 保留（可悬空） | **不级联删除**，见下 | 同左 |
| History | 保留 | 保留 | 保留 |
| Change log | 1 条 | **1 条** `object_drop`，不是 N 条 | 1 条 |
| 可逆 | `RESTORE` | `RESTORE DROPPED TABLE`（PURGE 前） | `RESTORE DROPPED DATABASE`（PURGE 前） |
| PURGE 之后 | — | 不可逆 | 不可逆 |

### 悬空 Relation 的处置

`RELATE` 跨 Table。容器被 drop 后指进去的 Relation 必须有确定语义。

**决定：不级联删除。** 级联是无界写入，而且会让 `RESTORE` 变成无法还原的操作。
改为：Relation 解析走同一套容器可见性过滤，端点落在 dropped 容器里的 Relation
**被隐藏而不是被破坏**；PURGE 时随容器一并清除；清不掉的由新增健康发现报出。

## 语义健康的联动

新增两项 `semantichealth.Kind`：

- `dangling_relation`（review_required）——端点已 PURGE 而 Relation 仍在；
- `dropped_object_pending_purge`（low_risk）——告诉用户空间仍被占用及剩余保留期。

同时必须修改既有判定：`unrouted_row`、F224 强制 Route、F225 强制 summary
**一律跳过 dropped 容器内的 Row**，否则一次 drop 会把健康报告淹掉。

## 分阶段

- **Stage 0（文档，立即）**：SKILL.md 写明 Table/Database 当前不可删除，
  Agent 不得承诺；唯一可用的是 Row `DELETE`、`DELETE ROUTE`、`DROP_COLUMN`。
- **Stage 1**：Table 的 plan／apply／restore／purge 与容器可见性过滤。
- **Stage 2**：Database 同上，含 `confirm_name` 与只读/包库拒绝。
- **Stage 3**：`lifecycle_policy.drop_retention` 配置项、两项健康发现、
  F224/F225 的 dropped 豁免。
- **Stage 4**：SKILL.md 与两个 adapter、CLI 子命令、Admin UI 的回收站视图。

## 明确不做

- 不引入一行式 `DROP` DDL；
- 不为 drop 级联重写 Row，不级联删除 Relation；
- 不做跨 Database 的原子 drop（一个 plan 一个库）；
- 不承诺 PURGE 会缩小文件——那是 F151；
- 不做 Database Package 卸载。

## RED 与完成门

- RED 先证明：`DROP TABLE`／`DROP DATABASE` 当前是 `unsupported_statement`；
- drop 后：`SHOW TABLES`／Catalog Atlas／Bootstrap Frame／
  `SHOW LEXICAL LOCATIONS FROM ALL TABLES` 均不再出现该对象，且其中 Row 的
  `revision` **一个都没变**；
- drop 是**一条** change log 记录，不随行数增长；
- `RESTORE DROPPED` 后对象与全部 Row 原样可见，`revision` 仍未变；
- PURGE 需要保留期或 `FORCE`＋独立审批；PURGE 后 `RESTORE DROPPED` 失败；
- `confirm_name` 不匹配、approval 的 `subject_sha256` 不匹配、plan 过期：均硬失败；
- 只读库与包安装库拒绝 drop，错误点明原因；
- 端点被 PURGE 的 Relation 产出 `dangling_relation`；
- dropped 容器内的 Row 不产生 `unrouted_row`；
- 回执写明 Page 进入空闲页复用而文件不缩小。

## 关联

- [执行计划](./execution-plan.md)
- [F224 Row 必须可导航](./f224-mandatory-row-route.md)
- [F225 Row 必须可展示](./f225-mandatory-row-summary.md)
- [F226 Database 级故障隔离](./f226-per-database-fault-isolation.md)
- [已知风险](../development/known-risks.md)
