# B+ Tree Node Codec v1

状态：F90 已完成，2026-07-31；冻结 internal/leaf Page payload 的确定性字节格式。

## Page 与 Node

Node 只编码在 F81 `page.Page.Payload` 内，外层 Page Header 继续提供 identity、generation、
Page LSN 和 CRC32C：

- `TypeBTreeLeaf` 对应 level 0 leaf Node；
- `TypeBTreeInternal` 对应 level > 0 internal Node；
- leaf 保存有序 `key → value` 和可为 0 的 `next_leaf_page_id`；
- internal 保存 `leftmost_child_page_id`，随后每项是有序 `separator key → right child`；
- key、leaf value 与所有 child Page ID 都必须非空/非零；key 严格递增且不得重复。

F90 不解释业务 key/value，也不执行 search、split 或 mutation。

## Payload v1

Payload 固定占满 Page 可用的 16320 bytes，未用区必须为零：

```text
0..47    Node Header
48..     8-byte Slot Directory，向高地址增长
free     全零 Free Space
...      variable Records，向低地址增长
```

Node Header（little-endian）：

| Offset | Size | Field |
| --- | ---: | --- |
| 0 | 8 | magic `MEMBTN01` |
| 8 | 2 | version = 1 |
| 10 | 2 | kind：leaf=1/internal=2 |
| 12 | 2 | level |
| 14 | 2 | flags = 0 |
| 16 | 4 | entry count |
| 20 | 2 | slot size = 8 |
| 22 | 2 | header size = 48 |
| 24 | 8 | internal leftmost child；leaf 为 0 |
| 32 | 8 | leaf next Page；internal 为 0 |
| 40 | 2 | free start |
| 42 | 2 | free end |
| 44 | 4 | reserved = 0 |

每个 Slot 为 `record_offset u16 + record_length u16 + key_length u16 + reserved u16`。
leaf Record 是 `key || value`；internal Record 是 `key || right_child_page_id u64`。
Slot 顺序就是 key 顺序，Record 从 Payload 尾部反向紧密排列。

Decode 拒绝错 magic/version/kind、Page type 不匹配、非法 level/child、乱序/重复 key、
重叠或越界 Slot、非紧密 Record、非零 free/reserved bytes 和无法重新确定性编码的值。

## 明确不做

- point search、range cursor、insert/delete、split/merge；
- prefix compression、overflow key/value、变长 Slot、Page occupancy 策略；
- root/allocator/leaf prev pointer 或业务 locator 编码。
