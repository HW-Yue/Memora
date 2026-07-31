# B+ Tree Leaf Delete v1

状态：F95 已完成；冻结单 leaf 物理 key 删除与 tombstone handoff 边界。

## 唯一行为

`DeleteLeaf(header, node, key)` 从一个已通过 F90 校验的 leaf Node 中删除精确 key，
返回新的 Node 与被移除 value 的深复制：

- key 按原始字节精确匹配，不做文本、前缀或大小写归一化；
- 删除 front/middle/back 后其余 entries 顺序和值 byte-identical；
- leaf 的 level、next link 和 Page identity 不变；
- 删除唯一 entry 可以得到合法空 root leaf；非 root 的占用不足暂时保留给 F96；
- empty key/错误 Header/错误 Node 原子返回 `ErrInvalid`；
- key 不存在原子返回 `ErrNotFound`，不生成伪 tombstone；
- 输出 Node、removed value 与输入完全深复制隔离，并通过 F90 Encode/Decode。

## Tombstone handoff

B+ Tree value 是不透明字节。Row/Table 逻辑 DELETE 必须由上层生成新的 tombstone
state/value，再使用 upsert 路径替换旧 value；不能调用本物理删除假装逻辑 tombstone。
`DeleteLeaf` 返回 removed value，只作为上层维护机械索引、补偿或审计的 handoff，不
解释 Row revision/state。

F95 不删除 internal separator，不修改 parent，不判断 fill factor，不 borrow/merge，
不 shrink root，也不读取 Page、发布 Buffer Pool 或生成 WAL；这些结构与持久化行为分别
属于 F96/F97。
