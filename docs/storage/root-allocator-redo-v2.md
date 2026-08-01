# Root/Allocator Redo v2

状态：F97c4 已实现并验收，PASS；取代
[Root/Allocator Redo v1](./root-allocator-redo-v1.md) 的 generation 语义。

## 唯一变化

原 expected/next generation 字段改为 expected/next publication revision。Physical
generation 由 Tree Control Header 和 B+ Tree Page Header 保存，不随普通提交改变。

Root payload 仍为 56 bytes，Allocator header 仍为 48 bytes；version 改为 2：

```text
root:
magic "MROT" | version 2 | header_size 56 | reserved
expected_revision u64 | revision u64
expected_root_page_id u64 | root_page_id u64
expected_next_page_id u64

allocator:
magic "MALL" | version 2 | header_size 48 | reserved
expected_revision u64
expected_next_page_id u64 | next_page_id u64
retired_count u32 | reserved u32
retired_page_id[retired_count] u64
```

Revision 必须严格 `expected + 1`。Allocator 与 Root 必须使用相同 expected revision。
Retired Page 的排序、范围与同事务 free-image 规则不变。F152 在不修改本 WAL payload 的
前提下允许后续事务复用 durable free Page；同一 Mutation Plan 内重新利用刚退休的 Page
记为 recycled，不进入最终 retired 集合，详见 [Free Page Reuse v1](./free-page-reuse-v1.md)。

尚无已发布或业务接入的数据文件，因此 decoder 明确拒绝 v1，不做静默兼容。
