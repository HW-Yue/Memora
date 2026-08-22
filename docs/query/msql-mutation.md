# MSQL Mutation Executor v1

状态：F15e Executor 与 F16b batch transaction / Result Envelope 接线已实现。

> **目标形态已改——但 `route_leaf_ids` 的三态语义保留。**
> 本文冻结的「非 nil 完整快照 = 替换 / 缺省 = 保留 / DELETE = 清空」在
> [写入形态](../product/write-model.md)下不变，wire 面也不变。
> 变的是落地方式：不再写独立的 membership 记录，而是直接改叶子上的 RowID 字段，
> 并同事务更新反向索引树。
> 一并消失的是 membership 自带的 `MembershipRevision` 与墓碑——
> 挂载关系并进叶子后由叶子自己的 revision 接管。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码。
> 迁移设计见[叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

F31 起，Canonical Skill 的正式自主写入先通过版本化 Mutation Plan 和
Policy，再由本 Executor 执行；直接 `exec` 仍是底层逻辑 MSQL 入口，不能
替代写前发现、decision、完整语义快照和 verify。详见
[Skill 写入流程 v1](../agent/skill-write-v1.md)。

## Request options

MSQL source 与每条 statement 的 parameter、mutation guard、authorization 分字段提交。
下面是单条 `StatementInput`：

```json
{
  "parameters": {
    "named": {"title": "新标题", "row_id": "row_..."},
    "positional": []
  },
  "mutation": {
    "expected_schema_version": 3,
    "expected_revision": 7,
    "max_affected_rows": 1,
    "actor": "agent:codex",
    "source": "conversation:event-42",
    "reason": "用户确认新标题",
    "route_leaf_ids": ["route_project", "route_recent"]
  },
  "authorization": {
    "version": "memora.authorization/v2",
    "actor": "agent:codex",
    "authorized_databases": ["work"],
    "default_level": "L1"
  }
}
```

INSERT 要求非零 `expected_schema_version` 和 1–1000 的 `max_affected_rows`。UPDATE/DELETE 还要求非零 `expected_revision`。Guard 不属于 SQL 字符串，不能被参数内容或注释改变。

F17a 的 `actor`、`source` 和 `reason` 也属于结构化 options，并原样进入已提交 History provenance；它们不参与 Parser、predicate 或 value expression。

`route_leaf_ids` 是提交后完整 Router membership 快照，位于结构化 option 而不是
SQL source。非 nil 空数组显式清空；目标必须是同一 Database 的 leaf。快照、Row
revision、History、Route locator 和 Change Log 原子提交，DELETE 始终清空 membership。

普通 UPDATE 缺少 `route_leaf_ids` 时保留当前 membership，并把 locator revision
推进到新 Row revision；这只适用于语义边界没有改变的修改。INSERT 由 Skill 提交完整
快照，RESTORE 到 live Row 也强制要求完整快照。当前没有 Row 级 Agent posting 或
`pending_reindex` 队列。

## INSERT

- Column list 按 current name/alias 绑定稳定 `column_id`；省略时使用 Catalog 顺序；
- value expression 只能由 literal、parameter 和不读取 Row 的表达式组成；
- Column/value 数量、重复/未知 Column 和逻辑类型在写入前验证；
- F15 v1 每条 INSERT 只接受一个 VALUES row；多个 row 先检查影响预算，再返回 `validation_error`，零写入。

## UPDATE 与 DELETE

Planner 先形成完整候选，再执行任何写入：

1. 绑定全部字段和 parameter；
2. `row_id = value` 走稳定 ID 精确读取，否则在有界窗口内计算 predicate；
3. 候选超过 `max_affected_rows` 返回 `constraint_violation`；
4. 扫描窗口不完整返回 `output_truncated`，不得写入；
5. F15 的单一 expected revision 只允许最终候选为一个 Row；
6. Row Service 在写事务中再次检查 schema/revision 后提交。

零候选正常返回 `affected_rows = 0`。多候选即使未超过较大的 max，也在 F15 返回 `validation_error`；后续批量 revision 协议不能复用一个 expected revision 覆盖多 Row。

## 结果

成功 INSERT/UPDATE/DELETE 返回：

- `affected_rows = 1`；
- 提交后的 Row `revision`；
- 本次事务的 `commit_sequence`；
- 非 nil 的空 `columns[]/rows[]`。

预算、类型、revision 或参数失败不产生部分写入。DELETE 仍是 Row Store 的逻辑 tombstone。

## 注入边界

Executor 只遍历 Parser AST、绑定参数并调用 Catalog/Row 契约。它不生成 SQLite SQL，也不把参数值重新交给 Lexer。包含 `'`、`;`、`--`、`)` 或 `DELETE` 等文本的参数只能作为 Column value 保存。

## 关联

- [MSQL 参数与表达式 v1](./msql-expressions.md)
- [MSQL SELECT Planner v1](./msql-select.md)
- [Row Store v1](../data/row-store-v1.md)
- [数据库 Mutation Agent](../agent/database-mutation-agent.md)
