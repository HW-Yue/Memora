# AI-Native 个人数据库：竞品调研与概念设计

> 状态：讨论稿 0.2  
> 首次创建：2026-07-28  
> 最近更新：2026-07-29  
> 项目暂定名：Memora

相关设计文档：

- [AI 自主治理、精确修改与 MVCC 设计](./AI_NATIVE_AUTONOMY_AND_MVCC_2026-07-29.md)
- [MSQL、语义路由与上下文缓存协议](./MSQL_SEMANTIC_ROUTING_AND_CONTEXT_2026-07-29.md)
- [Wiki 与 Obsidian Markdown 导出设计](./WIKI_EXPORT_DESIGN_2026-07-29.md)

> 当前设计修正（2026-07-29）：后续讨论已明确 Memora 不保存完整大文档、PDF、图片或机械切块。本文第 7、9、15、16 节中的 Artifact Archive、大对象内容树和原文持久化属于早期探索，已被新协议取代。当前方案以“临时读取资料 → AI 吸收和建模 → 通过 MSQL 写入约 800 字的完整语义模块 → 清除临时输入”为准。详见《MSQL、语义路由与上下文缓存协议》第 13、14 节。

## 1. 文档目的

本文记录 Memora 在立项阶段对 AI Agent 数据库、长期记忆、图向量数据库和便携式知识容器的初步调研，并提出第一版产品定义。

本文不是最终技术规格。尚未确定的内容使用“建议”“候选”或“待验证”表述，后续将通过原型、基准测试和真实 Agent 使用实验逐步收敛。

## 2. 问题定义

现有 Agent 长期状态通常采用两条路线：

1. 使用 Markdown、JSONL 等文本文件保存上下文；
2. 使用 SQLite、关系数据库或向量数据库，加上切块、Embedding 和 RAG 管线。

两条路线都有明显限制。

### 2.1 Markdown 作为长期数据库的限制

- 文件变大后，Agent 必须读取大量无关内容，消耗上下文窗口；
- 局部更新依赖字符串匹配或整段重写，缺乏稳定身份；
- 事实、推论、任务、决策和历史混在自然语言中；
- 很难表达覆盖、撤销、矛盾、来源和有效时间；
- 并发写入、原子更新、索引和一致性处理困难；
- 跨文件关系通常依赖约定，缺少数据库级保证。

### 2.2 传统数据库加向量检索的限制

- 表、列、JOIN 和应用 Schema 首先是为传统应用开发者设计的；
- Agent 必须预先知道表结构，才能生成可执行查询；
- 向量通常只是附加字段或插件，语义并未进入核心数据模型；
- RAG 常把文档机械切成定长片段并编号，切块不是稳定、可维护的知识对象；
- 修改原文后需要重新切块、重新编号和重新向量化；
- 很难精确表达“只修改这一条结论，同时保留其证据和历史”；
- Embedding、索引和原始事实容易耦合，换模型或换机器时迁移成本高。

### 2.3 Memora 希望解决的问题

Memora 希望成为一个真正面向个人和 Agent 的数据库：

- 数据只属于个人，可以长期积累；
- 项目、生活、学习、工作等领域默认相互隔离；
- 不同领域可以通过明确授权的关系和查询进行联动；
- 数据库复制到另一台电脑后，新的 Agent 不依赖旧会话上下文，也能快速理解库的身份、内容地图、语义类型和查询入口；
- 原始资料、结构化知识、关系、历史和检索索引可以统一保存；
- Agent 能对已保存内容进行精确、可审计的增删改，而不是只能追加大段文本；
- CLI 和 SQL 都是稳定的一等接口；
- Skill 教会 Claude Code、Codex 等 Agent 何时以及如何使用数据库。

## 3. 产品定位

暂定定义：

> Memora 是一个 self-describing、local-first、multi-model、agent-native 的个人数据库。它以稳定语义对象而不是 Markdown 段落或向量切块作为核心数据单位，并通过 CLI、SQL 和上下文编译接口供 Agent 使用。

这里的 AI-native 不等于“内置调用大模型”，也不等于“数据库里有向量字段”。它至少包含以下性质：

