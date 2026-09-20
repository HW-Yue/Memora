# Catalog Navigation v1

状态：F117 已批准实现规格；2026-08-01 冻结。

## 用户结果与路由

Admin 的第一个业务页面只浏览 Catalog：

```text
/catalog
→ /catalog/:database_id
→ /catalog/:database_id/:table_id
```

页面展示 Database 的 purpose/scope/schema version、Table 的 purpose/row semantics/schema
version，以及 Column 的名称/type/nullability/purpose/semantic role。稳定 ID 始终可见；
rename 后 URL 不失效。F117 不读取 Route、Row、History、Change 或 Trace。

## MSQL 旅程

```sql
SHOW DATABASES LIMIT 32 COMPACT;
DESCRIBE DATABASE "db_..." COMPACT;
SHOW TABLES FROM "db_..." LIMIT 32 COMPACT;
DESCRIBE TABLE "db_..."."tbl_..." COMPACT;
SHOW COLUMNS FROM "db_..."."tbl_..." LIMIT 32 COMPACT;
```

深链路用 point `DESCRIBE` 恢复当前对象，用相邻 `SHOW` 读取一层 children。Database/
Table ID 来自 F110 envelope，经双引号标识符规则转义；不得把 display name、HTML 或
任意 URL 文本直接拼接成 MSQL。continuation 只传不透明 cursor parameter，并固定
当前 scope 和 32 条预算。

## 页面状态

每一层都具有 loading、empty、ready、truncated、permission、error/corrupt 状态。
truncated 明示 snapshot 并由用户逐页加载；`revision_conflict` 不混合旧、新 snapshot，
提示刷新当前层。`permission_denied` 不显示对象是否存在；无效 envelope/version/字段
作为 corrupt contract，不猜字段或显示半套数据。

UI 只使用 DOM `textContent`/节点构造投影 Catalog 文本，不把名称、purpose 或错误消息
作为 HTML。浏览器历史使用 stable-ID path，Back/Forward 重新执行当前有界读取；
session 与 API client 继续复用 F115/F116。
