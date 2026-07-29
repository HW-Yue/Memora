# Row Store v1

状态：F15a 已实现 Insert/Get/List；revision mutation 与 MSQL 执行由 F15b/F15c 补齐。

## 逻辑身份

每条 Row 创建一次稳定 `row_<uuid>`，并保存：

```text
row_id, database_id, table_id, schema_version,
revision, row_state, values, created_at, updated_at
```

创建时 `revision = 1`、`row_state = live`。Row 不保存可变 Database、Table 或 Column 名称；业务值以稳定 `column_id → value` 编码。读取时按当前 Catalog 投影名称，因此 rename 不复制 Row，也不改变身份。

## Schema 与值校验

每次写入必须携带 `expected_schema_version`。Row Service 在同一个 Store transaction 中读取 Catalog 和写 Row；版本不一致返回 `revision_conflict`。

Insert：

- 拒绝未知 Column 和同一 Column 的 current name/alias 重复输入；
- 缺失 nullable Column 自动保存为 NULL；
- 缺失 `NOT NULL` Column 返回 `constraint_violation`；
- 所有值经过 [逻辑类型与字段预算 v1](./logical-types.md)；
- 任一字段失败时不写 Row，也不更新 Table Row index。

## 原型持久化

Row 和每表稳定 Row ID index 都只通过 `Store`/`Tx` 读写。SQLite bucket、SQL、rowid 和 schema 不进入 Row 层。

JSON 只是原型 Store value 的版本内编码。读取使用 JSON number 保留整数精度，再按当前 Column 类型规范化；Timestamp 重新规范化为 UTC。关闭 Store 后重开必须得到相同 ID、revision 和值。

## 读取边界

- `Get` 只返回 live Row；
- `List` 按稳定插入顺序返回 live Row；
- 单次内部 List limit 必须在 1–1000，F15c Planner 还会应用查询和影响行数预算；
- 返回字段使用当前 Column 名，不回显废弃 alias。

## 关联

- [语义记录模型](./semantic-records.md)
- [Catalog v1](./catalog-v1.md)
- [逻辑类型与字段预算 v1](./logical-types.md)
- [SQLite 原型 Store](../decisions/0001-prototype-store.md)