1. **自描述**：数据库携带自身用途、领域地图、类型定义、策略和推荐入口；
2. **可探索**：Agent 不知道 Schema 时，也能通过固定命令逐步发现数据；
3. **语义对象一等化**：事实、实体、事件、任务、决策、推论和关系具有稳定身份；
4. **知识演化一等化**：支持来源、修订、覆盖、撤销、矛盾、有效时间和置信度；
5. **上下文预算一等化**：查询可以按 token 预算编译出适合模型消费的 Context Pack；
6. **多种召回一等化**：结构化、全文、向量、图、时间和因果查询可以组合；
7. **派生数据可重建**：Embedding、摘要、聚类和索引不是唯一真相源；
8. **Agent 操作可验证**：稳定语法、机器可读错误、预检、事务和审计历史都是核心能力。

## 4. GitHub 相似项目调研

### 4.1 Memvid

- 仓库：https://github.com/memvid/memvid
- 定位：面向 AI Agent 的便携式单文件记忆层；
- 核心：把内容、Embedding、检索结构和元数据打包进单个文件；
- 存储：append-only Smart Frames，带时间戳、校验和与历史状态；
- 借鉴：单文件 capsule、不可变帧、时间旅行、索引与数据共同迁移；
- 差异：更偏长期记忆和 RAG 文件，不是完整的个人多模型数据库。

### 4.2 HEBBS

- 仓库：https://github.com/hebbs-ai/hebbs-memory-engine
- Skill：https://github.com/hebbs-ai/hebbs-skill
- 定位：面向 Agent 的认知记忆引擎；
- 核心：相似、时间、因果和类比四类召回，配合衰减、强化和 consolidation；
- 形态：单个可执行程序、Agent Skill、可携带的 `.hebbs/` 派生认知层；
- 借鉴：检索不能只有 vector top-k；Skill 和检索策略都属于产品；
- 差异：Markdown 文件仍是 source of truth，索引层可以删除和重建。

### 4.3 HelixDB

- 仓库：https://github.com/HelixDB/helix-db
- 定位：从底层实现的图—向量数据库；
- 核心：图、向量、KV、文档和关系模型，支持动态 JSON 查询及 MCP；
- 借鉴：图与向量的统一、Agent 可生成的结构化查询树；
- 差异：主要服务 AI 应用后端，数据库的无上下文自解释不是主要目标。

### 4.4 YantrikDB

- 仓库：https://github.com/yantrikos/yantrikdb-server
- 定位：能够遗忘、合并和检测矛盾的认知记忆数据库；
- 核心：时间衰减、记忆 consolidation、冲突检测；
- 借鉴：数据库应管理知识质量，而不只是保存向量；
- 差异：主要聚焦记忆生命周期，而不是个人通用数据库。

### 4.5 MatrixOrigin Memoria

- 仓库：https://github.com/matrixorigin/Memoria
- 定位：带 Git 式版本控制的 Agent 记忆层；
- 核心：snapshot、branch、merge、rollback、provenance 和矛盾治理；
- 借鉴：Agent 的探索性写入需要分支、审查和回滚；
- 差异：依赖 MatrixOne 体系，更像 Agent 记忆平台而不是轻量本地个人数据库。

### 4.6 OceanBase seekdb

- 仓库：https://github.com/oceanbase/seekdb
- 定位：面向 Agent 状态的 AI-native 搜索数据库；
- 核心：结构化、全文与向量混合检索，数据库级 COW `FORK/MERGE`；
- 借鉴：SQL 生态、混合查询、隔离实验和合并；
- 差异：保留 MySQL 兼容和传统关系模型，目标是工作负载 AI-native，不是数据表达本身 AI-native。

### 4.7 其他相关项目

- sqlite-memory：https://github.com/sqliteai/sqlite-memory
  - Markdown、SQLite、FTS5、向量搜索和 CRDT 的组合；
- agentmemory：https://github.com/jayzeng/agentmemory
  - 面向 Coding Agent 的 Markdown 记忆、CLI、Skill 和多种搜索模式；
- TencentDB-Agent-Memory：https://github.com/Tencent/TencentDB-Agent-Memory
  - 本地长期记忆、分层管线、跨 Agent/设备迁移和 Skill 生成；
- SwarmVault：https://github.com/swarmclawai/swarmvault
  - Markdown Wiki、知识图、RAG 和 Agent Memory；
- OpenMemory：https://github.com/kishan0725/AgentMemory
  - 基于 SQLite/Postgres 的本地认知记忆服务。

## 5. 竞品结论与空缺

