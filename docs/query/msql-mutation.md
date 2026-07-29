# MSQL Mutation Executor v1

状态：F15e Executor 与 F16b batch transaction / Result Envelope 接线已实现。

## Request options

MSQL source、parameter values 和 mutation guard 分字段提交：

```json
{
  "source": "UPDATE work.notes SET title = :title WHERE row_id = :row_id",
  "parameters": {
    "named": {"title": "新标题", "row_id": "row_..."},
    "positional": []
  },
  "expected_schema_version": 3,
  "expected_revision": 7,
  "max_affected_rows": 1
}
```

INSERT 要求非零 `expected_schema_version` 和 1–1000 的 `max_affected_rows`。UPDATE/DELETE 还要求非零 `expected_revision`。Guard 不属于 SQL 字符串，不能被参数内容或注释改变。

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
- 非 nil 的空 `columns[]/rows[]`。

预算、类型、revision 或参数失败不产生部分写入。DELETE 仍是 Row Store 的逻辑 tombstone。

## 注入边界

Executor 只遍历 Parser AST、绑定参数并调用 Catalog/Row 契约。它不生成 SQLite SQL，也不把参数值重新交给 Lexer。包含 `'`、`;`、`--`、`)` 或 `DELETE` 等文本的参数只能作为 Column value 保存。

## 关联

- [MSQL 参数与表达式 v1](./msql-expressions.md)
- [MSQL SELECT Planner v1](./msql-select.md)
- [Row Store v1](../data/row-store-v1.md)
- [数据库 Mutation Agent](../agent/database-mutation-agent.md)
