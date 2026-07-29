# AI 自主治理、精确修改与 MVCC 设计

> 状态：架构讨论稿 0.1  
> 日期：2026-07-29  
> 项目暂定名：Memora  
> 相关文档：[AI-Native 个人数据库：竞品调研与概念设计](./AI_NATIVE_PERSONAL_DATABASE_RESEARCH_2026-07-29.md)
>  
> 查询协议：[MSQL、语义路由与上下文缓存协议](./MSQL_SEMANTIC_ROUTING_AND_CONTEXT_2026-07-29.md)

## 1. 文档目的

本文讨论 Memora 的两个核心问题：

1. 如何让 AI 自主创建、建模和维护个人数据库，同时防止结构失控、越权和误修改；
2. 如何通过事务、版本、MVCC、Undo、WAL 和历史修订，让 AI 能安全地精确修改数据并处理并发。

本文是方向性设计，不是最终实现规格。隔离级别、日志格式、锁粒度和持久化布局需要通过 Go 原型与故障注入测试后确定。

## 2. 核心定义

Memora 的 AI-native 定义是：

> AI 是数据库的主要创建者、建模者、写入者、查询者和维护者。用户通过自然交流和资料输入产生需求，不需要手工建表、切块、选择 Embedding、设计 ETL 或整理数据库结构。

典型流程：

```text
用户自然交流或提供资料
          │
          ▼
AI 判断当前主题与 Space
          │
          ├── 发现已有数据库结构
          ├── 复用或演化已有 Type
          ├── 必要时创建 Space/Type/View/Link
          ├── 创建或精确修改对象
          └── 维护索引、自描述信息和来源
```

AI-native 不代表 AI 可以绕过数据库规则。更准确的分工是：

> AI 负责语义判断和建模决策；数据库引擎负责一致性、安全边界、并发、历史与可恢复性。

## 3. 约束与自主权的分层

Memora 不能采用两个极端：

- 完全固定 Schema：AI 只能向预定义记忆表追加文本；
- 完全自由 Schema：不同 Agent 随意创建同义类型和字段，最终无法理解或迁移。

建议采用四层治理模型。

### 3.1 引擎不变量：AI 不能绕过

这些约束由内核强制执行：

- 对象 ID、事务 ID 和版本号必须合法且唯一；
- revision 链不能产生环；
- 已提交版本不可原地篡改；
- 跨 Space 读取和写入必须通过权限检查；
- 引用完整性必须满足类型策略；
- 写事务必须原子提交或完整回滚；
- 索引不能成为唯一真相源；
- provenance 和 actor 不能被普通更新静默覆盖；
- 系统保留字段不能被用户 Schema 重新定义；
- 文件格式、校验和及日志顺序必须合法。

### 3.2 Policy：个人可以配置

Policy 决定 AI 在某个 Space 内可以做什么：

```text
policy project.memora
├── allow_create_type: true
├── allow_cross_space_link: ask
├── allow_hard_delete: false
├── allow_merge_objects: true
├── require_reason_for_schema_change: true
├── max_auto_mutations_per_turn: 50
└── sensitive_fields: [secret, token, medical]
```

Policy 本身是带版本和审计历史的数据库对象。

### 3.3 Ontology 约定：可演化但需治理

类型、字段、别名和关系允许 AI 创建和修改，但必须先执行发现与冲突检查：

```text
AI 提议创建 reading_note
          │
          ▼
检查已有类型和别名
          │
          ├── 已有等价类型 -> 复用
          ├── 可扩展已有类型 -> 提议 ALTER
          ├── 名称相似但语义不同 -> 创建并注明差异
          └── 确为新概念 -> 创建并记录理由
```

数据库应支持：

- Type/Field discovery；
- Schema 相似度提示；
- 类型和字段别名；
- rename、retype、merge、split；
- Schema migration；
- 兼容视图；
- deprecated 状态；
- Schema 决策来源和理由。

### 3.4 Agent 自主决策区

在满足前三层约束后，AI 可以自主：