| 类别 | 代表项目 | 已解决 | 仍未充分解决 |
| --- | --- | --- | --- |
| Markdown + 索引 | HEBBS、agentmemory | 可读、易接入、可搜索 | 精确更新、强关系、事务、长期演化 |
| SQL + Vector | sqlite-memory、seekdb | 成熟事务、混合检索 | Agent 自解释、原生语义对象 |
| 认知记忆 | YantrikDB、Memoria | 遗忘、合并、冲突、历史 | 通用个人数据和多模态状态 |
| 单文件记忆 | Memvid | 便携、离线、快速召回 | 丰富可编辑数据模型与通用查询 |
| Graph + Vector | HelixDB | 关系、向量、性能 | 个人数据库、无上下文接管 |

初步判断：现有项目尚未完整覆盖“个人数据库 + 无上下文自描述 + 精确语义编辑 + SQL/Agent 双重查询 + 多领域隔离联动”。这是 Memora 可以验证的产品空缺。

## 6. 个人数据库的隔离与联动

“各种方面互不打扰，又能很好联动”不应简单实现成很多互不相干的数据库文件。建议引入四个概念：

### 6.1 Space

`Space` 是数据隔离、权限和默认检索的边界，例如：

- `personal.health`
- `personal.finance`
- `work.company-a`
- `project.memora`
- `learning.english`

Agent 默认只能操作当前 Space，不会在查询代码问题时意外召回健康或财务信息。

### 6.2 Domain

`Domain` 是 Space 内的语义领域，可共享类型和术语。Domain 主要帮助组织与检索，不一定是安全边界。

### 6.3 Link

跨 Space 不自动混查，而是通过显式 `Link` 连接：

```text
project.memora:task/launch
    --depends_on-->
personal.calendar:event/2026-09-vacation
```

Link 可以只暴露目标的摘要、某些字段，或允许完整跟随。

### 6.4 View

`View` 是经过授权的跨 Space 联合视图，例如“本周安排”可以联动工作任务、个人日历和旅行计划，却不暴露无关内容。

建议原则：

- 默认隔离；
- 显式联动；
- 最小披露；
- 每次跨边界查询可审计；
- Context Pack 标注每条内容来自哪个 Space。

## 7. 核心数据模型：语义对象而不是切块

### 7.1 为什么固定切块不优雅

传统 RAG 通常执行：

```text
文档 -> 按长度切块 -> chunk_001/chunk_002 -> Embedding -> top-k
```

问题在于 chunk 只是索引过程产生的技术碎片，不是用户或 Agent 真正关心的对象。它没有稳定语义边界，无法自然地被修改、引用、覆盖或建立关系。

### 7.2 建议：Artifact、Node、Claim 三层模型

#### Artifact：原始材料

原始输入保持完整，例如一篇 Markdown、一封邮件、一个 PDF、一段对话或一张图片。Artifact 不因索引方式改变而丢失。

#### Node：有稳定身份的内容结构

Node 表示材料中的自然结构，例如：

- 文档章节；
- 对话轮次；
- 表格行；
- 代码符号；
- 邮件；
- 图片区域；
- 音频时间段。

Node 使用稳定 ID，并记录在原始 Artifact 中的位置锚点。它不是固定 token 长度的 chunk。

#### Claim：可独立维护的语义陈述

Claim 是 Agent 可以精确修改的最小知识单位，例如：

```text
主体：Memora
谓词：implementation_language
客体：Go
状态：accepted
有效时间：2026-07-28 起
来源：architecture-discussion/turn-3
置信度：1.0
```

Claim 可以是：

- fact：事实；
- decision：决策；
- preference：偏好；
- hypothesis：假设；
- inference：推论；
- constraint：约束；
- task state：任务状态。

Claim 不要求所有知识都变成简单三元组。它可以拥有正文、结构化属性、证据、限定条件和关系。

### 7.3 Chunk 降级为派生检索窗口

系统仍然可以为了 Embedding 临时构造检索窗口，但它不再是权威数据：

```text
Artifact + Nodes + Claims
          │
          ├── keyword index
          ├── vector windows
          ├── graph index
          └── summaries
```

检索窗口应记录它由哪些 Node/Claim 组合产生。修改一条 Claim 后，只重建受影响的窗口，而不是重新编号整篇文档。

### 7.4 精细编辑示例

```sql
UPDATE claims
SET object = 'Go 1.26',
    confidence = 1.0
WHERE id = 'claim_01J...';
```

数据库内部不原地销毁历史，而是产生一次 revision：

