# Root/Allocator Redo v1

状态：F97c2 已实现并验收；generation 语义已被
[Root/Allocator Redo v2](./root-allocator-redo-v2.md) 取代。

## 唯一结果

Tree transaction 的 `root`/`allocator` WAL payload 可确定编解码，并携带恢复所需的
generation、root、allocator high-water 与 retired Page 前置状态。

## Redo Payload

`allocator` redo 保存 expected generation、expected/next Page ID 和有序 retired ID；
`root` redo 保存 expected/next generation、expected/next root 与 expected allocator
high-water。

Root payload 固定 56 bytes：

```text
magic[4] = "MROT" | version u16 = 1 | header_size u16 = 56
reserved[8] = 0
expected_generation u64 | generation u64
expected_root_page_id u64 | root_page_id u64
expected_next_page_id u64
```

Allocator payload 为 48-byte header 加 `retired_count * 8`：

```text
magic[4] = "MALL" | version u16 = 1 | header_size u16 = 48
reserved[8] = 0
expected_generation u64
expected_next_page_id u64 | next_page_id u64
retired_count u32 | reserved u32 = 0
retired_page_id[retired_count] u64
```

Root generation 必须严格 `expected + 1`。Retired ID 严格递增、位于
`[2, expected_next_page_id)`，不在本 Feature 中复用。

- 每个 Tree transaction 必须恰有一个最后出现的 root redo；
- allocator redo 可选，但若存在必须位于 root 前且 generation 与 root 一致；
- `[expected_next, next)` 每个 Page 都必须有更早的 `page-init`；
- 每个 retired Page 必须在同一事务被写成 `free` Page，不立即复用；
- root 必须小于 final allocator high-water，且不能属于 retired；
- validation 失败时事务零 Page 写入。

## 明确不做

F97c2 不读取或写入 Page、不执行 recovery、不生成 B+ Tree mutation，也不定义在线
commit API。Committed recovery 属于 F97c3。