- 选择或创建 Space；
- 创建适合当前领域的 Type；
- 判断本轮信息是否值得长期保存；
- 创建、修订、关联、合并或拆分对象；
- 建立查询 View；
- 更新 Root Manifest 和领域摘要；
- 对低风险错误进行自动修复；
- 选择检索策略和查询计划。

## 4. 操作风险等级

不是所有写操作都需要用户逐次确认，否则 AI-native 会退化成人工数据库管理。建议按照可恢复性和影响范围分级。

### 4.1 L0：只读操作

- inspect、describe、select、search、explain；
- 不需要确认；
- 仍受 Space 权限和敏感字段策略约束。

### 4.2 L1：低风险、局部且可逆

- 创建普通对象；
- 更新一个对象的非敏感字段；
- 添加同一 Space 内的关系；
- 保存明确决策或活跃任务；
- 默认允许自动执行并记录 provenance。

### 4.3 L2：结构或批量变化

- 创建、修改或合并 Type；
- 批量更新；
- 跨 Space Link；
- 大规模对象合并、拆分或移动；
- 应先生成 mutation plan 和影响分析；Policy 决定自动执行还是请求确认。

### 4.4 L3：高风险或难恢复

- hard delete；
- 清除历史；
- 降低隐私等级；
- 导出敏感 Space；
- 强制覆盖并发修改；
- 默认必须显式授权，且不能仅由 Skill 文本绕过。

## 5. AI 修改协议

AI 写入不应只是自由生成 SQL 后立即执行。建议采用：

```text
Intent
  │
  ▼
Discover
  读取 Space、Type、对象当前版本与约束
  │
  ▼
Plan
  生成结构化 mutation plan
  │
  ▼
Validate
  语法、Schema、Policy、引用和影响检查
  │
  ▼
Execute
  在事务和快照中执行
  │
  ▼
Verify
  检查写入结果和预期状态
  │
  ▼
Commit / Rollback
```

### 5.1 Mutation Plan

计划示例：

```json
{
  "space": "project.memora",
  "reason": "用户确认第一版不依赖向量 API",
  "expected_snapshot": 1842,
  "operations": [
    {
      "op": "revise",
      "object_id": "decision_vector_search",
      "expected_revision": 3,
      "set": {
        "status": "rejected",
        "reason": "外部 API 违背离线和零配置目标"
      }
    },
    {
      "op": "create",
      "type": "design_decision",
      "values": {
        "title": "核心检索使用 BM25 与结构化索引"
      }
    }
  ]
}
```

Mutation Plan 是结构化操作，不依赖模型输出的自然语言说明。高级 CLI 可以把它编译为内部执行计划或 SQL，并提供 `--dry-run`、`--explain` 和 `--show-sql`。

### 5.2 修改前置条件

每次重要修改应允许携带：

- `expected_revision`：对象仍是 AI 刚才读取的版本；
- `expected_snapshot`：数据库快照未发生不兼容变化；
- `if_exists` / `if_not_exists`；
- 字段值断言；
- 类型和 Policy 版本断言；
- 最大影响行数；
- 允许访问的 Space 清单。

前置条件不满足时，不允许静默覆盖。

## 6. AI-native 修改操作

普通 `INSERT/UPDATE/DELETE` 必须支持，同时提供适合知识维护的高级语义操作：

```text
CREATE       创建对象
UPSERT       根据稳定身份创建或更新
PATCH        只修改指定字段
REVISE       创建带理由的新修订
SUPERSEDE    用新结论取代旧结论
MERGE        合并重复对象并迁移关系
SPLIT        把混合对象拆成多个对象
MOVE         在 Space/Domain 之间移动
RETYPE       修改对象语义类型
RENAME       修改类型、字段或对象名称
LINK         创建关系
UNLINK       终止关系
ARCHIVE      从默认视图移除但保留历史
RESTORE      恢复归档对象
DELETE       逻辑删除
PURGE        按高风险策略物理清除
```

高级操作可以作为 SQL 方言、系统存储过程或 CLI 命令实现。最终形态需要在 parser 原型阶段决定。

### 6.1 Merge

