# Router Tree v1

状态：F22 历史实现说明。当前实现以 Database 为 root；产品目标已改为 Table
为 root，见 [AI-native 产品宪章](../product/ai-native-product-charter.md)。
在完成迁移前，本文件不能作为最终查询协议或 Feature 完成证据。

## 系统对象

F22 当前实现中，每个 Database 有一个统一 Router root。节点使用稳定
`route_...` ID，包含 database/parent、name/aliases/path、kind、purpose、
revision 和 deleted。目标架构要求每个 Table 拥有独立 root，Database 只通过
`SHOW TABLES` 提供表级发现，不能继续让一棵整库树混合不同 Row 语义。

Router 是系统派生索引，不是 AI 自建业务表。内部按稳定 ID 引用；Agent 使用 `/项目/memora/存储引擎` 等路径。name 是最多 64 字符的 Unicode 小写路径段，允许字母、数字、`-`、`_`；purpose 必填且最多 800 字符。

rename 不改变 node ID，旧名称进入 aliases，旧路径继续作为 redirect。后代 current path 在同一事务递归更新，旧后代路径同样保留 redirect。

## 树与容量

root/branch 可以有多个 child，leaf 不能有 child。启动 fan-out 上限为 12；leaf 启动 Row 上限为 100。超过上限返回 constraint error，提示需要语义 split；引擎不能自行发明分支名或移动 membership。

AI 通过显式事务完成 split/merge：创建目标节点、为受影响 Row 提交完整 membership、删除或保留旧节点，再原子 commit。同一 Row revision 允许重复提交或重新组织 membership；更旧 revision 不得覆盖较新 locator。

## Membership

leaf 保存 `database_id + table_id + row_id + row_revision`，同一 Row 可属于多个 leaf。引擎同时维护 `Row locator → sorted leaf IDs` 反向索引。完整替换先预检所有目标 leaf 容量，再原子移除旧引用、增加新引用；失败不能先破坏旧 membership。

`route_leaf_ids` 是提交后完整 membership 快照：非 nil 空数组表示显式清空，字段缺失表示本次未提供语义重建结果。INSERT/UPDATE/RESTORE 在同一 Row transaction 中替换快照并写入提交后的 Row revision；失败或 Batch rollback 不留下 Row、正向 locator 或反向索引的部分状态。Row DELETE 始终清空 membership。

普通 UPDATE/RESTORE 未提供新快照时，旧语义 locator 立即失效，并为新 Row revision 原子写入 durable `pending_reindex`；机械索引不依赖该异步结果。

删除 leaf/子树时递归清除所有正反向引用。

## 遍历

child 按 name 稳定排序，页大小为 1–100。cursor 是绑定 parent ID 与 offset 的不透明 base64url 值；跨 parent、损坏或越界 cursor 返回 validation error。leaf 只返回 locator，不返回 Row 正文。

## MSQL

以下是当前已实现的 Database 级历史语法，不是最终目标：

所有动态值都使用 literal/parameter，不接受字符串插值：

```sql
CREATE ROUTE ROOT FOR DATABASE :database PURPOSE :purpose;
CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose;
ALTER ROUTE :route RENAME TO :name;
DELETE ROUTE :route;
SHOW ROUTES FROM DATABASE :database AT :path CURSOR :cursor LIMIT :limit;
SHOW ROUTES UNDER :parent CURSOR :cursor LIMIT :limit;
OPEN ROUTE FROM DATABASE :database AT :path LIMIT :limit;
OPEN ROUTE :leaf LIMIT :limit;
```

CREATE 返回新节点的稳定 ID、path、kind、purpose 和 revision。ALTER/DELETE 要求结构化 `expected_revision`；三种写操作都要求 `max_affected_rows`。SHOW 返回节点元数据和可继续读取的 `next_cursor`；空 cursor 表示第一页。OPEN 只接受 leaf，结果严格为 Database/Table/Row/revision locator，不返回业务正文。

Database name 先绑定 Catalog stable ID；因此新会话可从 `/` 冷启动，不需要预先记住 root ID。上述写操作参加普通显式 Batch 事务，任一写失败时节点、路径和 membership 一起回滚。

目标语法从 `DESCRIBE TABLE` 后进入：

```sql
SHOW ROUTES FROM TABLE :qualified_table AT ROOT LIMIT :limit;
SHOW ROUTES UNDER :parent CURSOR :cursor LIMIT :limit;
OPEN ROUTE :leaf LIMIT :limit;
```

迁移必须保留稳定 RowID、revision、membership 原子性和有界 cursor，并给现有
Database 级树提供显式转换或拒绝路径。

## 关联

- [Agent 语义目录索引](./semantic-routing.md)
- [上下文生命周期](./context-lifecycle.md)
- [Row Store v1](../data/row-store-v1.md)
