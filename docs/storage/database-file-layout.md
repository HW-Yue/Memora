# Database 物理目录

状态：B+ Tree 是必做主索引；本文的完整 Tablespace/History/独立 generation
目录仍是后置候选。F81–F97 先在当前 Database 物理边界内实现 Page/root。

## 稳定目录身份

Instance 的 `databases/` 下使用不可变 `database_id`，不使用可读名称：

```text
databases/
└── db_<stable-id>/
```

Data Dictionary 保存 `database_id ↔ current_name`。rename、同名库、包安装和设备同步都不能触发目录改名。

## Database 顶层

```text
db_<stable-id>/
├── database.meta
├── data/                 当前权威 Row 和普通物理索引
├── history/              长期语义 revision
└── indexes/              可重建派生索引
```

`data/` 和 `history/` 不得被 `REBUILD LEXICAL INDEX` 或缓存清理删除。`indexes/` 可以按 generation 丢弃，但必须能从当前 Row、Schema、关系和索引规则重建。

## 每表 Tablespace

`data/` 内每个 User Table 使用不可变 `table_id`：

```text
data/
├── table_<stable-id>/
│   ├── space_000001.mdata
│   └── space_000002.mdata
└── table_<stable-id>/
    └── space_000001.mdata
```

一个 Table Tablespace 保存当前 Row、聚簇索引和普通二级 B+ Tree。小表通常只有一个 Data File，达到策略后可以滚动增加；Table rename 不改变目录。

## Database 级 History

```text
history/
├── history_000001.mhist
├── history_000002.mhist
└── history.index
```

History record 携带原始 `table_id`、稳定 `row_id`、revision 和 commit sequence。MOVE、RETYPE、SPLIT、MERGE 或 Table rename 不迁移旧历史。History Store 是权威数据，不使用 Undo Purge 生命周期。

## 独立索引 generation

Router、Agent 倒排和机械倒排的重建成本与触发条件不同，各自维护 generation：

```text
indexes/
├── manifest
├── router/
│   ├── gen_000007/
│   └── gen_000008/
└── inverted/
    ├── agent/
    │   └── gen_000012/
    └── mechanical/
        └── gen_000021/
```

`manifest` 原子记录当前启用的 Router、Agent inverted、mechanical inverted generation 以及各自覆盖到的 commit sequence。查询开始时读取一次 manifest 并固定该组合，不能在一次查询中途混用新旧 generation。

重建先写新目录并完成校验与持久化，再通过原子替换 manifest 发布。旧 generation 等现有读者释放后回收。重建一种索引不复制其他索引，也不能触碰 `data/` 或 `history/`。

## 尚未确认

- `database_id`、`table_id` 和 generation ID 编码；
- `database.meta` 与 manifest 的最终原生二进制编码和双写保护；
- Data File、History segment 和 posting 文件的扩展名；
- Tablespace/History 的滚动、预分配、压缩和回收参数；

## 关联

- [macOS Instance 数据目录](./macos-instance-directory.md)
- [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)
- [物理与检索索引](./indexing.md)
- [Agent 语义目录索引](../query/semantic-routing.md)