```sql
MERGE OBJECT 'concept_b_plus_tree'
INTO 'concept_btree'
PRESERVE SOURCES;
```

Merge 需要原子处理：

- 字段冲突；
- 入边和出边迁移；
- 别名；
- 来源与历史；
- 搜索索引；
- 旧 ID 到新 ID 的永久重定向。

### 6.2 Split

```sql
SPLIT OBJECT 'decision_123'
INTO (
  TYPE design_decision TITLE '采用分页存储',
  TYPE design_decision TITLE '第一版不依赖向量 API'
);
```

旧对象进入 `superseded`，新对象保存 `derived_from`，关系按计划分配，不能简单删除旧记录。

### 6.3 Retype 与 Move

```sql
RETYPE OBJECT 'object_789'
FROM hypothesis
TO design_decision
REASON '用户已经明确确认';
```

```sql
MOVE OBJECT 'claim_456'
FROM SPACE 'project.memora'
TO SPACE 'learning.database';
```

跨 Space Move 必须同时检查目标 Schema、Policy、关系可见性和敏感数据规则。

## 7. 必须区分的四类版本号

“数据库有版本号”至少包含四个完全不同的概念，不能混用一个 `version` 字段。

### 7.1 Format Version

表示磁盘文件和 Page 编码格式：

```text
format_version = 1
```

由数据库引擎管理，用于判断当前二进制是否能安全读取文件。

### 7.2 Schema/Ontology Version

表示某个 Space 的类型、字段、约束和别名版本：

```text
schema_version(project.memora) = 12
```

Schema 变化本身也必须通过事务提交并具有历史。

### 7.3 Transaction/Commit Version

全库单调递增的提交序号，也可以称为：

```text
commit_seq = 1842
```

它定义 MVCC 的可见性和一致快照。第一版建议使用本地 64 位单调递增序号，不直接使用墙上时钟作为事务顺序。

### 7.4 Object Revision

表示某个逻辑对象发生了多少次语义修订：

```text
object_id = claim_abc
revision = 7
```

对象 revision 适合 Agent 做乐观并发检查；commit sequence 适合引擎判断版本可见性。一次事务可以创建多个对象 revision，但只产生一个 commit sequence。

## 8. MVCC 数据模型

每个逻辑对象有稳定 `object_id`，每次修改生成一个不可变版本记录：

```text
Object Version
├── object_id
├── object_revision
├── creator_txn_id
├── begin_commit_seq
├── end_commit_seq
├── previous_version
├── operation
├── payload
├── provenance
├── reason
└── checksum
```

概念上的可见范围：

```text
begin_commit_seq <= snapshot_seq < end_commit_seq
```

当前版本的 `end_commit_seq` 可以使用 `infinity`，或通过版本链和 current catalog 表达。未提交版本只对创建它的事务可见。

### 8.1 示例

```text
claim_abc

revision 1
  value: Go
  visible: [100, 145)

revision 2
  value: Go 1.26
  visible: [145, infinity)
```

在快照 120 查询时看到 revision 1；在快照 160 查询时看到 revision 2。

### 8.2 默认隔离级别候选

第一版建议优先实现 Snapshot Isolation：

- 事务开始时获取 `snapshot_seq`；
- 同一事务中的查询看到稳定快照；
- 读取不会阻塞普通写入；
- 不同对象的写入可以并发；
- 同一对象发生并发写入时采用 first-committer-wins；
- 写偏差等更复杂异常可以在后续 Serializable 模式解决。

对于 AI 客户端，大部分事务应该短小，包含一次可解释的状态变更。长时间推理不应一直占用数据库事务：AI 先读取并生成计划，最后以 `expected_revision` 开启短事务提交。

## 9. CLI 多进程并发模型

Memora 以 CLI 为主要接口，因此即使单机，也可能同时存在：

- Codex 进程；
- Claude Code 进程；
- 后台 compaction；
- 索引任务；
- 用户手工 CLI；
- 文件同步或备份工具。

不能假设只有一个写入者。

### 9.1 目标

