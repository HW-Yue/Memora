# Row Document View v1

状态：F119 已批准实现规格；2026-08-01 冻结。

## 用户结果与路由

Route leaf locator 中的 RowID 进入稳定深链路：

```text
/rows/:database_id/:table_id/:row_id
```

页面把当前 Row 渲染成完整动态文档，而不是横向 Row Grid。标题、摘要、字段顺序和
字段说明只来自 `row_detail` 与 result column metadata；缺少 title role 时按协议显示
RowID/revision，不猜 `title/body` 等列名。

## 有界 MSQL

```sql
SELECT * FROM "db_..."."tbl_..." WHERE row_id = :row LIMIT 1;
SHOW HISTORY FROM "db_..."."tbl_..." FOR ROW :row LIMIT 20;
SHOW HISTORY FROM "db_..."."tbl_..." FOR ROW :row CURSOR :cursor LIMIT 20;
```

Database/Table 使用严格 quoted stable ID；RowID 和 cursor 只作为 parameters。point
SELECT 返回当前完整字段，History 只返回 revision/provenance 元数据。F119 不执行
`AS OF` 正文读取，也不比较 revision；这些属于 F121。

## 投影与状态

系统字段成为紧凑状态条；业务字段按 columns 数组顺序成为纵向 section，显示 type、
purpose 和 semantic role。字符串保留换行，其他 JSON 值确定性转成只读文本；任何值
都只进入 DOM `textContent`。

页面具有 loading、empty、ready、truncated、permission、error/corrupt。严格验证
`memora.row-detail/v1` identity、Schema、display role、Column identity/order、point Row
identity，以及 History 的 Row scope/page snapshot。missing current Row 可以与已有 History
并存；不得把正常空结果猜成损坏或绕过 MSQL 回表。
