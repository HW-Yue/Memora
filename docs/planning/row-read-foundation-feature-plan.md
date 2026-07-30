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

- InnoDB Page、聚簇 B+ Tree 和多级 Buffer Pool；
- 多 writer 锁调度、gap/next-key lock、死锁检测和锁升级；
- doublewrite、change buffer、adaptive hash 与 Group Commit；
- 为尚未出现的范围查询规模预建复杂优化器。

## F81 Fast RowID Read Path

新增 Store 内部可替换的 Catalog/Row Directory，daemon 打开文件时从完整已提交
事务重建：

```text
database[name/alias]             → database metadata
table[database, name/alias]      → table + current Schema metadata
current[row_id]                  → latest committed revision meta
revision[row_id, revision]       → record meta
visible[row_id, commit_sequence] → 可见 revision 定位
table[table_id]                  → 稳定顺序的 live row_id
```

写事务只有在 durable COMMIT 后才能原子发布目录变化。未完成事务、损坏事务和回滚
不得进入目录；删除与 supersede 保留版本定位，但不进入当前 live 集合。

目标读取路径：

```text
exact row_id plan
→ Catalog Directory lookup
→ Row Directory lookup
→ one target ReadAt
→ CRC / decode / Schema validate / project
→ memora.result/v1
```

验收门：

- Database/Table/Schema 解析直接查 Catalog Directory，不重读完整 Catalog；
- current RowID Get 平均 O(1)，不调用 `IDs(ObjectKindRow)`；
- exact revision 直接定位，as-of commit 只遍历该 Row 的版本，不扫其他 Row；
- close/reopen 重建结果与写后内存状态完全一致；
- Table cursor 不通过全对象 decode 去重；
- insert/update/delete/supersede、事务尾损坏和 CRC 错误有确定测试；
- benchmark 分别报告 Row 数、每 Row revision 数、冷启动时间、点查延迟和内存。

## F82 Local Minimal MVCC

在 F81 目录上增加最小 snapshot 可见性：

- 一个 daemon 串行发布写事务；
- autocommit 语句在开始时捕获 committed sequence；
- 显式事务在开始时固定 snapshot，并可读取自己的 staged writes；
- reader 只读到 snapshot 已提交的完整 revision；
- expected revision 继续阻止 Agent 覆盖陈旧版本；
- rollback 丢弃 staged writes；History 仍是永久语义历史，不充当 MVCC Undo。

验收门：

- commit 前、commit 中途和 crash tail 都不能产生 partial read；
- 长 reader 在新写提交后仍看到自己的稳定 snapshot；
- 新 autocommit reader 看到最新 commit；
- 显式事务 read-own-writes，rollback 后其他 reader 永远看不到该写；
- 同一 Row 的陈旧 expected revision 返回稳定冲突；
- race test 与故障注入通过。

## 后续升级触发器

只有 benchmark 证明内存、启动扫描、范围查询或并发写成为瓶颈，才分别 Review
checkpoint/compaction、Page/B+ Tree/Buffer Pool 或 Advanced MVCC/Undo/Redo。
升级可以替换物理实现，不能改变 MSQL、RowID、Route locator 和 History 语义。
