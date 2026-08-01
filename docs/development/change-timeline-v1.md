# Change Timeline v1

状态：F120 已批准实现规格；2026-08-01 冻结。

## 用户结果与路由

Admin 的 `Changes` 导航进入有界 Database 选择页；选择后进入：

```text
/changes/:database_id
```

页面按 committed change sequence 从早到晚展示完整事务 summary。事务 entry 默认不读，
只在用户展开一笔事务时有界读取，因此一个大事务或大量 Row 不会一次进入浏览器上下文。
F120 展示 metadata 和 locator，不读取 Row 正文，也不计算 before/after diff。

## 有界 MSQL

```sql
SHOW DATABASES LIMIT 32 COMPACT;
DESCRIBE DATABASE "db_..." COMPACT;
SHOW CHANGES IN DATABASE "db_..." LIMIT 20;
SHOW CHANGES IN DATABASE "db_..." CURSOR :cursor LIMIT 20;
SHOW CHANGE :transaction IN DATABASE "db_..." LIMIT 32;
SHOW CHANGE :transaction IN DATABASE "db_..." CURSOR :cursor LIMIT 32;
```

Database 使用严格 quoted stable ID；transaction ID 与 cursor 只作为 parameters。timeline
cursor 固定 high-water snapshot，entry cursor 固定 transaction checksum/scope。UI 不从
Repository、Store、Page 或 change envelope 文件读取。

## 投影与预算

- 首屏与每次 timeline 续页最多 20 笔事务；顺序必须严格递增且不得重复；
- summary 显示 sequence、时间、actor/source/reason、entry count 与短 transaction ID；
- 单次展开最多 32 个 entry，显示 object kind/ID、operation、revision、Schema、History
  locator 与 related IDs；entry 页保持 canonical order且不得重复；
- Database scope 查询只能返回所选 Database 的可见 scope；跨库事务不向单库 Admin
  暴露其他 Database ID。

## 状态与边界

页面覆盖 loading、empty、ready、truncated、permission、corrupt、error 与
revision_conflict。所有服务端文本只进入 `textContent`；响应必须严格匹配冻结 columns、
page、stable identity、枚举、事务与 Database scope。

F120 不做 retention、反向/最新优先协议、正文读取、historical body、revision diff、Route
trace、同步、PITR 或 mutation。反向时间线需要独立读取协议证据，不能由 UI 猜测。