```text
revision 1: implementation_language = Go
revision 2: implementation_language = Go 1.26
reason: 锁定工具链版本
actor: codex
```

也可以明确推翻旧结论：

```sql
SUPERSEDE CLAIM 'claim_old'
WITH (subject = 'Memora', predicate = 'storage_format', object = 'MORA Frames')
REASON '不采用 SQLite 作为核心格式';
```

这里的 `SUPERSEDE` 可以先作为 CLI/扩展语法，底层编译成事务操作；是否直接扩展 SQL 语法需要原型验证。

## 8. SQL 应当是一等接口

SQL 对 Agent 仍然很有价值：

- 大模型见过大量 SQL，生成能力成熟；
- SELECT、WHERE、JOIN、GROUP BY、ORDER BY 的表达规范且紧凑；
- 语法错误可以被解析器精确定位；
- 查询可以 dry-run、解释执行计划和设置只读权限；
- 人类开发者也可以调试和复现 Agent 操作。

但不建议用正则表达式验证完整 SQL。SQL 有嵌套、引号、注释、子查询和方言差异，正则只能做外围约束，不能可靠解析语法。

建议流程：

```text
Agent 输出 SQL
     │
     ▼
Lexer / Parser 生成 AST
     │
     ├── 语法诊断：行、列、期待的 token
     ├── 能力检查：是否使用支持的语法
     ├── 权限检查：是否越过 Space 边界
     ├── 影响分析：预计读取/修改多少对象
     └── 查询执行或拒绝
```

正则可以用于：

- 从 Markdown code fence 中提取 SQL；
- 检查是否只有一条 statement；
- 屏蔽明显危险的 CLI 输入；
- 对错误响应做稳定分类。

SQL 本身应由真正的 parser 处理。

### 8.1 SQL 与 AI-native 能力的关系

建议保留标准 SQL 子集，并通过以下方式扩展：

1. 系统表：`memora_spaces`、`memora_types`、`memora_links`；
2. 表值函数：`semantic_search()`、`context_pack()`、`graph_walk()`；
3. 明确的扩展语法：仅用于版本、分支、覆盖和来源操作；
4. CLI 高级命令：编译为 SQL/内部查询计划，并支持 `--show-sql`；
5. JSON 参数绑定：避免 Agent 手工转义长文本。

示例：

```sql
SELECT c.id, c.subject, c.predicate, c.object, c.confidence
FROM claims AS c
JOIN semantic_search(
  query => '为什么没有使用 SQLite',
  space => 'project.memora',
  limit => 20
) AS s ON s.id = c.id
WHERE c.status = 'active'
ORDER BY s.score DESC
LIMIT 10;
```

### 8.2 面向 Agent 的错误响应

CLI 应返回稳定 JSON，而不只是一段错误文本：

```json
{
  "ok": false,
  "error": {
    "code": "SQL_UNKNOWN_COLUMN",
    "message": "column 'contents' does not exist",
    "line": 1,
    "column": 8,
    "found": "contents",
    "expected": ["content"],
    "suggestion": "Replace 'contents' with 'content'",
    "retryable": true
  }
}
```

Agent 可以根据结构化错误自动修正，而无需解析不稳定的自然语言日志。

## 9. 候选持久化架构

### 9.1 逻辑分层

```text
Canonical Layer
  原始 Artifact、Node、Claim、Entity、Event、Relation

Semantic Layer
  类型、ontology、来源、有效时间、置信度、生命周期

Derived Layer
  全文索引、向量索引、图索引、摘要、聚类、热度

Context Layer
  按目标与 token budget 编译的 Context Pack
```

### 9.2 候选物理布局

研发期优先使用目录格式：

```text
personal.memora/
├── manifest
├── catalog
├── frames
├── objects
├── indexes
├── checkpoints
└── integrity
```

发布和迁移时可以打包为单文件：

```text
memora pack personal.memora personal.mora
memora unpack personal.mora
```

目录格式便于调试、修复和增量开发；单文件格式便于运输和分享。两者应共享同一逻辑格式。

### 9.3 Append-first，但支持精确更新

“精确更新”和“append-only”并不冲突：

- 用户看到的是对稳定对象的 `UPDATE`；
- 引擎内部追加 revision，不破坏旧版本；
- catalog 指向当前有效 revision；
- 压缩过程可合并历史，但必须遵守保留策略；
- 查询默认只返回当前状态，也能显式查询历史。

