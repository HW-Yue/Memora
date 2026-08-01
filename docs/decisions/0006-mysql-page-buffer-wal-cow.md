# ADR-0006：MySQL 式 Page/Buffer Pool/WAL，COW 用于 generation

状态：Accepted，2026-07-31；F81–F108 及 F97a–F97d3 拆分项均已完成；
每项仍须独立 TDD、验收和合入。

## 总体选择

Memora 参考 MySQL/InnoDB 的成熟分层，但按本地单 writer、少量 reader 缩小实现：

```text
MSQL Executor
→ B+ Tree
→ single-instance Buffer Pool
→ fixed-size Page
→ Data/Index File

writer commit
→ Redo WAL durable
→ publish committed view
→ dirty Page asynchronous flush
```

普通 B+ Tree 更新使用 Buffer Pool 中的 Page + Redo WAL。COW 不作为同一 Page 的
第二套日常更新协议，只用于需要整代替换的 rebuild、compaction、snapshot 和
index generation/root 切换。

## Page v1

- Page 大小使用 Instance 已有默认值 16 KiB；4–64 KiB 的兼容字段继续保留，但
  一个 Instance 创建后不能无迁移改变；
- Page Header 至少包含 format、page ID/type、generation、page LSN、payload
  length、flags 和 checksum；
- B+ Tree internal/leaf、free/manifest 和必要的 overflow 使用明确 Page type；
- Page 通过 `space_id + page_id` 定位，业务身份永远使用稳定逻辑 ID；
- checksum 错误不能静默返回旧值或空结果。

## Buffer Pool v1

- 第一版只有一个有硬内存上限的 Buffer Pool instance；
- Page Table 使用 `space_id + page_id` 定位 Frame；
- Frame 具有 pin count、读写 latch、dirty、page LSN 和访问状态；
- 淘汰参考 InnoDB young/old LRU 与 midpoint insertion，保护热点不被扫描冲掉；
- clean Page 可直接淘汰；dirty Page 只有在对应 WAL 已持久化后才能刷回；
- Query Workspace、Route Frame、模型结果和语义优先级不得进入 Buffer Pool。

多 Buffer Pool instance、复杂自适应刷脏、独立 Page Cleaner 线程池和细粒度 latch
优化全部后置。

## Redo WAL v1

- LSN 是 WAL 字节顺序，不能与 commit sequence、revision 或 Binlog cursor 混用；
- mutation 先形成私有物理 write set，不向 reader 发布未提交 B+ Tree Page；
- WAL 保存可校验、幂等的 Page init/delta/root/allocator 记录和事务 COMMIT；
- COMMIT record fsync 后事务才可确认成功并发布 committed view；
- dirty Page 刷盘前必须满足 `durable_wal_lsn >= page_lsn`；
- checkpoint 记录 recovery 起点，只有覆盖范围内 Page 已安全持久化后才回收旧 WAL；
- WAL 是 Instance 级分段日志，位于 datadir 的 `redo/`，不进入 Cache、逻辑
  Database snapshot、Wiki 或 Database package；
- 每个 Page 在 checkpoint 后首次修改记录 full-page image，配合 checksum 恢复
  torn Page；首版不增加 doublewrite；
- recovery 从 checkpoint 重放完整已提交事务，忽略半条、校验失败和未提交尾部。

第一版不做 Group Commit；单 writer 下先保证顺序、故障注入和可解释恢复。

F109 Change Log envelope 作为逻辑 Page Record 与业务变化进入同一 WAL transaction；
它不是第二份独立 durability 日志，因此首版不需要 Redo/Binlog 两阶段提交。

## MVCC 与 Undo

Row 继续使用 immutable revision 和 commit sequence。普通 reader 根据 snapshot
从 version B+ Tree 选择可见 revision；写锁保护明确逻辑对象。

第一版共享 Page 只应用已经 WAL-committed 的私有 write set，因此 rollback 可以
丢弃 staged writes，不要求先实现通用物理 Undo chain。若以后允许 uncommitted
dirty Page steal、in-place Row body 或多 writer，再单独引入 Undo/Purge。

## COW 的限定用途

- 在线构建新的 Secondary Index 或 Router physical generation；
- compaction 后形成新 Data/Index generation；
- snapshot/backup 所需的稳定 root 集合；
- 校验完成后通过 WAL 保护的 manifest/root swap 原子发布；
- 旧 generation 等最老 reader 释放后回收。

COW generation 失败不影响当前 root；它不代替普通事务 Redo，也不产生第二套
RowID、History、Route 或 Change Log 语义。

Tree 的逐提交 root publication revision 与 physical generation 必须分离。普通 Redo
提交只增加 revision；Page Header generation 和当前 physical generation 保持不变。

## 实施 Feature

- F81–F86c：Page、WAL stream/commit、recovery、Segment Set、checkpoint 与回收；
- F87–F89：Buffer Pool loading/eviction 与 dirty flush；
- F90–F97：B+ Tree codec、read、mutation 与持久 root；
- F98–F102：Catalog/Row/version/Table cursor 与 MSQL point-get；
- F103–F104：snapshot visibility 与精确对象写锁；
- F105–F108：旧 Store 迁移、default switch 与 COW generation。

每项独立 TDD、验收和合入；后一项不能替前一项补完成证据。

## 明确后置

gap/next-key lock、锁等待/死锁检测、多 writer、Buffer Pool 分片、doublewrite、
change buffer、adaptive hash、Group Commit、完整 per-table Tablespace/Extent 和
高级后台 I/O 调度。

## 关联

- [ADR-0005：B+ Tree 必做](./0005-btree-mandatory-primary-index.md)
- [Buffer Pool](../storage/buffer-pool.md)
- [MVCC、Undo、Redo 与 Binlog](../storage/mvcc-undo-redo.md)
