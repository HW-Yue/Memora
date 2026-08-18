# F226：Database 级故障隔离

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
**这是可用性缺陷，不是优化**：一个 Database 出问题会让整个 Instance 读写全停。

## 现象

改一个 Database 的字段出错后，其余 Database 既写不了也读不了。

## 两个独立成因

### 成因 1：`poisoned` 是全实例单一布尔，且读路径也查它

`internal/pagestoremigration/authority.go`：

- `poisonPublication`（`:541`）只做一件事：`authority.poisoned = true`。
  没有对象、没有 Database、没有任何作用域；
- `healthyLocked`（`:568`）见到 `poisoned` 就返回 `ErrAuthorityPoisoned`；
- `BeginWrite`（`:199`）查它 → 所有 Database 写入失败；
- **`lockRead`（`:554`）也查它** → 所有 Database 读取同样失败。

读被一起拖死是最没必要的一环：发布失败后，已提交的旧 generation 仍然完好，
读它是安全的。把读一并阻断，等于把「一次写发布出了问题」升级成「整个实例不可用」。

### 成因 2：所有 Database 共用一套物理文件

实测真实实例布局：

```text
databases/
├── database.memora          ← 单个文件，所有 Database 共用
├── page-authority-v1.json
├── page-index-v1/           ← 单套 Page/WAL，所有 Database 共用
│   ├── catalog.pages  current.pages  versions.pages  fulltext.*
│   └── catalog.wal/  current.wal/  versions.wal/  fulltext.wal/
└── change-index-v1/         ← 单套 change log
```

没有按 Database 分目录。物理故障域因此等于整个 Instance：单个 Page 损坏、
单个 WAL torn tail、单次 generation 替换失败，影响面都是全部 Database。

**这与设计文档冲突。** [原生 Store](../storage/native-minimal-store.md) 第 21 行写的是
`databases/db_<stable-id>/database.memora`——每 Database 一个文件。实现漂移了，
文档没跟上。本 Feature 同时修正这个漂移。

## 分两阶段，先拿回可用性

### Stage 1：收敛 poison 作用域（小改动，解决绝大部分痛）

即使文件暂不拆，也必须做到：

1. **读不再因写发布失败而失效。** `lockRead` 不再检查 `poisoned`；
   已提交 generation 的读取始终可用。只有 `closed` 仍然阻断读；
2. **poison 按 Database 收敛。** `poisoned` 从 `bool` 改为受影响 Database ID 的集合；
   `BeginWrite`／`BeginRowWrite` 只拒绝命中集合的 Database；
3. **失败信封点明范围**：哪个 Database、哪个对象、其余 Database 不受影响；
4. **恢复路径不变**：现有 `doctor repair` 与 reopen 收敛逻辑继续适用，
   只是作用域从全实例变为单 Database。

Stage 1 之后，「改一个库出错导致全实例停摆」不再成立。

### Stage 2：物理文件按 Database 拆分

`databases/db_<stable-id>/` 各自持有 `database.memora`、`page-index-v1/` 与
其 WAL；`change-index-v1` 与 system 文件的归属单独冻结（见下）。

## Stage 2 必须先解决的三件事

不解决就不要动手，否则会用一个更难的问题换掉现在的问题。

1. **事务作用域。** `executor.TransactionFactory`（`internal/msql/executor/batch.go:39`）
   签名是 `func(context.Context) (ExplicitTransaction, error)`——**没有 database 参数**，
   事务是实例级的。拆文件后跨 Database 的多 statement 事务要么引入 2PC，
   要么显式降级为「一个事务只能覆盖一个 Database」。
   **建议后者**：Skill 的写入本来就按 Database 授权，跨库原子性没有产品故事，
   为它引入 2PC 不划算。这需要在 MSQL 契约里显式冻结并给出清晰错误。
2. **Change log 的全局序。** `change-index-v1` 现在是单一全局 commit sequence。
   按库拆分后要么每库独立序（失去跨库时间线），要么保留一个共享序号文件
   （重新引入共享失败点，但面积远小于整套 Page/WAL）。必须显式选一个并写进规格。
3. **Lexical/fulltext 的范围。** `page-index-v1/fulltext.*` 目前是全实例一套。
   按库拆分后跨库 lexical 查询变成 fan-out，`SHOW LEXICAL LOCATIONS` 的预算与
   cursor 语义需要重新定义。

## 明确不做

- 不在 Stage 1 引入任何文件布局变更；
- 不为跨 Database 原子性引入 2PC（除非产品出现真实需求）；
- 不追溯拆分存量实例的文件——Stage 2 走既有 `instanceupgrade` 的
  `--plan` / `--apply` 显式高风险流程，不静默迁移；
- 不把 poison 收敛当作放宽 fail-closed：命中的 Database 仍然硬失败，
  只是不再连带其他 Database。

## RED 与完成门

**Stage 1**

- RED 先证明：让一个 Database 的发布失败后，另一个 Database 的 `SELECT` 与
  `INSERT` 当前都返回 `ErrAuthorityPoisoned`；
- 注入单 Database 发布失败后：该 Database 写入失败、**其余 Database 读写正常**；
- 已 poison 的 Database 自身的读取仍然可用（读旧 generation）；
- `closed` 仍然阻断全部读写；
- 失败信封含受影响 Database 与对象；
- 多个 Database 分别 poison 时集合正确累积，`doctor repair` 后逐个清除；
- reopen 后 poison 集合按持久状态重建，不误判健康。

**Stage 2**

- 上述三件事各自有冻结结论并落进规格；
- 新建实例按 `databases/db_<id>/` 布局，reopen、备份、恢复、搬迁、
  Database Package 安装/fork/merge 全部通过；
- 单 Database 目录整体损坏时，其余 Database 可正常打开与读写；
- 存量实例经 `upgrade --plan` 展示计划、`--apply` 迁移后逐字节校验一致；
- `native-minimal-store.md` 的布局描述与实现一致（修正当前漂移）；
- 目标测试、`-race`、fault injection 与完整 CI 全绿。

## 关联

- [执行计划](./execution-plan.md)
- [原生 Store](../storage/native-minimal-store.md) — 布局描述当前与实现不一致
- [Page Store Authority](../storage/page-store-authority-v1.md)
- [已知风险](../development/known-risks.md)