这同时提供易用性、审计、回滚与崩溃恢复。

## 10. 无上下文接管协议

数据库文件无法被大模型直接理解。可实现的目标是：任何安装了 Memora CLI 和通用 Skill 的 Agent，只需一个固定入口，就能在极少 token 内接管数据库。

建议命令：

```bash
memora inspect --level bootstrap --format json
```

返回内容包括：

- 数据库身份与用途；
- Space 和 Domain 地图；
- 类型与字段摘要；
- 数据规模与最近活动；
- 活跃任务和未解决冲突；
- 隐私和访问策略；
- 推荐查询入口；
- 索引可用性和兼容状态。

数据库内部需要持久化 Root Manifest，并为其建立固定、无需索引即可读取的位置。

## 11. 当前设计原则

1. **个人所有**：本地优先，导出自由，不被服务端绑定；
2. **默认隔离**：不同生活领域和项目不会被无意混入上下文；
3. **显式联动**：通过 Link、View 和授权进行跨 Space 查询；
4. **原始数据永存**：索引、摘要、Embedding 可重建；
5. **语义对象稳定**：知识不是一次性 chunk；
6. **修改保留历史**：精确更新对外简单，对内可审计；
7. **SQL 一等公民**：采用标准子集和严格 parser；
8. **CLI 一等公民**：稳定 JSON、错误码、stdin、事务和 dry-run；
9. **模型无关**：不把某个 Embedding 或 LLM 设为文件格式前提；
10. **渐进理解**：先 bootstrap，再按目标展开，避免全库注入上下文；
11. **查询有证据**：Context Pack 中每条结论可回到来源和 revision；
12. **智能是可选层**：核心数据库在没有 LLM 服务时仍能正确读写和查询。

## 12. 关键未决问题

以下问题需要后续逐项讨论和原型验证：

1. Claim 是否是唯一核心语义对象，还是采用更通用的 Frame 统一所有类型？
2. Artifact、Node、Claim 的稳定 ID 和定位锚点如何设计？
3. Space 是安全边界、文件边界，还是同一数据库内的逻辑边界？
4. 跨 Space Link 如何避免敏感信息通过摘要或关系泄露？
5. SQL 采用现有 parser/执行引擎，还是实现受控方言？
6. 标准 SQL 表如何映射 append-only revision 和当前状态？
7. 图查询采用 SQL/PGQ、递归 CTE、表值函数还是独立语法？
8. Context Pack 的 token 估算如何兼容不同模型 tokenizer？
9. Embedding 模型不同或不存在时，如何保证最小可用检索？
10. 单文件 capsule 是工作格式还是只读/交换格式？
11. 分支、合并和冲突治理应该进入 MVP 还是第二阶段？
12. 数据库是否允许 Agent 动态扩展 ontology，如何防止类型失控？

## 13. 建议的下一步

在决定具体 Go 库和磁盘格式之前，先完成以下设计：

1. 定义最小语义对象模型；
2. 用 10 至 20 个真实个人数据场景验证模型；
3. 设计 Space、Link、View 的隔离语义；
4. 定义第一版 SQL 子集和系统表；
5. 定义 `inspect/bootstrap` 响应；
6. 制作内存原型，验证精确更新、历史和混合检索；
7. 再根据访问模式决定底层文件格式与索引结构。

## 14. 调研说明

本轮调研时间为 2026-07-28。GitHub CLI 搜索因环境未登录而未能返回结果，随后通过 GitHub 网页索引检索并阅读上述项目公开页面。项目功能和活跃状态可能继续变化，后续进入详细竞品分析时应锁定 commit 或 release 版本复核。

## 15. 大对象、分页与增量修改

个人数据库除了保存 Claim、任务和记忆，还需要保存大型 Markdown、PDF、对话、代码和其他资料。系统不能因为一个文档中的局部内容增长，就重写整个对象或重新编号后续所有切块。

### 15.1 三种边界必须分离

传统 RAG 经常把存储边界和检索边界混合为固定 token chunk。Memora 应明确分离：

```text
逻辑对象
Document / Section / Paragraph / Claim / Task
                    │
                    ▼
物理页面
Page / Extent / Overflow Page / B+ Tree
                    │
                    ▼
派生检索视图
Posting / Passage / Summary / Graph / Vector(optional)
```

