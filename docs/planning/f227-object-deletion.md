# F227：Table / Database 归档（容器级逻辑删除）

状态：候选（2026-08-20 立项，同日按"只归档、不物理删除"重写）。
当前 Table 与 Database **完全无法删除**，而 SKILL.md 曾让 Agent 以为可以。

## 实测现状：三层里两层是空的

| 对象 | 入口 | 语义 | 结论 |
|---|---|---|---|
| Row | `DELETE FROM db.t WHERE …` | `State=deleted`、`Revision++`、Route 归属清空为 `[]`、追加 `history.OperationDelete`、fulltext 投影为 `StateDeleted`；`RESTORE` 可回来 | **已完备，不改** |
| Route | `DELETE ROUTE :id` → `router.DeleteNodeIn`（`service.go:344`） | 递归 `deleteSubtree` 打 `Deleted=true`，反向归属解开，root 拒绝删除 | 已有，不改 |
| Relation | `UNRELATE` | `relation.StateDeleted`（`relation/service.go:134`） | 已有，不改 |
| Column | `PLAN SCHEMA CHANGE` → `APPLY SCHEMA CHANGE`（`ActionDrop`） | `Impact.Destructive=true, Reversible=false` | 已有，不改 |
| Table | 无 | 无 `catalog.DropTable` | **本 Feature** |
| Database | 无 | 无 `catalog.DropDatabase`，CLI 28 个子命令无一涉及 | **本 Feature** |

`DROP` 已在 `lexer/token.go:52` 保留为关键字，但 `parser.go:73` 的语句分发里没有它。

## 决定：容器只归档，不提供物理删除

动词是 **`ARCHIVE` / `UNARCHIVE`**，不是 `DROP`，也不是 `DELETE`。
这不是给删除换个说法，是承认引擎实际能做到的事：

1. **物理擦除本来就做不到。** MVCC 读者持 statement snapshot，
   durable-then-publish／no-steal 纪律里没有从活跃快照下抽走 Page 的机制；
   F151 Compaction 已延后，即使删掉 key，文件也不会变小。
   叫它 `DROP` 而背后是 tombstone，是在对用户撒谎。
2. **个人尺度上空间不是问题。** 实测 570 行约 556 KB。归档一个库省下的磁盘
   在这个量级没有意义，而可恢复性有意义。
3. **可逆性反过来简化了授权。** 一个完全可逆的操作不需要
   `PLAN` / `APPLY` 两阶段哈希绑定审批（那是给 `DROP_COLUMN` 这种不可逆动作的）。
   `ARCHIVE` 是单条 L2 语句。这一条推翻了本文档初版的两阶段设计。
4. **语义数据库里"过期"多于"错误"。** 归档保留了回答"我以前是怎么想的"的能力，
   而这正是产品主张的一部分。

真正的擦除是另一个问题，见文末"明确不做"。

## 语法

```text
ARCHIVE DATABASE work REASON :reason                      (L2)
ARCHIVE TABLE work.notes REASON :reason                   (L2)
UNARCHIVE DATABASE work                                   (L2)
UNARCHIVE TABLE work.notes                                (L2)
SHOW ARCHIVED DATABASES [CURSOR :c] [LIMIT :n] [COMPACT]  (L0)
SHOW ARCHIVED TABLES FROM work [...]                      (L0)
```

`REASON` 必填并写入 change log。`DROP TABLE` / `DROP DATABASE` 保持不被 parser 接受，
但错误信息从 `unsupported_statement` 改为点名 `ARCHIVE`——这是 Agent 最需要的一句提示。

## 五条实现约束

### 1. 可见性由容器自身决定，不下沉到每一行

Catalog 的 Database/Table 记录增加 `archived_at`、`archived_reason`。
其中的 Row **一行都不重写**。

理由是硬的：5,000 行的 Table 不该为一次归档产生 5,000 次 Row 修订；
F226 之后 poison 按库收敛，这种批量写正是最容易毒化该库的写入形态。
直接后果——change log 只留 **1 条** `object_archived`，
`UNARCHIVE` 之后每行的 `revision` 一个都没变。

### 2. "不可见"是一份穷举清单，漏一处就是 bug

`SHOW DATABASES`／`SHOW TABLES`／Catalog Atlas／Bootstrap Frame／
`visibleLexicalDatabaseIDs`（`executor/lexical_locations.go:52`）／Route 解析／
Relation 端点解析／`SHOW LEXICAL LOCATIONS FROM ALL TABLES`，
一律按"自身或任一祖先已归档"过滤。

**别名同样要退出解析集**（`Database.Aliases`、`Table.Aliases`），
否则一个 synonym 会解析到一个不可见对象。

