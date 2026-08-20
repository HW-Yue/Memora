# F226：Database 级故障隔离

状态：**Stage 1 已实现（2026-08-11）**；**Stage 2 已评估并延后（2026-08-20）**，
不在当前路线。延后不是漏做：交付物是下面的评估、判据和替代方案。
这是可用性缺陷，不是优化：Stage 1 之前，一个 Database 出问题会让整个 Instance
读写全停。

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

### 成因 2（已评估为有意设计，不是缺陷）：所有 Database 共用一套物理文件

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

发现之初这被记为「实现漂移」——[原生 Store](../storage/native-minimal-store.md)
当时写的是 `databases/db_<stable-id>/database.memora`，每 Database 一个文件。
**2026-08-20 评估后结论相反：单套文件是正确取舍，漂的是那份文档。** 该文档已改为
描述实测布局并写明这是有意选择；本节保留原始记录以便追溯判断过程，
评估依据见下面的 Stage 2。

## 分两阶段，先拿回可用性

### Stage 1：收敛 poison 作用域（已实现）

即使文件暂不拆，也必须做到：

1. **读不再因写发布失败而失效。** `lockRead` 不再检查 `poisoned`；
   已提交 generation 的读取始终可用。只有 `closed` 仍然阻断读；
2. **poison 按 Database 收敛。** `poisoned` 从 `bool` 改为受影响 Database ID 的集合；
   `BeginWrite`／`BeginRowWrite` 只拒绝命中集合的 Database；
3. **失败信封点明范围**：哪个 Database、哪个对象、其余 Database 不受影响；
4. **恢复路径不变**：现有 `doctor repair` 与 reopen 收敛逻辑继续适用，
   只是作用域从全实例变为单 Database。

Stage 1 之后，「改一个库出错导致全实例停摆」不再成立。

**实现说明（2026-08-11）**：`poisoned bool` 拆为 `poisonedAll bool` 与
`poisonedDatabases map[string]struct{}`；`healthyLocked` 拆为 `openLocked`（读，
只看 `closed`）与 `writableLocked(ctx, databaseIDs...)`（写）。Row/Route 发布按
`mutationDatabaseIDs` 收敛；Catalog 发布按 `changedDatabaseIDs` 收敛（无法判定
差异时 fail closed 到 Instance 级）；generation 替换保持 Instance 级，因为它本来
就重写所有 Database 的索引。`BeginRowWrite` 增加早失败检查，不再等到发布阶段。
poison 仍只在内存中，reopen 后由既有 reconciliation 收敛，与改动前一致。

### Stage 2：物理文件按 Database 拆分（已评估并延后）

设想是 `databases/db_<stable-id>/` 各自持有 Page/WAL。**2026-08-20 评估后决定不做。**

#### 为什么不做

1. **最热的读路径本来就跨 Database。** `SHOW LEXICAL LOCATIONS FROM ALL TABLES`
   经 `visibleLexicalDatabaseIDs`（`internal/msql/executor/lexical_locations.go:52`）
   把所有可见 Database 作为一次查询打进同一个倒排索引；Catalog Atlas 列举全部
   Database 供跨库导航；Bootstrap Frame 是两者之和，**每次查询的第一步就跨库**。
   拆分会把它变成「N 个库各查一次再归并」，而 `LIMIT`／`BYTES`／`cursor` 的预算
   语义全部建立在单一索引上，需要整套重做。这是给最高频路径加永久成本。
2. **Stage 1 已经解决了真实发生过的痛。** 用户报告的「改一个库出错、其他库读写
   全停」源于 poison 单布尔且读路径也查它，与文件布局无关，Stage 1 已修复。
   拆分在此之上只多买到**介质级隔离**。
3. **介质级隔离的价值低于直觉。** 主导的损坏模式是共享引擎代码的缺陷——Page 编码、
   B+ Tree、treecommit、recovery 全是同一套代码，`treecommit` 出 bug 会损坏它碰到的
   那棵树，与树在哪个文件无关。**拆文件对这一类零作用。** 真正能被隔离的只有坏扇区
   与单文件 torn tail：前者通常同盘一起失效，后者已有 WAL + CRC + checkpoint +
   torn-tail 恢复覆盖。
4. **规模不支持。** 实测 570 行约 556 KB。20 个 Database 各 5,000 行也只有约 50 MB，
   全量 COW `REBUILD LEXICAL INDEX` 在这个量级是秒级的；「一个库的增长拖累全体索引
   重建」在个人尺度上不成立。
5. **代价是三项必须重新冻结的语义外加一次有真实数据风险的迁移**：事务作用域
   （`executor.TransactionFactory` 无 database 参数，事务是实例级的）、change log
   的单一全局 commit sequence、lexical/fulltext 的 fan-out 契约。

#### 改为采取的替代方案

- 逻辑层故障隔离：Stage 1（已完成）；
- 恢复粒度按 Database：需要时让 `REBUILD LEXICAL INDEX` 支持只重建一个 Database，
  拿到「单库可独立恢复」的实际好处而不动文件布局；
- 备份／恢复／搬迁按 Database：走既有的逻辑 snapshot 与 Database Package，
  不依赖文件边界；
- 文档与实现对齐：[原生 Store](../storage/native-minimal-store.md) 的布局描述已改为
  实测布局，并写明单套文件是有意选择。

#### 重新评估的触发条件

出现下列任一情况时重开此项，不必等其余：

1. 多设备同步（F161）进入路线——按 Database 的同步单元天然要求物理分离；
2. 出现一次可追溯到跨 Database 文件耦合的真实损坏事故；
3. 单个 Database 增长到 GB 级，使索引重建成为运维负担；
4. Database Package 需要成为「安装／卸载不触碰其他 Database 文件」的一等公民。

## 明确不做

- 不拆分物理文件（见上；触发条件满足前不重开）；
- 不为跨 Database 原子性引入 2PC；
- 不把「文档写着每库一个文件」当作实现待办——文档已改为跟随实现；
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

已评估并延后，无完成门。重新评估时以上述四个触发条件为入口，届时需重新冻结
事务作用域、change log 全局序与 lexical fan-out 三项语义。

## 关联

- [执行计划](./execution-plan.md)
- [原生 Store](../storage/native-minimal-store.md) — 布局描述当前与实现不一致
- [Page Store Authority](../storage/page-store-authority-v1.md)
- [已知风险](../development/known-risks.md)