- 多个只读查询并发；
- 读取不因普通写事务长期阻塞；
- 不同对象或不同 Page 的修改尽量并发；
- 同一对象的竞争修改返回明确冲突；
- Schema 修改和 compaction 有更强协调；
- 进程崩溃后数据库可以恢复到完整提交边界。

### 9.2 写冲突

两个 Agent 同时读取 revision 3：

```text
Agent A: UPDATE expected_revision = 3
Agent B: UPDATE expected_revision = 3
```

Agent A 先提交 revision 4 后，Agent B 的提交必须失败：

```json
{
  "ok": false,
  "error": {
    "code": "REVISION_CONFLICT",
    "object_id": "claim_abc",
    "expected_revision": 3,
    "actual_revision": 4,
    "snapshot_seq": 1842,
    "current_commit_seq": 1843,
    "retryable": true,
    "suggestion": "Reload the object, compare revisions, and re-plan the mutation"
  }
}
```

数据库不替 AI 猜测如何合并两个语义变化。字段级无冲突的 Patch 可以在后续提供自动合并，但必须生成明确 merge revision。

### 9.3 锁粒度候选

MVCC 不能消除所有锁。可能仍需要：

- 数据库文件/进程协调锁；
- commit 序号分配锁；
- catalog 与 Schema 锁；
- Page latch；
- 对象写意向或冲突表；
- compaction 与 checkpoint 协调锁。

具体锁实现应尽量短持有，不能在调用 LLM 或等待用户时占用。

## 10. Undo、WAL 与历史修订的关系

三个概念容易混淆：

### 10.1 历史 Revision

面向用户和 Agent，回答：

- 这条知识以前是什么？
- 谁改的？
- 为什么改？
- 能否回到某个历史状态？

它属于长期语义历史。

### 10.2 Undo Log

面向事务引擎，回答：

- 当前事务失败后如何撤销未提交修改？
- 如何为旧快照重建先前可见状态？
- 如何回滚 Page、索引和 catalog 的部分变更？

Undo 可以采用 before-image、inverse operation 或旧版本指针。由于 Memora 倾向 append-first，很多数据对象的旧版本天然存在，但 Page 分配、索引、引用计数和 catalog 修改仍需要明确的回滚信息。

### 10.3 WAL/Redo Log

面向崩溃恢复，回答：

- 已经承诺提交的数据是否真正落盘？
- 进程在写到一半时，启动后应该重放哪些操作？

建议遵守 write-ahead 原则：日志相关记录持久化后，脏页才能被认为安全写回。

### 10.4 候选事务日志流程

```text
BEGIN txn 9001 at snapshot 1842
  │
  ├── 生成新 Object Version
  ├── 记录索引变更
  ├── 记录 Page 分配/修改的 Undo 信息
  ├── WAL append
  │
  ▼
VALIDATE
  检查 expected_revision、Schema、Policy 和引用
  │
  ▼
COMMIT
  分配 commit_seq 1843
  写 commit record
  fsync durability boundary
  发布新版本可见性
```

崩溃恢复：

```text
扫描 WAL
├── 有完整 commit record -> redo 必要修改
└── 无 commit record      -> undo/丢弃未提交修改
```

具体采用 ARIES 风格、shadow paging、纯 append log 或混合设计尚未决定，不应在原型前过早锁定。

## 11. 回溯、撤销与时间旅行

### 11.1 查询历史快照

候选 SQL：

```sql
SELECT *
FROM claims
AS OF COMMIT 1842
WHERE id = 'claim_abc';
```

或者：

```sql
SELECT *
FROM HISTORY('claim_abc')
ORDER BY revision;
```

### 11.2 Undo 已提交修改

已提交修改的“撤销”不应删除历史，而是创建一个新的补偿事务：

```text
revision 3: value A
revision 4: value B
revision 5: revert revision 4 -> value A
```

revision 4 仍然保留，revision 5 记录：

- `reverts_revision = 4`；
- 撤销原因；
- actor；
- commit sequence；
- 原始来源。

候选命令：

```bash
memora undo --transaction 1843 --reason "Agent 修改了错误的项目"
```

