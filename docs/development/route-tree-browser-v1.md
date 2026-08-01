# Route Tree Browser v1

状态：F118 已批准实现规格；2026-08-01 冻结。

## 用户结果与路由

Admin 从 Catalog 的 Table 页面进入该 Table 的权威语义 Route Tree：

```text
/routes
/routes/:database_id/:table_id
/routes/:database_id/:table_id/:route_id
```

`/routes` 只引导用户从 Catalog 选择 Table，不另造跨库扫描。Table 路由展示第一层
Route；node 路由 point-read 当前 node，再按 kind 读取一层 children 或 leaf locator。
URL 只使用 stable ID，rename 后仍可刷新、Back/Forward。

## 有界 MSQL

```sql
DESCRIBE TABLE "db_..."."tbl_..." COMPACT;
SHOW ROUTES FROM TABLE "db_..."."tbl_..." AT ROOT LIMIT 12;
DESCRIBE ROUTE :route;
SHOW ROUTES UNDER :route [CURSOR :cursor] LIMIT 12;
OPEN ROUTE :route [CURSOR :cursor] LIMIT 20;
```

Route ID 和 cursor 只作为 statement parameters；Table stable ID 使用与 Catalog 相同的
严格 quoted-identifier 规则。页面一次只保留当前 node、当前层最多 12 个 child 或
20 个 locator。continuation 必须保持同一 snapshot；冲突时不混入旧树。

## 投影与状态

node 只显示 name/path/kind/purpose/revision，以及 point `DESCRIBE` 返回的按需 synopsis。
leaf 只显示 `database_id/table_id/row_id/revision` locator，不读取 Row 字段、正文、History
或 relation；Row document 链接留给 F119。

root、branch、leaf 都有 loading、empty、ready、truncated、permission、error/corrupt。
返回值必须严格验证 envelope/page、stable ID、kind、parent 和 Database/Table scope；
页面只用 DOM node/`textContent`，不得投影内部错误、拼接 HTML 或使用 Web Storage。
