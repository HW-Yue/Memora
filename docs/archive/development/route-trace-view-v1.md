# Route Trace View v1

状态：F122 已完成；2026-08-01 冻结并通过真实浏览器验收。

## 用户结果与路由

Admin 的 `Route Traces` 导航先选择 Database，再浏览固定快照 timeline：

```text
/traces
/traces/:database_id
/traces/:database_id/:table_id/:trace_id
```

detail 按 ordinal 展示候选 Route、选择、回退、locator、outcome、单步耗时与剩余预算。
它是结构化观察收据，不显示 prompt、问题正文、节点描述、Row values、模型输出或推理。

## 有界 MSQL

```sql
SHOW DATABASES LIMIT 32 COMPACT;
DESCRIBE DATABASE "db_..." COMPACT;
SHOW ROUTE TRACES IN DATABASE "db_..." LIMIT 20;
SHOW ROUTE TRACES IN DATABASE "db_..." CURSOR :cursor LIMIT 20;
SHOW ROUTE TRACE :trace IN DATABASE "db_..." LIMIT 24;
SHOW ROUTE TRACE :trace IN DATABASE "db_..." CURSOR :cursor LIMIT 24;
```

Database/Table/Trace 使用稳定 ID；trace 和 cursor 只作为 parameters。timeline cursor 固定
high-water/retention epoch；step cursor 固定 trace checksum/scope。detail step result 明确
携带 `database_id/table_id`，用于验证深链路 scope；候选与 locator 空集确定编码为 `[]`。

## 投影与预算

- Database 每页 32、trace summary 每页 20、step 每页 24；最多 64 step；
- timeline 严格递增 sequence，step 严格递增 ordinal，续页不得重排、重复或换 snapshot；
- 每个 step 最多 24 个候选和 24 个 locator；Route/RowID 均可进入既有稳定页面；
- 页面以纵向 step flow 表达 `candidates → selected → locator/outcome`，不是 hidden
  chain-of-thought，也不根据结果脑补 AI 原因。

## 状态与边界

覆盖 loading、empty、ready、truncated、permission、corrupt、error 与
revision_conflict。所有文本只进入 `textContent`。

F122 不记录 trace、不修改 retention、不读取 Row 正文、不展示 prompt/描述/推理，也不
增加 predictor provenance、token 或模型调用成本；这些仍由宿主收据和后续 Feature 决定。