- `Page` 是磁盘读写、缓存和空间管理单位；
- `Node` 是内容结构与精确编辑单位；
- `Claim` 是知识维护单位；
- `Passage` 是检索时按需生成的派生窗口；
- 四者不能共享同一套易失效编号。

### 15.2 候选大对象结构

大型有序内容可以借鉴 B+ Tree、Rope、Piece Table 和 Copy-on-Write：

```text
Document Root
├── Internal Page
│   ├── Leaf Page
│   ├── Leaf Page
│   └── Leaf Page
└── Internal Page
    ├── Leaf Page
    └── Leaf Page
```

叶子页保存内容或内容 extent，内部页至少保存：

- 子页位置或标识；
- 子树字节数与字符数；
- 粗略 token 数；
- key/range 边界；
- 校验和；
- 可选的关键词 Bloom Filter；
- 可选的派生摘要引用。

当一个叶子页因插入内容超过容量时执行 page split：

```text
修改前：

Parent
└── Page A: 22KB

修改后：

Parent
├── Page A1: 11KB
└── Page A2: 11KB
```

一次局部编辑原则上只需要更新：

1. 被修改的叶子；
2. 分裂或合并产生的相邻叶子；
3. 从叶子到根的索引路径；
4. 依赖变化范围的派生索引；
5. 当前 revision 指针。

未受影响的文档区域不移动、不重新编号、不重新向量化。

### 15.3 稳定 Node 与位置锚点

不能只用全局 byte offset 引用内容，因为在文档前部插入数据后，后方偏移会变化。

建议引用形态：

```text
artifact_id: doc_xxx
node_id: node_abc
revision: 7
local_range: 120..460
```

或者使用稳定标记：

```text
anchor:
  node: node_abc
  start_marker: marker_01
  end_marker: marker_02
```

底层页面可以分裂，`node_id` 保持稳定。内容结构示例：

```text
Artifact: Memora 系统设计
├── Node: 存储引擎
│   ├── Node: 页面格式
│   └── Node: 页面分裂
└── Node: 查询引擎
    ├── Node: SQL Parser
    └── Node: 执行计划
```

### 15.4 检索窗口是可失效的物化视图

检索 Passage 不使用永久性的 `chunk_001` 编号，而应保存依赖关系：

```text
Passage
├── derived_from: [node_abc@7, node_def@3]
├── content_hash
├── tokenizer/version
├── index_kind
└── valid_until_revision
```

Node 更新后，引擎只使依赖它的 Passage 失效并局部重建。不同查询还可以动态采用不同粒度：

- 精确事实查询使用 Claim；
- 局部理解使用 Node；
- 跨段推理组合相邻 Node；
- 全局问题先查询层级摘要；
- 核验时返回 Artifact 的原文范围。

数据库中不存在唯一正确的固定 chunk size。

### 15.5 语义路由树

B+ Tree 内部节点除物理统计外，可以引用可重建的语义路由信息：

```text
Node Summary
├── topics: [storage, btree, page-split]
├── entities: [Memora]
├── time_range
├── token_estimate
└── child_summary_refs
```

查询可以先在高层定位相关子树，再读取叶子。但这些摘要只是派生索引：可以过期、可以重建，不能替代原文，也不能因摘要遗漏而永久隐藏底层数据。

## 16. 上下文、主题路由与记忆准入

真正的记忆不是保存对话中的每一句话，而是保存一段经历对已有状态和世界模型造成的有效变化。

### 16.1 四级记忆层次

可以把大模型上下文类比为有限且昂贵的数据库 Buffer Pool：

```text
L0 Model Context
   当前实际注入模型的内容；容量最小，成本最高

L1 Session Working Set
   当前话题、临时判断和候选记忆；尚未全部持久化

L2 Semantic Memory
   经筛选的事实、决策、任务、偏好、关系和经验

L3 Artifact Archive
   完整文档、原始对话、文件、媒体和历史版本
```

工作流：

```text
用户输入
   │
   ▼
识别当前主题与工作集
   │
   ▼
从 L2/L3 调取相关数据
   │
   ▼
编译为 L0 Context Pack
   │
   ▼
推理产生候选状态变化
   │
   ▼
筛选、合并、修订后进入 L2
```

### 16.2 一段对话可以包含多个项目

不能把整场对话简单绑定到一个项目。一轮对话甚至一句话都可能对应多个 Space 和 Domain。

建议引入临时 `Topic Span`：

