# Row Revision Diff v1

状态：F121 已批准实现规格；2026-08-01 冻结。

## 范围修正

F121 只比较同一个 RowID 的两个既有 revision。原计划写作“Row/Route revision”，但
F111 只提供 Route 当前 point/children/locator，既没有历史 Route point read，也没有
Route history cursor。UI 不得绕过 MSQL 读取 `nativerouter` 历史记录，因此 Route diff
明确延后到独立历史读取协议有真实用户证据时再规划。

## 用户结果与路由

Row History 和 Change Timeline 的 Row UPDATE/DELETE/RESTORE entry 可进入稳定深链路：

```text
/diffs/:database_id/:table_id/:row_id/:before_revision/:after_revision
```

页面显示两个 revision 的身份、状态和 Data Dictionary 驱动的逐字段 before/after。
不猜 `title/body` 列名，不把两个完整 JSON blob 当作不可读文本。

## 有界 MSQL 与预算

```sql
SELECT * FROM "db_..."."tbl_..." AS OF REVISION :before
WHERE row_id = :row LIMIT 1;
SELECT * FROM "db_..."."tbl_..." AS OF REVISION :after
WHERE row_id = :row LIMIT 1;
```

Database/Table 使用严格 quoted stable ID；RowID 和 revision 只作为 parameters。必须满足
`before < after`，每侧精确一行、同一 RowID，返回 revision 与请求一致。两个结果的
columns、Column ID/order、Row Detail identity/display role 必须一致。

正文按 UTF-8 JSON 编码后的两侧合计最多 512 KiB；越界拒绝投影，不截断、不隐藏差异。
逻辑字段只接受协议支持的 scalar/null。页面显示所有变化字段与未变化字段计数；字符串
保留换行，所有值只进入 `textContent`。

## 状态与边界

覆盖 loading、ready、empty/not_found、permission、corrupt、error 与 over_budget。
F121 不读取 History page、Route history、Relation、Trace 或 change envelope，不执行
current SELECT、restore、mutation，也不提供逐字符/语义 diff；v1 是确定性的字段级比较。
