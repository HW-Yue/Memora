# Committed Change Log（Binlog）与未来同步

状态：产品定位已确认；F83 事件契约与 MSQL 仍待用户 Review，未获实现授权。

## 第一用途

Memora Binlog 的第一用途是给用户和 AI 提供全 Instance、严格按提交顺序排列的
变化时间线，让 Admin 可以展示：

- 哪个数据项被 insert、revise、split、merge、move、supersede 或 delete；
- Database、Table、Column 和约束怎样演化；
- 语义 Route 节点怎样创建、改名、拆分、合并或移动；
- Route membership 怎样挂入、迁移、失效或修复；
- Relation、配置和维护计划怎样变化；
- 一次原子 Mutation 同时影响了哪些对象。

主从复制、PITR 和跨设备同步以后可以消费同一逻辑变化流，但不是第一版格式和
Feature 顺序的主导目标。

## 与其他记录的边界

| 记录 | 回答的问题 | 生命周期 |
| --- | --- | --- |
| History | 某个语义 Row 为什么形成这个 revision | 永久业务历史 |
| Committed Change Log / Binlog | 每次已提交事务按顺序改变了哪些逻辑对象 | 可配置但默认长期可观察 |
| Security Audit | 谁以什么权限调用了什么，结果如何 | 独立安全保留策略 |
| Route Trace | AI 查询时看到和选择了哪些节点 | 可清理观察数据 |
| Redo/Undo | 物理恢复和 MVCC 是否需要重做/旧版本 | 仅按物理引擎需要引入 |

Binlog 不复制原始 prompt、隐藏推理或完整大正文。Row 详情和正文 diff 通过事件里的
稳定 RowID/revision 定位 History；Catalog、Route 和 Relation 使用稳定 revision
或事件内的有界字段 delta。

## 提交与一致性

Binlog 只包含已经提交的逻辑变化，并保留完整事务边界。回滚、未完成事务和 crash
tail 不得产生可见事件。

F81 后，Change Transaction Envelope 作为普通逻辑 Page Record，与 Row、Catalog、
Route、membership 和 Relation Page 变化进入同一个 WAL transaction：

```text
private physical/logical write set
  → WAL page redo + change envelope Page record
  → WAL COMMIT + fsync
  → publish B+ Tree committed view
  → publish Change Log cursor
```

Change Log 不作为第二个独立 durability 日志，因此首版不需要 Redo/Binlog 两阶段
提交或 Group Commit。未来若为复制拆出独立流文件，再重新 Review 一致性协议。

## F83 最小事件契约

每个已提交事务形成一个有序 envelope：

- format version、transaction ID、commit sequence、committed at；
- actor、source、reason 与可选 Source Receipt ID；
- Database scope 和事务级 checksum；
- 按确定顺序排列的 change entries。

每个 entry 至少包含：

- object kind：Database、Table、Column、Row、Relation、Route node、
  Route membership 或 Configuration；
- 稳定 Database/Table/object ID；
- operation；
- before/after revision 与 Schema version；
- Row History locator，或有界的 Catalog/Route/Relation 字段 delta；
- 与 split/merge/move/compensation 相关的关联对象 ID。

物理 Page、offset、Buffer Pool 内容、模型凭据、Query Workspace 和宿主原始上下文
不得进入事件。

## Admin 读取形态

候选 MSQL：

```sql
SHOW CHANGES AFTER COMMIT_SEQUENCE :cursor LIMIT :limit;
SHOW CHANGES IN DATABASE :database AFTER COMMIT_SEQUENCE :cursor LIMIT :limit;
SHOW CHANGE :transaction_id;
```

结果必须支持稳定 cursor、scope、truncated、事件版本和事务原子性。Admin 提供：

- Instance/Database/Table 的变化时间线；
- 单事务影响对象列表；
- 数据项 before/after revision 与字段 diff；
- Route Tree 节点和 membership 的前后结构对比；
- 从事件跳转到当前对象、History、Source Receipt 和实际 MSQL；
- 筛选 actor、operation、object kind、Database/Table 和 commit sequence。

## “索引变化”的边界

第一版可视化的“索引”指 AI 使用的语义 Route Tree 和 membership。Row Directory、
Page/B+ Tree 或 Buffer Pool 属于可重建物理加速状态，不进入逻辑 Binlog；未来
Studio 可用独立 Engine Diagnostics 展示其 rebuild、compaction 和健康事件。

## 未来同步复用

以后若增加复制或多设备同步，需要在不破坏本地 Admin 事件的前提下另行扩展：

- origin Instance/device ID 与 global transaction ID；
- 订阅位点、确认、保留窗口和幂等去重；
- 快照后追赶、断点续传、加密和授权；
- 并发修改、Schema 冲突、删除传播与防回环；
- PITR 的受信快照与日志保留策略。

同步端不得重新执行原始 MSQL 或 Agent 决策；是否采用 before/after image、字段
delta 或混合重放格式，在同步 Feature Review 时再决定。

## 尚待 Review

- Change Log 默认保留期限，以及用户是否允许关闭或清理；
- Row 正文 diff 只引用永久 History，还是额外保存有界摘要；
- Catalog/Route delta 的具体字段和单事务大小预算；
- 锁冲突、失败事务和维护 dry-run 是否进入独立诊断流；
- `SHOW CHANGES` 的最终 MSQL 语法和分页 cursor。

## 关联

- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
- [数据可视化与本地观察接口计划](../planning/visual-inspection-feature-plan.md)
- [History Store v1](../data/history-store-v1.md)
