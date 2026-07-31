# Tree Control v1

状态：F97c1 已实现并验收；generation 语义已被
[Tree Control v2](./tree-control-v2.md) 取代。

## 唯一用途

B+ Tree Tablespace 的 physical slot 1 保存一个 Page-codec 保护的 control Page，作为
committed root 与 allocator high-water 的恢复目标。slot 0 仍是 F82 space manifest，
Tree data Page 从 ID 2 开始。

通用 Page Manager 不强制每个 space 都是 Tree space；只有使用本协议的 space 才保留
Page 1。

## Control Page

Page Header：

- type = `tree-control`，space/page identity = `<space_id>/1`；
- generation 是每次 Tree commit 严格加一的 committed generation；
- Page LSN 是发布该 generation 的 root redo LSN；
- flags 为零。

Payload 固定 32 bytes：

```text
magic[8] = "MEMTRC01"
version u16 = 1
payload_size u16 = 32
reserved u32 = 0
root_page_id u64
next_page_id u64
```

committed 状态必须满足 generation/LSN 非零、`2 <= root_page_id < next_page_id`。
此外只允许一个无 root 的 bootstrap control：
`generation=0, root=0, next_page_id=2, lsn=0`。

## 明确不做

F97c1 不解释 WAL redo、不执行 B+ Tree mutation plan、不定义运行时 commit API，
也不接 Catalog/Row key space。Root/Allocator recovery 属于 F97c2。