执行前必须分析后续事务是否依赖该修改，不能盲目恢复旧字节。

### 11.3 Space 级时间旅行

数据库可以在指定快照读取整个 Space：

```sql
SET SNAPSHOT = 1842;
SELECT * FROM project_memora.active_decisions;
```

第一版可以只提供只读时间旅行。分支、合并和将整个 Space 回滚到历史状态可作为后续能力。

## 12. Schema 的 MVCC

AI 自主建模意味着 Schema 也会并发变化，因此 Schema 不能是进程外的静态配置。

Schema/ontology 应作为版本化系统对象存储：

```text
Type: design_decision
schema revision 11
  fields: title, status, reason

schema revision 12
  fields: title, status, reason, confidence
```

事务在固定 Schema snapshot 下解析和执行 SQL。提交时如果相关 Type 的 Schema 已变化，需要：

- 证明变化兼容并继续；或
- 返回 `SCHEMA_VERSION_CONFLICT`，要求 Agent 重新发现和生成计划。

字段 rename 应优先保留稳定 field ID：

```text
field_id: fld_123
old_name: rationale
new_name: reason
```

名字变化不应导致数据重写或来源断裂。

## 13. 索引与 MVCC

索引项需要能判断对象版本是否对当前快照可见：

```text
posting
├── term
├── object_id
├── object_revision
├── begin_commit_seq
└── end_commit_seq/tombstone
```

查询步骤：

1. 从 segment/posting 找到候选版本；
2. 根据 snapshot 过滤不可见版本；
3. 读取当前快照对应的对象内容；
4. 计算 BM25、字段权重和关系权重；
5. 返回带 revision 和 commit sequence 的结果。

后台 compaction 只有在确认不存在更老的活跃快照、备份或保留要求时，才能物理回收旧版本和 tombstone。

## 14. 自描述与自主建模

为了让另一个 AI 接管数据库，每个 Space 必须自我描述：

```text
Space Manifest
├── purpose
├── scope
├── owner
├── policies
├── types and aliases
├── recommended entry views
├── current schema version
├── active goals
├── unresolved conflicts
└── recent structural changes
```

AI 每次创建或修改 Type 时，需要同步维护：

- 类型用途；
- 字段语义；
- 示例；
- 与相似类型的区别；
- 创建或修改理由；
- 兼容和迁移信息。

数据库可以拒绝只有名字、没有语义说明的新 Type。

## 15. Skill 的职责

Skill 不是数据库 Schema，也不能代替内核约束。它是 Agent 的操作协议。

Skill 应要求 Agent：

### 会话开始

- 调用 bootstrap/inspect；
- 判断当前涉及的 Space 和 Topic；
- 读取相关 Schema 与活跃状态；
- 不扫描或注入整个数据库。

### 写入之前

- 先查重和发现已有类型；
- 区分 decision、hypothesis、fact 和临时想法；
- 读取待修改对象的最新 revision；
- 生成带前置条件的 mutation plan；
- 对批量或跨 Space 修改先 dry-run。

### 提交之后

- 验证结果；
- 检查是否产生未解决冲突；
- 必要时更新 Manifest；
- 不重复保存整段对话；
- 只沉淀有效状态变化。

### 遇到冲突

- 不使用强制覆盖作为默认重试；
- 重新读取最新版本；
- 比较两个修改的语义；
- 可以安全合并时创建 merge revision；
- 无法判断时请求用户决策。

## 16. 面向 Agent 的事务错误

错误必须机器可读、可定位、可恢复：

```json
{
  "ok": false,
  "error": {
    "code": "SCHEMA_VERSION_CONFLICT",
    "message": "type 'design_decision' changed after the plan was created",
    "expected_schema_version": 11,
    "actual_schema_version": 12,
    "retryable": true,
    "recovery": {
      "action": "describe_type",
      "command": "memora describe type design_decision --json"
    }
  }
}
```

第一版至少需要稳定区分：

- SQL 语法错误；
- 未知 Type/Field；
- Policy 拒绝；
- revision 冲突；
- Schema 版本冲突；
- 引用完整性错误；
- 事务中止；
- 数据库忙或锁超时；
- WAL/恢复错误；
- 文件格式不兼容；
- 派生索引尚未就绪。

