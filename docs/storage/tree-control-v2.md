# Tree Control v2

状态：F97c4 已实现并验收，PASS；取代
[Tree Control v1](./tree-control-v1.md) 的 generation 语义。

## 唯一结果

Tree Control 分离物理 generation 与逐提交 revision，使普通路径内更新无需重写整棵树。

## 字段语义

- Page Header `generation` 是 physical generation；普通 Tree commit 保持不变；
- payload `revision` 是 root publication revision，每次 commit 严格加一；
- Page Header LSN 是发布该 revision 的 root redo LSN；
- root 与 allocator high-water 含义不变。

Payload 固定 40 bytes：

```text
magic[8] = "MEMTRC02"
version u16 = 2
payload_size u16 = 40
reserved u32 = 0
revision u64
root_page_id u64
next_page_id u64
```

Committed 状态要求 physical generation、revision、LSN 非零，且
`2 <= root_page_id < next_page_id`。bootstrap 固定为 physical generation 1、
revision 0、root 0、next Page ID 2、LSN 0。

Root/Allocator redo v2 保持原 payload 尺寸，但版本变为 2，并把原
expected/next generation 字段明确改为 expected/next revision。Recovery 比较 revision，
同时验证 committed root Page 属于 control 的 physical generation。

## 兼容与边界

尚无已发布或业务接入的数据文件，因此实现只接受 v2，不保留 v1 静默兼容。F97c4
不实现 durable commit、Buffer Pool batch publish、COW generation swap 或业务 key。
