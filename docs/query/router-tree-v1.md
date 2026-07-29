# Router Tree v1

状态：F22a 已冻结 Router 树、路径、membership、容量和 cursor；Row/MSQL 接入由 F22b 完成。

## 系统对象

每个 Database 有一个统一 Router root。节点使用稳定 `route_...` ID，包含 database/parent、name/aliases/path、kind、purpose、revision 和 deleted。

Router 是系统派生索引，不是 AI 自建业务表。内部按稳定 ID 引用；Agent 使用 `/项目/memora/存储引擎` 等路径。name 是最多 64 字符的 Unicode 小写路径段，允许字母、数字、`-`、`_`；purpose 必填且最多 800 字符。

rename 不改变 node ID，旧名称进入 aliases，旧路径继续作为 redirect。后代 current path 在同一事务递归更新，旧后代路径同样保留 redirect。

## 树与容量

root/branch 可以有多个 child，leaf 不能有 child。启动 fan-out 上限为 12；leaf 启动 Row 上限为 100。超过上限返回 constraint error，提示需要语义 split；引擎不能自行发明分支名或移动 membership。

AI 通过显式事务完成 split/merge：创建目标节点、为受影响 Row 提交完整 membership、删除或保留旧节点，再原子 commit。同一 Row revision 允许重复提交或重新组织 membership；更旧 revision 不得覆盖较新 locator。

## Membership

leaf 保存 `database_id + table_id + row_id + row_revision`，同一 Row 可属于多个 leaf。引擎同时维护 `Row locator → sorted leaf IDs` 反向索引。完整替换先预检所有目标 leaf 容量，再原子移除旧引用、增加新引用；失败不能先破坏旧 membership。

删除 leaf/子树时递归清除所有正反向引用。Row DELETE 和普通 UPDATE 缺少新 membership 时的自动失效/`pending_reindex` 由 F22b/F24 接入。

## 遍历

child 按 name 稳定排序，页大小为 1–100。cursor 是绑定 parent ID 与 offset 的不透明 base64url 值；跨 parent、损坏或越界 cursor 返回 validation error。leaf 只返回 locator，不返回 Row 正文。

## 关联

- [Agent 语义目录索引](./semantic-routing.md)
- [上下文生命周期](./context-lifecycle.md)
- [Row Store v1](../data/row-store-v1.md)
