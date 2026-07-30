# RowID 取数基础 Feature 计划

状态：讨论稿，待用户 Review；F81/F82 未获执行授权。

## 用户结果

AI 通过语义 Route 得到 RowID 后，Memora 像正常关系数据库按主键取数：全程纯代码、
结果确定、速度不随全库 revision 数量线性退化。并发只覆盖本地个人数据库真正需要
的 snapshot 正确性，不复制 InnoDB 的大规模并发实现。

## 当前真实路径

```text
SELECT ... WHERE row_id = :id
→ Parser / Binder / Executor
→ exactRowID fast-path
→ DescribeTable：重读并组装全部 Database / Table / Column
→ Row Service.Get
→ Row Repository.Read
→ IDs(ObjectKindRow)：复制并排序所有物理 Row record ID
→ 匹配 row_id / row_id@revision，逐个 decode 选最新 revision
→ File.Get：record_id → offset Map → ReadAt + CRC
```

SQL 层已经避免扫描业务 Row；文件层也能 O(1) 找到指定物理 record。缺口位于中间：
Schema 解析会重组全部 Catalog，逻辑 RowID 到最新可见 revision 也没有目录。因此
当前主键点查同时受 Catalog 规模和全部 Row revision 数量影响。

## MySQL 参考边界

需要参考：

- SQL 解析、绑定、执行计划与存储访问职责分层；
- `row_id` 作为稳定主键，精确谓词走 point-get；
- Schema/类型校验、投影、稳定错误和事务可见性；
- delete、revision、commit、rollback 与 snapshot 的确定行为。

第一阶段不复制：

- InnoDB 的多级 Buffer Pool、复杂 latch 和后台线程；
- 锁等待队列、gap/next-key lock、范围锁、死锁检测和锁升级；
- doublewrite、change buffer、adaptive hash 与 Group Commit；首版用 WAL full-page
  image + checksum 处理 torn Page；
- 为尚未出现的范围查询规模预建复杂优化器。

## F81 Persistent B+ Tree RowID Read Path

实现通用持久化 B+ Tree，并建立至少四个逻辑 key space：

```text
catalog[database/table name, id] → current Schema revision locator
current[table_id, row_id]        → latest visible revision locator
version[row_id, sequence]        → immutable revision locator
table[table_id, row_id]          → ordered live/tombstone state
```

内存 Catalog cache 和 Buffer Pool 只加速树访问，不是权威目录。daemon 重开从
checkpoint/root 打开树并重放 WAL，不能扫描全部 Row Record 重建主索引。写事务
只有在 Redo COMMIT durable 后才能发布数据 revision 与索引变化。

目标读取路径：

```text
exact row_id plan
→ Catalog B+ Tree/cache
→ Row current/version B+ Tree
→ Buffer Pool
→ target Record ReadAt
→ CRC / decode / Schema validate / project
→ memora.result/v1
```

验收门：

- 16 KiB Page、checksum、page LSN、Redo WAL、checkpoint 与 recovery 通过；
- 单实例 Buffer Pool 的 pin/latch、young/old LRU、dirty/flush ordering 通过；
- B+ Tree 支持 search/insert/delete、顺序遍历、split/merge 和 root grow/shrink；
- Page checksum、root/manifest、close/reopen、损坏拒绝和 crash fault point 通过；
- Database/Table/Schema 解析查 Catalog 树或缓存，不重读完整 Catalog；
- current RowID Get 为 `O(log_B N)` Page 路径，不调用 `IDs(ObjectKindRow)`；
- exact revision 与 as-of 只扫描该 Row 的版本 key range；
- Table cursor 沿有序叶 Page 前进，不通过全对象 decode 去重；
- insert/update/delete/supersede、事务尾损坏和 CRC 错误有确定测试；
- benchmark 报告树高、Page 访问数、split/merge、冷/热点查、重启时间和内存。

F81 按 Page/WAL → Buffer Pool → B+ Tree → 真实 key space → COW generation 骨架
五个连续切片实施，详见
[ADR-0006](../decisions/0006-mysql-page-buffer-wal-cow.md)。

## F82 Local Minimal MVCC

在 F81 B+ Tree root/version key space 上增加最小 snapshot 可见性：

- 一个 daemon 串行发布写事务；
- autocommit 语句在开始时捕获 committed sequence；
- 显式事务在开始时固定 snapshot，并可读取自己的 staged writes；
- 写 Row、Table/Schema、Route 或 membership 前取得对应稳定 ID 的排他写锁；
- autocommit 持锁到语句终态，显式事务持锁到 commit/rollback；
- Mutation Plan 将全部 lock key 排序后执行非等待 try-lock；
- 锁冲突立即返回版本化的稳定 lock-conflict 错误，不建立等待队列或死锁环；
- reader 只读到 snapshot 已提交的完整 revision；
- expected revision 继续阻止 Agent 覆盖陈旧版本；
- rollback 丢弃 staged writes；History 仍是永久语义历史，不充当 MVCC Undo。

验收门：

- commit 前、commit 中途和 crash tail 都不能产生 partial read；
- 长 reader 在新写提交后仍看到自己的稳定 snapshot；
- 新 autocommit reader 看到最新 commit；
- 显式事务 read-own-writes，rollback 后其他 reader 永远看不到该写；
- 同一 Row 的陈旧 expected revision 返回稳定冲突；
- 同一对象的并发写只能有一个成功取得写锁，不同对象不互相误锁；
- 多对象 Mutation 不会留下部分锁或部分发布状态；
- race test 与故障注入通过。

## 后续升级触发器

B+ Tree、最小 Buffer Pool 与 WAL 不再等待 benchmark 才进入。后续数据只决定
Secondary Index、完整 Tablespace/Extent、Buffer Pool 分片、compaction 或 Advanced
MVCC/Undo/Redo 的实现顺序；升级不能改变 MSQL、RowID、Route locator 和 History。
