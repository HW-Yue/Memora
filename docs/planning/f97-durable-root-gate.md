# F97 Durable Root 拆分 Review

状态：拆分已获用户批准；F97a、F97b1、F97b2、F97c1 已完成；F97c2
Root/Allocator Redo 已 Review、批准并开工。

## 产品门

- 目标故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 用户结果：一次 mutation 只有在 WAL COMMIT durable 后才成功；成功提交后无论何时
  crash/reopen，exact lookup 都从同一已提交 root 得到结果；“失败事务永不发布”经
  F97b Review 修正为候选边界：commit decision 前失败确定不发布，decision I/O 错误
  返回 outcome unknown 并由 recovery 判定，待用户确认；
- 物理作用域：B+ Tree Page、root descriptor、Page allocator、Buffer Pool 与 Redo WAL；
- 语义作用域：不改变 Row revision、History、Route membership、Schema 或结果 envelope；
- 上下文预算：不增加 AI 可见节点、RowID、正文、模型调用或 prompt 内容；
- 永久边界：Agent 仍只发 MSQL，不接触 Page/root/allocator；无 Vector、SQLite fallback、
  全库扫描或第二套业务 authority；
- 依赖：F81–F96 已完成；F98 业务 Catalog key space 继续等待本组全部完成。

标准旅程沿用现有 MSQL，不新增物理语法：

```sql
BEGIN;
UPDATE work.notes SET title = :title WHERE row_id = :row_id;
COMMIT;
-- 在 COMMIT durable 后、Page flush 前终止 daemon 并重开
SELECT * FROM work.notes WHERE row_id = :row_id LIMIT 1;
```

F97 的验收只证明这段旅程所依赖的物理索引可持久提交；业务 Executor 切换属于 F98–F102。

## 现状证据与拆分理由

- `btree` 只有 node 级纯函数；没有从 root descent 后递归传播 split/rebalance 的事务计划；
- `wal` 已接受 `root`/`allocator` change type，但 recovery 对两者返回
  `ErrUnsupportedRedo`；
- `OpenSegmentSet` 遇到未提交 active tail 返回 `ErrPoisoned`，尚无 crash 后截到最后
  已验证 commit 并恢复可写状态的入口；
- Buffer Pool 只能修改已 resident Page，没有多 Page 私有 write set、新 Page 安装或
  root-last 原子发布接口；
- Page Manager 只按文件长度给出连续 high-water，不保存已提交 tree root。

把这些一次实现会同时引入树算法、WAL 修复协议、持久 metadata 格式和并发发布，任一
故障都无法由单一 RED 定位，因此不满足 Feature 大小门。

## 候选拆分与顺序

| Feature | 唯一主要结果 | 明确不做 |
| --- | --- | --- |
| F97a Tree Mutation Plan | 任意深度 byte-key mutation 生成私有 Page after-image、new root、allocated/retired ID | WAL、Buffer Pool、文件 I/O |
| F97b WAL Recovery Open | Review 发现必须先建立 durable frontier；建议拆为 F97b1/F97b2 | root/allocator、B+ Tree |
| F97c1 Tree Control Codec | slot 1 versioned control Page 可确定编解码 | WAL、recovery、tree mutation |
| F97c2 Root/Allocator Redo | root/allocator metadata 可随 committed WAL 幂等恢复 | tree mutation、业务 key |
| F97d Durable Tree Commit | 私有计划按 WAL durable → committed Page → root-last 发布并可 reopen | Catalog/Row key 编码、MVCC |

F97a 详细候选契约见
[B+ Tree Mutation Plan v1](../storage/btree-mutation-plan-v1.md)，独立开工门见
[F97a B+ Tree Mutation Plan](./f97a-btree-mutation-plan-gate.md)。
F97b Review 见 [WAL Recovery Open 拆分 Review](./f97b-wal-recovery-open-review.md)。

F97c1 冻结格式保留 F82 的 slot 0 space manifest 不变；slot 1 使用独立、versioned tree
control Page，保存 committed root、generation 与连续 allocator high-water。新 Tree root
从 slot 2 开始。F97 首版不复用 retired Page：删除/merge 在同一事务把它变为 `free`
Page，但空间复用或 generation compaction 留给后续独立 Feature，避免同时引入 free-list
恢复协议。控制格式见 [Tree Control v1](../storage/tree-control-v1.md)，redo 协议见
[Root/Allocator Redo v1](../storage/root-allocator-redo-v1.md)。

## RED 与完成证据候选

### F97a

- 多层 insert/update/delete、递归 split/rebalance、root grow/shrink 目前没有 API；
- 固定 seed 随机状态序列逐步对拍排序 map，并检查可达 Page、separator、leaf link、
  无重复/丢失 key、allocated/retired 集合与输入深复制；
- no-space/corruption/reader fault 原子失败，零共享状态修改。

### F97b

- subprocess 在 change header/payload、commit、fsync 前后终止；
- 只允许丢弃最后 durable commit 之后的 active tail；已提交区域的 CRC、LSN、digest
  损坏必须拒绝，不能截掉证据继续；
- repair truncate、file sync、必要的 directory/roll 顺序逐点 fault injection，重复打开收敛。

### F97c1/F97c2

- 当前 committed `root`/`allocator` recovery 稳定返回 `ErrUnsupportedRedo`，作为首个 RED；
- bootstrap、root grow/shrink、allocator advance、retired Page、reopen、checkpoint 后恢复、
  重复恢复和前置状态不匹配；
- unknown version、bad identity/generation/high-water、乱序或缺 Page init 整个事务零写入。

### F97d

- WAL commit 未返回成功时当前进程 root/Page/allocator 零发布；decision I/O 错误返回
  outcome unknown；commit durable 后任何 publish/flush fault 返回 recovery-required 并
  poison 当前 Tree，重开后得到已提交结果；
- root-last 事件日志、reader/writer 可控调度、Buffer Pool 硬容量与 `-race`；
- reference model 随机 commit/crash/reopen，每一步校验树和 root；
- targeted `-count=20`、`go test ./...`、全量/受影响包 race、vet、format 与 CI。

## 开工决定

- 原 F97：REVISE，不直接编码；
- F97a–F97d 拆分：已批准；
- F97a：完成，PASS；
- F97b：REVISE 后拆为 F97b1 Durable Frontier 与 F97b2 Repairing Open，均已完成；
- F97c：规模 Review 后拆为 F97c1 codec 与 F97c2 redo；
- F97c1：完成，PASS；
- F97c2：PASS，已由持续执行指令授权并开工；
- F97d：等待 F97c2 完成后独立 Review。