```text
Conversation
├── Span 1 -> project.memora/storage
├── Span 2 -> project.website/deployment
├── Span 3 -> personal.preference
└── Span 4 -> ephemeral，不进入长期记忆
```

Session Working Set 可以维护当前焦点和候选主题：

```json
{
  "active_focus": "project.memora/storage",
  "candidate_topics": [
    {
      "space": "project.memora",
      "domain": "storage",
      "probability": 0.91
    }
  ]
}
```

主题判断属于可修正的推断，不能在低置信度下直接污染长期数据。发生话题切换时更新工作焦点，但不改变上一项目已经保存的状态。

### 16.3 记忆准入流程

候选信息写入长期语义层之前，需要经过：

```text
是否为新信息？
   ├── 已存在且一致 -> 强化、更新访问统计或忽略
   ├── 已存在但更精确 -> 创建 revision
   ├── 与已有内容矛盾 -> 标记 disputed，等待解决
   └── 全新信息
          ├── 是否具有长期价值？
          ├── 是否可能影响未来行为？
          ├── 是否属于明确决策或任务？
          ├── 是否只是临时状态或闲聊？
          ├── 是否有可靠来源？
          └── 是否含有敏感内容？
```

候选评分可以作为启发式，而不是绝对真理：

```text
admission_score =
    novelty
  * durability
  * future_utility
  * confidence
  * scope_relevance
  * safety_factor
```

建议默认策略：

| 信息类型 | 默认处理 |
| --- | --- |
| 已确认决策及理由 | 长期保存 |
| 稳定用户偏好 | 长期保存 |
| 活跃任务 | 保存并带生命周期 |
| 临时报错和命令输出 | 留在 Session 或 Artifact |
| 未确认设想 | 保存为 hypothesis，不得冒充 decision |
| 已否决方案及理由 | 保存为历史，避免重复研究 |
| 寒暄、重复和低价值内容 | 不进入长期语义层 |
| 密钥与敏感数据 | 拒绝、脱敏或进入加密隔离区 |

### 16.4 保存状态变化，而不是复制整段对话

一次讨论更适合沉淀为：

```text
Decision:
  第一版核心检索不依赖 Embedding API

Principle:
  Page、Node、Claim 和 Passage 必须分离

Requirement:
  系统要识别一段对话中的多个项目和主题

Hypothesis:
  B+ Tree + Rope/Piece Table + Copy-on-Write
  适合作为大型有序对象的候选存储结构
```

原始对话可以按用户保留策略进入 Artifact Archive，但长期工作记忆主要保存上述状态增量。

## 17. 不依赖向量 API 的检索架构

### 17.1 当前决策

第一版核心功能不依赖外部 Embedding API，也不要求本地下载大型向量模型。

原因：

- 外部 API 需要密钥、网络和费用；
- 私人数据可能离开本机；
- 不同模型的维度与语义空间不兼容；
- 模型可能下线或更改；
- 换模型后需要重建所有向量；
- 本地模型会增加下载体积、资源占用和部署复杂度；
- 这与单个 Go 可执行程序、离线可用和复制即走的目标冲突。

向量只允许作为可选、可删除、可重建的 Derived Index，不能成为数据库可读性的前提。

### 17.2 第一版核心检索能力

```text
Query
├── 倒排索引
├── BM25
├── 精确词组
├── 前缀查询
├── 中文字符 N-gram
├── 英文与符号 tokenizer
├── 字段权重
├── Space/Domain/类型过滤
├── 时间与状态过滤
├── Entity alias 扩展
├── 图关系遍历
└── 层级文档路由
```

候选综合评分：

```text
score =
    bm25
  + title_weight
  + exact_phrase_bonus
  + entity_match_bonus
  + relation_bonus
  + recency_weight
  + importance_weight
  + active_project_bonus
  + accepted_claim_bonus
  - stale_penalty
  - disputed_penalty
```

每个得分组成部分应尽量可以通过 `EXPLAIN` 返回，方便 Agent 理解为什么召回某条记录。

### 17.3 Agent 负责查询理解，数据库负责确定执行

调用者本身就是大模型，因此数据库无需再调用另一个模型理解问题。Agent 可以把自然语言改写成关键词、实体、时间和结构化过滤条件。

例如：

```sql
SELECT *
FROM SEARCH(
  '大文档 局部修改 B+Tree Page Split Rope Copy-on-Write',
  space => 'project.memora'
)
WHERE type IN ('decision', 'principle', 'document_node')
ORDER BY score DESC
LIMIT 20;
```

