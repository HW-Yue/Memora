# B+ Tree Single-Node Upsert v1

状态：F93 已完成，2026-07-31；冻结未满 leaf/internal Node 的有序 upsert 契约。

- `UpsertLeaf` 在 leaf 内按字节序插入 `key/value`，重复 key 原位替换 value；
- `UpsertInternal` 插入 `separator/right_child`，重复 separator 替换 right child；
- 返回值明确 `Replaced`，输出 Node 与输入 key/value 完全深复制隔离；
- mutation 在私有候选 Node 上完成，再通过 F90 Encode 验证形状与 Page 容量；
- 任意 invalid/no-space 失败都不修改输入 Node，错误保持 `ErrInvalid`/`ErrNoSpace`；
- Page header、leaf next、internal level/leftmost child 保持不变；
- 结果 key 始终严格递增，Encode→Decode 后语义不变。

F93 只提供单 Node 纯 mutation，不读取 Page、不发布 Buffer Pool、不生成 WAL，不实现
split、parent propagation、root growth 或 delete。