写入也必须拒绝：归档对象上的 `INSERT`／`UPDATE`／`DELETE`／`CREATE ROUTE` 硬失败，
错误点明"已归档，先 UNARCHIVE"。否则它不叫归档。

### 3. 嵌套按最近的已归档祖先判定

归档 Database **不**逐个标记它的 Table。可见性看"自身或祖先"。
于是：单独归档过的 Table，在库被 `UNARCHIVE` 之后仍然是归档状态——
这是对的，`UNARCHIVE` 不该替用户撤销另一个决定。

### 4. 名字继续占用命名空间

归档对象保留原名。再建同名 Database/Table **硬失败**，错误里点名那个归档对象，
并给出 `UNARCHIVE` 与改名两条出路。

不做自动改名（`work@archived-<ts>` 之类）：悄悄改用户的数据名比报错更糟，
而对 Agent 来说一次带解法的报错只是一个 round trip。

### 5. 只读库与包安装库拒绝归档

`ReadOnly=true` 或 `PackageSHA256 != ""` 的 Database 走包卸载，不走归档。
包卸载本 Feature 不做。

## Admin UI

`SHOW DATABASES … COMPACT` 目前出现在 `catalog.js:294`、`changes.js:327`、
`traces.js:422` 三处，默认全部只列活跃对象。

新增"已归档"区域（Catalog 页内的独立分区，不是弹窗）：列出对象、`archived_at`、
`archived_reason`，提供 `UNARCHIVE`。归档入口本身不做一键按钮——
要求填 `REASON` 再提交，与引擎侧的必填保持一致。

前端不对数量设限，与 F223 定下的"限制是给 AI 的、不是给 UI 的"一致。

## 语义健康的联动

- `unrouted_row`、F224 强制 Route、F225 强制 summary **一律跳过归档容器内的 Row**，
  否则一次归档会把健康报告淹掉；
- 不需要 `dangling_relation`：没有物理删除，端点永远还在，只是被隐藏；
- 新增 `archived_container`（low_risk，仅信息）：告知归档对象数量与占用，
  让"仓库里堆了什么"是可见的。

## 分阶段

- **Stage 0（已完成，2026-08-20）**：SKILL.md 写明容器当前不可删除，
  Agent 不得承诺、不得用逐行删除模拟。
- **Stage 1**：Catalog 归档字段 + `ARCHIVE`/`UNARCHIVE TABLE` + 可见性过滤全清单 + 写入拒绝。
- **Stage 2**：Database 级同上，含命名空间占用与只读/包库拒绝。
- **Stage 3**：`SHOW ARCHIVED …`、change log `object_archived`、
  `archived_container` 健康项、F224/F225 归档豁免。
- **Stage 4**：SKILL.md 与两个 adapter、CLI 子命令、Admin UI 已归档区。

## 明确不做

- **不提供容器的物理删除。** 需要真正擦除时，这是一个产品级问题而非容器级问题：
  Memora 今天**在任何层级都没有真正的擦除**——Row `DELETE` 同样保留 History 与旧修订。
  隐私场景（写进去的密码、他人的敏感信息）需要一个统一的"抹除并重写 History"能力，
  应当单独立项一次做对，不是挂在归档上做半套。
- 不引入一行式 `DROP` DDL；
- 不为归档级联重写 Row，不级联删除 Relation；
- 不做跨 Database 的原子归档（一条语句一个对象）；
- 不做 Database Package 卸载。

## RED 与完成门

- RED 先证明：`DROP TABLE` / `DROP DATABASE` 当前是 `unsupported_statement`；
- 归档后：`SHOW TABLES`／Catalog Atlas／Bootstrap Frame／
  `SHOW LEXICAL LOCATIONS FROM ALL TABLES`／别名解析均不再出现该对象，
  且其中 Row 的 `revision` **一个都没变**；
- 归档是**一条** change log 记录，不随行数增长；
- 归档对象上的写入硬失败，错误点明 `UNARCHIVE`；
- `UNARCHIVE` 后对象与全部 Row 原样可见，`revision` 仍未变；
- 归档 Database 后再 `UNARCHIVE`，此前单独归档的 Table 仍保持归档；
- 同名 Database/Table 创建失败并点名归档对象；
- 只读库与包安装库拒绝归档，错误点明原因；
- 归档容器内的 Row 不产生 `unrouted_row`；
- `SHOW ARCHIVED …` 走与 `SHOW DATABASES` 同一套 cursor/limit/bytes 预算语义。

## 关联

- [执行计划](./execution-plan.md)
- [F223 Route Branch Fan-out 上限](./f223-route-branch-fanout-limit.md)
- [F224 Row 必须可导航](./f224-mandatory-row-route.md)
- [F225 Row 必须可展示](./f225-mandatory-row-summary.md)
- [F226 Database 级故障隔离](./f226-per-database-fault-isolation.md)