## 17. 生命周期与垃圾回收

MVCC 和历史修订会持续增加数据量，需要区分逻辑历史与物理垃圾。

不能回收的内容：

- Policy 要求保留的审计历史；
- 尚被有效语义记录、Relation 或 Source Receipt 引用的版本；
- 活跃快照仍可能读取的版本；
- 尚未完成备份的提交；
- legal hold 或用户锁定内容。

可候选回收的内容：

- 已被合并且超过保留期的索引 segment；
- 可重建的旧摘要和向量；
- 无活跃快照可见的临时版本；
- 中止事务遗留 Page；
- 无引用且允许清理的对象 blob。

GC/compaction 必须产生报告，不能让 AI 在不知情的情况下把“逻辑删除”变成不可恢复的物理删除。

## 18. 第一版实现边界建议

第一版不需要立刻实现 MySQL 的全部复杂度，但必须从数据结构上避免封死未来。

### 18.1 MVP 必须具备

- 稳定 object ID；
- 不可变 object version；
- 单调递增 commit sequence；
- 短事务；
- Snapshot Read；
- 同对象 first-committer-wins；
- `expected_revision` 乐观并发控制；
- 原子多对象 mutation；
- WAL 与崩溃恢复；
- 未提交事务回滚；
- 逻辑删除和历史查询；
- Schema 版本及变更历史；
- 结构化事务错误；
- 多 CLI 进程并发测试。

### 18.2 可后续实现

- Serializable 隔离；
- 字段级自动合并；
- 长事务；
- 分布式 MVCC；
- Space branch/merge；
- 完整 SQL time-travel 方言；
- 跨设备事务复制；
- 用户可配置的复杂锁策略；
- 自动历史压缩策略。

## 19. 需要原型验证的问题

1. 单调 commit sequence 如何在多进程下低成本分配？
2. Page 使用 in-place update + WAL，还是 Copy-on-Write + manifest swap？
3. Undo 采用 before-image、逻辑 inverse operation，还是旧版本链为主的混合方式？
4. 索引 segment 与对象事务如何实现原子可见？
5. Schema snapshot 如何参与 SQL parse、bind 和 execute？
6. Snapshot Isolation 是否足以覆盖个人多 Agent 使用场景？
7. CLI 短进程是否每次独立恢复/打开数据库，还是使用可选 daemon？
8. 多进程锁采用操作系统文件锁、共享内存还是本地协调进程？
9. compaction 如何确定全局最老活跃快照？
10. 回滚已提交语义操作时，如何处理后续依赖和关系？
11. AI 自动 Schema 变化的默认风险等级如何划分？
12. Mutation Plan 是公开稳定协议，还是 CLI 内部表示？

## 20. 当前方向性结论

1. AI 是 Memora 的主要 DBA，但不是数据库内核规则的制定者；
2. 内核不变量、个人 Policy、Ontology 治理和 Agent 自主区分层处理；
3. 低风险可逆修改允许 AI 自动执行，高风险和跨边界操作需要更强授权；
4. AI 修改采用 Discover、Plan、Validate、Execute、Verify、Commit/Rollback 流程；
5. 所有重要更新携带 expected revision、reason、actor 和 provenance；
6. 对象 revision、事务 commit sequence、Schema version 和文件 format version 必须分离；
7. 第一版以 Snapshot Isolation 和乐观并发控制为主要并发模型；
8. 长时间 AI 推理不能占用数据库事务，提交阶段使用短事务重新验证；
9. 同一对象的并发更新默认 first-committer-wins，不允许静默覆盖；
10. 历史 revision、Undo Log 和 WAL/Redo 是三个不同层次；
11. 已提交修改通过补偿 revision 撤销，而不是删除历史；
12. Schema 也是 MVCC 管理的版本化数据；
13. 索引必须支持快照可见性，compaction 不能破坏活跃快照和保留策略；
14. Skill 指导 AI 行为，真正的安全与一致性必须由引擎执行。