若结果不足，Agent 可以根据返回的术语和实体再次改写查询。模型负责理解和规划，数据库负责离线、确定性、快速且可解释地执行。

### 17.4 中文与混合技术文本

中文不能只依赖单一分词器。建议一次写入生成多类词项：

```text
原文：大文档采用页面分裂和 Copy-on-Write

中文词项：大文档、页面分裂
字符 N-gram：大文、文档、页面、面分、分裂
英文词项：copy、on、write
标准词项：copy-on-write、cow
实体词项：technology/Copy-on-Write
```

Entity alias 作为个人数据库的长期词典随数据一起迁移：

```text
B+ Tree = B Plus Tree = B加树
Copy-on-Write = COW = 写时复制
Agent = 智能体
```

### 17.5 同步索引与异步派生任务

写事务提交前必须同步完成：

1. 写入 Frame/Revision；
2. 更新当前版本指针；
3. 更新倒排索引；
4. 更新结构化字段索引；
5. 更新时间索引；
6. 更新关系索引；
7. 更新前缀和 N-gram 索引；
8. 原子提交。

提交成功的数据应立即可通过基础检索查到。

以下任务可以异步执行：

- 大文档层级摘要；
- 主题提取；
- Claim 候选抽取；
- 冲突检查；
- 记忆 consolidation；
- 冷数据压缩；
- 可选向量化。

数据库需要暴露派生状态：

```text
revision committed
├── fulltext ready
├── summary stale
├── topic map pending
└── contradiction check pending
```

### 17.6 倒排索引的增量结构

倒排索引可以借鉴 Lucene/Elasticsearch 的不可变 segment 和后台合并思想，与 append-first revision 模型结合：

```text
新 revision
    │
    ▼
实时增量 Segment
    │
    ├── term dictionary
    ├── posting lists
    ├── field lengths
    └── tombstones
    │
    ▼
后台 Merge / Compaction
```

- 新写入生成小型不可变 segment；
- 查询同时读取活跃 segment；
- 更新和删除先生成新 revision/tombstone；
- 后台合并小 segment 并按保留策略清理旧数据；
- 超长 posting list 自身也采用分页或分段结构。

### 17.7 向量扩展边界

未来可以保留通用 Derived Index 插件接口：

```text
Derived Index
├── fulltext
├── ngram
├── graph
├── summary
└── vector (optional)
```

如果未来确有需求，可以允许用户主动安装本地 provider，或由外部 Agent 提交已经计算好的向量。但必须遵守：

1. 不在后台隐式调用外部 API；
2. 没有向量时全部核心功能正常；
3. 向量索引可整体删除和重建；
4. 多个模型及维度可以并存；
5. 每条向量记录模型、维度、归一化方式、源内容哈希和索引版本；
6. 数据迁移可以选择不携带向量索引。

## 18. 当前形成的架构判断

截至 2026-07-29，讨论已形成以下方向性判断，但仍需原型验证后才能升级为稳定规格：

1. Memora 是个人所有、local-first 的 Agent 数据库，而不只是记忆插件；
2. 数据领域通过 Space 默认隔离，通过 Link 和 View 显式联动；
3. SQL 是一等查询和修改接口，完整语法必须由 parser/AST 验证，正则只处理外围格式；
4. 底层倾向采用统一 Frame，上层提供 Artifact、Node、Claim、Entity、Event、Task 和 Relation 等强类型对象；
5. 大型有序内容需要分页、树形寻址、稳定锚点和增量更新；
6. Page、Node、Claim、Passage 分别承担物理存储、内容结构、知识维护和检索职责；
7. Append-first revision 与用户可见的精确 `UPDATE` 并不冲突；
8. 记忆系统保存状态变化，不默认把每句话写入长期语义层；
9. 一段对话允许同时路由到多个项目、Space 和 Domain；
10. 第一版核心检索采用倒排索引、BM25、N-gram、结构化过滤、实体别名和图关系；
11. 第一版不依赖外部向量 API，向量只作为未来可选派生索引；
12. Agent 负责理解自然语言和生成查询计划，数据库负责确定性执行、权限检查、证据和结构化错误；
13. 基础索引同步更新并保证 read-your-writes，高成本语义派生任务允许异步完成；
14. 数据库通过 Root Manifest 和 bootstrap 协议支持陌生 Agent 无旧上下文接管。
