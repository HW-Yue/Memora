# MSQL、语义路由与上下文缓存协议

> 状态：架构讨论稿 0.1  
> 日期：2026-07-29  
> 项目暂定名：Memora  
> 相关文档：
> - [AI-Native 个人数据库：竞品调研与概念设计](./AI_NATIVE_PERSONAL_DATABASE_RESEARCH_2026-07-29.md)
> - [AI 自主治理、精确修改与 MVCC 设计](./AI_NATIVE_AUTONOMY_AND_MVCC_2026-07-29.md)
> - [Wiki 与 Obsidian Markdown 导出设计](./WIKI_EXPORT_DESIGN_2026-07-29.md)

## 1. 文档目的

本文定义 Memora 面向 Agent 的标准操作语言、语义路由索引和上下文缓存原则，重点回答：

1. 陌生 Agent 如何发现有哪些数据库；
2. Agent 如何理解数据库、表、字段和索引的用途；
3. Agent 如何在很小上下文内选择下一跳；
4. 为什么实际数据必须通过 SQL 获取和修改；
5. 所有操作如何通过版本化、标准化语言完成；
6. 动态索引结果是否应保留在模型上下文中；
7. Skill 应包含哪些语法、流程和错误恢复规则。

## 2. 当前核心决策

1. Agent 不得直接读取或修改 Memora 物理文件、Page、Segment 或内部索引；
2. 所有数据库交互通过一套版本化标准语言完成；
3. 语言暂称 `MSQL`（Memora SQL）；
4. MSQL 以标准 SQL 子集为主体，并增加 Agent 发现和语义路由语句；
5. CLI 只是 MSQL 的传输、执行和结构化响应入口；
6. 语义路由只告诉 Agent“这里是什么、应该去哪、应该查哪些表”，不直接返回业务数据；
7. 实际数据只能通过 `SELECT` 或标准查询表函数获取；
8. 数据创建和修改通过 SQL DDL/DML 与事务完成；
9. AI 自主决定业务数据库、表和字段；引擎保留不可绕过的系统字段；
10. Skill 保存稳定协议，不保存某个数据库的动态索引内容；
11. 动态路由结果默认是短期查询状态，不永久留在模型上下文；
12. 当前焦点只保留一个紧凑 Route Frame，以发挥缓存价值而不污染后续对话。
13. 数据库可以确定性导出为 Obsidian Markdown Wiki，但导出目录不是数据库真相源。

## 3. 控制面与数据面

Memora 的 Agent 接口分为两个逻辑平面：

```text
控制面：Discover / Describe / Route
回答：
- 有哪些数据库？
- 这个数据库做什么？
- 有哪些表和字段？
- 当前问题属于哪个分支？
- 下一步应查询哪些表？

数据面：SQL Query / Mutation
负责：
- 精确读取数据；
- 全文检索；
- JOIN 与关系查询；
- 创建和修改数据库、表、字段；
- 创建、更新和删除记录；
- 事务、MVCC 和历史查询。
```

控制面也使用 MSQL 语句，不另造一套自然语言 CLI。

## 4. Agent 标准状态机

Agent 第一次接入或切换主题时遵循：

```text
CONNECT
   │
   ▼
SHOW SERVER
   │
   ▼
SHOW DATABASES
   │
   ▼
DESCRIBE DATABASE ... COMPACT
   │
   ▼
SHOW ROUTES / OPEN ROUTE
   │
   ▼
DESCRIBE TABLE ... COMPACT
   │
   ▼
SELECT / SEARCH
   │
   ▼
必要时 BEGIN / INSERT / UPDATE / DELETE / COMMIT
```

以下情况允许复用已缓存的 Route Frame，跳过部分发现步骤：

- 对话仍在同一数据库和同一语义分支；
- Route revision 和 Schema version 没有变化；
- 上次查询没有返回 cache stale；
- 当前问题没有明显切换到另一个项目或个人领域。

## 5. MSQL 语言边界

### 5.1 SQL 主体

第一版候选支持：

```text
DDL
- CREATE DATABASE
- ALTER DATABASE
- CREATE TABLE
- ALTER TABLE
- CREATE INDEX
- DROP（受 Policy 限制）

DML
- SELECT
- INSERT
- UPDATE
- DELETE（默认逻辑删除）

QUERY
- WHERE
- JOIN
- GROUP BY
- ORDER BY
- LIMIT
- Common Table Expression（是否进入 MVP 待定）

TRANSACTION
- BEGIN
- COMMIT
- ROLLBACK

DISCOVERY
- SHOW SERVER
- SHOW DATABASES
- DESCRIBE DATABASE
- SHOW TABLES
- DESCRIBE TABLE
- SHOW ROUTES
- OPEN ROUTE
- SHOW RELATIONS
- SHOW HISTORY
```

### 5.2 不使用自然语言修改数据库

以下命令形态不作为正式协议：

```text
memora remember "..."
memora save-this
memora update-the-old-note
```

它们的含义不够精确，无法稳定执行并发前置条件、影响分析和错误恢复。

Agent 必须把自然语言意图编译为 MSQL。

### 5.3 CLI 只是传输层

候选入口：

```bash
memora exec --format json
```

SQL 从 stdin 传入。长文本与复杂值使用参数绑定，不直接拼接到 SQL 字符串。

## 6. 协议握手

Agent 首次接入执行：

```sql
SHOW SERVER;
```

候选响应：

```json
{
  "ok": true,
  "data": {
    "engine": "memora",
    "engine_version": "0.1.0",
    "protocol": "msql",
    "protocol_version": 1,
    "format_version": 1,
    "capabilities": [
      "sql",
      "semantic_routes",
      "fulltext_search",
      "mvcc",
      "history"
    ]
  },
  "meta": {
    "snapshot": 1843
  }
}
```

Agent 必须根据协议版本选择语法，不能假设所有 Memora 版本支持相同能力。

## 7. 数据库发现

### 7.1 查看数据库

```sql
SHOW DATABASES;
```

结果不只返回名称，还包含严格限制长度的一句话用途：

```text
project_memora      Memora 的产品、架构、决策和开发状态
knowledge_database AI 已吸收的数据库技术知识
personal           用户偏好、目标和长期个人状态
```

每个数据库必须具有：

- 稳定内部 database ID；
- AI 可读名称；
- purpose；
- scope；
- Schema version；
- Route revision；
- 最近更新时间；
- Policy 引用。

### 7.2 了解数据库

```sql
DESCRIBE DATABASE project_memora COMPACT;
```

紧凑输出只包含：

- purpose；
- 主要表及一句话说明；
- 顶层 Route；
- Schema version；
- 当前活动焦点；
- 必要 Policy 提示。

完整设计历史只在显式执行 `FULL` 时返回：

```sql
DESCRIBE DATABASE project_memora FULL;
```

## 8. AI 自主 Schema

业务数据库、表和字段由 AI 根据当前领域自主设计。例如：

```sql
CREATE TABLE project_memora.design_decisions (
    title TEXT NOT NULL
        DESCRIPTION '便于检索的决策名称',

    conclusion TEXT NOT NULL
        DESCRIPTION '当前有效结论',

    rationale TEXT
        DESCRIPTION '形成结论的原因',

    status ENUM(
        'proposed',
        'accepted',
        'rejected',
        'superseded'
    ) NOT NULL DEFAULT 'proposed'
        DESCRIPTION '决策当前状态'
)
DESCRIPTION '保存项目设计决策及其理由';
```

引擎自动维护系统信封：

```text
_row_id
_database_id
_table_id
_revision
_created_txn
_updated_txn
_valid_from
_valid_to
_previous_version
_actor
_source
_checksum
```

AI 不能删除、重定义或伪造这些系统字段。

Schema 说明本身是版本化数据。AI 后续可以通过标准 `ALTER`、rename、migration 和兼容视图修正自己的早期设计。

## 9. 表与字段发现

```sql
SHOW TABLES FROM project_memora;
```

```sql
DESCRIBE TABLE project_memora.design_decisions COMPACT;
```

紧凑表描述示例：

```text
TABLE design_decisions
PURPOSE 保存项目设计决策及其理由

FIELDS
id          ID      stable
title       TEXT    决策名称
conclusion  TEXT    当前结论
rationale   TEXT    决策原因
status      ENUM    proposed|accepted|rejected|superseded
revision    UINT    对象修订版本

INDEXES
PRIMARY(id)
FULLTEXT(title, conclusion, rationale)
BTREE(status)
```

字段说明必须告诉 Agent“字段语义是什么”，而不只是数据类型。

## 10. 语义路由索引

### 10.1 定义

语义路由是给 Agent 阅读的紧凑认知索引。它不暴露物理 Page、Segment、offset 或内部 Object ID。

Agent 看到：

```text
/project/memora/indexing/routing
```

引擎内部可以使用任意稳定 ID 和物理位置。

### 10.2 Router Page

每个 Router Page 只包含：

- 当前路径；
- 一句话 purpose；
- 少量子分支；
- 每个子分支一句话范围；
- 相关表；
- 可选的查询提示；
- Route revision。

候选预算：

```text
目标：300～500 个中文字
硬上限：800 字
子分支：最多 8～12 个
```

Router Page 不能承载完整知识内容。

### 10.3 路由语句

```sql
SHOW ROUTES
FROM DATABASE project_memora
AT '/';
```

继续导航：

```sql
OPEN ROUTE
FROM DATABASE project_memora
AT '/indexing';
```

候选紧凑输出：

```text
ROUTE /indexing
PURPOSE Memora 的查询、索引和上下文装载

@lexical    BM25、N-gram、Posting 和 Segment
@routing    AI 可读的短索引与下一跳选择
@relations  结构关系与语义关系
@ranking    多路候选合并和排序

TABLES design_topics, design_decisions, relations
```

### 10.4 短句柄

同一次导航响应可以返回临时短句柄：

```text
@1 lexical
@2 routing
@3 relations
@4 ranking
```

Agent 可以在协议允许的情况下使用句柄继续：

```sql
OPEN ROUTE @2 USING CURSOR 'nav_72';
```

短句柄只在导航会话内有效，不能成为持久关系或数据库主键。

## 11. 路由不是数据读取

`SHOW ROUTES` 和 `OPEN ROUTE` 只能返回导航元数据。它们不能返回完整业务行，也不能绕过表级权限和查询限制。

选择逻辑位置后，Agent 必须通过 SQL 读取：

```sql
SELECT
    id,
    title,
    body,
    revision
FROM project_memora.design_topics
WHERE route = '/indexing/routing'
  AND status = 'current'
ORDER BY updated_at DESC
LIMIT 5;
```

全文检索作为表函数参与标准 SQL：

```sql
SELECT
    d.id,
    d.title,
    d.body,
    d.revision,
    s.score
FROM SEARCH(
    database => 'project_memora',
    query => '语义索引 SQL 查询 跳转',
    limit => 20
) AS s
JOIN project_memora.design_topics AS d
  ON d.id = s.object_id
WHERE d.route = '/indexing/routing'
ORDER BY s.score DESC
LIMIT 5;
```

## 12. 路由、倒排索引和关系图

不能只依赖 AI 沿目录逐层选择，否则一次路由判断错误会造成漏召回。

完整检索：

```text
用户问题
   │
   ├── Semantic Router
   │     选择最可能的数据库和逻辑路径
   │
   ├── Inverted Index
   │     全局 BM25/N-gram 召回，防止目录漏检
   │
   └── Relation Graph
         扩展候选的结构和语义邻居
   │
   ▼
合并、去重、排序
   │
   ▼
SELECT 最终语义记录
```

路由回答“应该去哪”，倒排索引回答“哪些记录命中”，关系图回答“还应查看哪些关联模块”。

## 13. 语义记录预算

数据库保存的是 AI 已经吸收和组织的认知模块，而不是机械文档 chunk。

候选写作预算：

```text
目标：约 800 个中文字
软下限：约 300 字
软上限：约 1200 字
要求：单一主题、独立完整、可精确修改
```

800 字是语义预算，不是物理 Page 大小或机械切分规则。

## 14. 资料吸收协议

Memora 不持久化完整 PDF、Markdown、图片、音频或原始大文档。外部资料只作为临时输入：

```text
外部资料
   │
   ▼
Agent 有界、分阶段阅读
   │
   ├── 发现或创建数据库与表
   ├── 提取完整语义模块
   ├── 更新已有记录
   ├── 建立关系
   └── 检查覆盖和冲突
   │
   ▼
通过 MSQL 提交认知状态
   │
   ▼
清除 Memora 临时读取缓存
```

临时读取窗口不是持久化 chunk，也不是数据库权威对象。

Memora 可以保存极小的 Source Receipt：标题、作者、版本、来源 URI 提示、内容哈希、吸收时间和影响的对象清单，但不保存正文。

Memora 不得隐式删除用户磁盘上的原始文件。它只是不导入原文并清理自身临时副本。

## 15. 写入和参数绑定

长文本不能手工拼接到 SQL 中。Agent 使用参数绑定：

```sql
INSERT INTO project_memora.design_topics (
    title,
    body,
    route
) VALUES (
    :title,
    :body,
    :route
);
```

参数通过标准 CLI 参数文件或 stdin 伴随协议传递。参数绑定负责类型、转义和长度检查，SQL 仍是唯一数据操作语言。

更新必须携带 revision 前置条件：

```sql
UPDATE project_memora.design_topics
SET body = :body
WHERE id = :id
  AND revision = :expected_revision;
```

影响零行时，引擎需要区分 `OBJECT_NOT_FOUND` 和 `REVISION_CONFLICT`。

## 16. 事务与物理自动化

AI 只负责逻辑 MSQL。引擎自动处理：

- Parser 与 AST；
- 执行计划；
- Page 分裂和合并；
- B+ Tree 与倒排索引；
- MVCC；
- Undo；
- WAL/Redo；
- 锁与并发冲突；
- Segment merge；
- 崩溃恢复；
- 物理空间回收。

长时间 Agent 推理不能持有事务。Agent 先发现、读取和生成计划，最后开启短事务并通过 revision 前置条件重新验证。

## 17. 索引上下文是否保留

### 17.1 问题

如果所有 `SHOW ROUTES`、`OPEN ROUTE` 和 `DESCRIBE` 结果都留在模型上下文中：

- 会持续消耗 token；
- 已经离开的话题仍会影响后续推理；
- 旧 Schema 和旧 Route 可能误导后续查询；
- 多个数据库的索引会互相污染；
- Agent 可能因为“看见过”旧路径而跳过重新验证。

如果完全丢弃：

- 同一话题的下一次查询需要重复导航；
- 增加工具调用和延迟；
- Agent 无法利用当前工作集的局部性。

因此不能选择“全部永久保留”或“每次全部丢弃”。

### 17.2 三层缓存模型

```text
L0 Model Route Frame
  模型上下文中保留的极短当前导航状态

L1 Navigation Session Cache
  CLI/Agent 会话侧保存完整 Router 结果和短句柄

L2 Engine Cache
  数据库引擎保存 Page、Schema、posting 和执行计划缓存
```

只有 L0 消耗大模型上下文。

### 17.3 L0 Route Frame

模型上下文只保留：

```text
db: project_memora
route: /indexing/routing
schema: 12
route_rev: 31
cursor: nav_72
tables: design_topics, design_decisions, relations
focus: 语义路由与 SQL 协作
```

目标控制在约 50～150 tokens。它不是完整 Router Page，只是当前工作目录。

同一主题继续时，Agent 可以复用它；主题明显切换时必须替换或清除。

### 17.4 L1 Navigation Session Cache

完整 Router Page、候选分支和短句柄存放在 Agent/CLI 的会话缓存，而不是 system prompt：

```text
cursor: nav_72
database_id: db_01
route_id: route_19
schema_version: 12
route_revision: 31
cached_routes: [...]
expires_at: ...
```

Agent 通过 cursor 复用结果，不需要把完整索引重新注入模型上下文。

L1 缓存必须有：

- TTL；
- 最大条目数；
- database/route revision；
- LRU 或主题切换淘汰；
- 显式 invalidate；
- 对敏感 Space 更短的生命周期。

### 17.5 L2 引擎缓存

引擎可以缓存：

- B+ Tree Page；
- Schema catalog；
- Router 编译结果；
- 倒排 posting；
- SQL 执行计划；
- 热门对象版本。

这些缓存完全不进入模型上下文。缓存失效和空间淘汰由程序自动处理。

### 17.6 不进入 system prompt

动态数据库索引绝对不应写入 Skill 的长期 system context。

Skill 只包含：

- MSQL 语法；
- 标准状态机；
- 输出协议；
- 错误恢复；
- 上下文预算；
- 禁止行为。

具体有哪些数据库、表、字段和 Route 必须在运行时发现。

### 17.7 缓存失效

Route Frame 和 Navigation Cache 至少绑定：

```text
database_id
schema_version
route_id
route_revision
```

如果 Schema 或 Route 已变化，引擎返回：

```json
{
  "ok": false,
  "error": {
    "code": "NAVIGATION_CACHE_STALE",
    "expected_schema_version": 12,
    "actual_schema_version": 13,
    "retryable": true,
    "recovery": {
      "statement": "DESCRIBE DATABASE project_memora COMPACT"
    }
  }
}
```

Agent 重新发现，不允许继续依赖旧字段和旧路径。

### 17.8 不长期占用 MVCC 快照

导航缓存不能长期固定一个数据库快照，否则会阻碍旧版本回收和 compaction。

缓存保存的是逻辑路径和版本指纹，不是一个长期活跃事务。每次实际 SQL 查询获取新短快照，并验证 Schema/Route 版本。

## 18. 查询结束后的上下文清理

一次查询完成后：

```text
完整 Router 输出      -> 从模型工作上下文淘汰
DESCRIBE 详细结果      -> 从模型工作上下文淘汰
SQL 原始候选列表       -> 从模型工作上下文淘汰
当前 Route Frame       -> 同主题时保留
最终有效知识           -> 按任务需要保留
长期状态变化           -> 通过 MSQL 写入数据库
```

如果对话已经切换主题：

```text
旧 Route Frame -> 清除
旧 cursor      -> 可留在 L1，等待 TTL/LRU 淘汰
新主题          -> 重新 SHOW DATABASES 或 ROUTE
```

## 19. 上下文预算参数

控制面语句应支持输出预算：

```sql
OPEN ROUTE
FROM DATABASE project_memora
AT '/indexing'
WITH (
    MAX_CHARS = 600,
    MAX_ROUTES = 6,
    FORMAT = 'COMPACT'
);
```

响应返回：

```json
{
  "meta": {
    "used_chars": 542,
    "has_more": true,
    "schema_version": 12,
    "route_revision": 31,
    "cursor": "nav_72"
  }
}
```

数据查询仍必须显式 `LIMIT`。引擎可以通过 Policy 设置默认最大行数和最大输出字符数。

## 20. Skill 规范

Skill 必须完整描述以下内容。

### 20.1 语法

- MSQL 版本；
- EBNF；
- 数据类型；
- 标准 SQL 子集；
- Memora 扩展语句；
- 参数绑定；
- 事务和 MVCC 语义；
- 返回格式。

示例 EBNF：

```ebnf
show_databases =
    "SHOW" "DATABASES" ";" ;

describe_database =
    "DESCRIBE" "DATABASE" identifier
    [ "COMPACT" | "FULL" ] ";" ;

show_routes =
    "SHOW" "ROUTES"
    "FROM" "DATABASE" identifier
    "AT" string_literal
    [ route_options ] ";" ;

open_route =
    "OPEN" "ROUTE"
    ( "FROM" "DATABASE" identifier "AT" string_literal
    | route_handle "USING" "CURSOR" string_literal )
    [ route_options ] ";" ;
```

### 20.2 行为流程

- 何时重新发现数据库；
- 何时复用 Route Frame；
- 何时执行 `DESCRIBE COMPACT`；
- 查询必须包含的限制；
- 写入前的 revision 检查；
- 事务提交后的验证；
- 主题切换时的缓存清理。

### 20.3 错误恢复

| 错误码 | Agent 行为 |
| --- | --- |
| `SQL_SYNTAX_ERROR` | 根据位置和 expected tokens 修正 |
| `UNKNOWN_DATABASE` | 重新执行 `SHOW DATABASES` |
| `UNKNOWN_TABLE` | 执行 `SHOW TABLES` |
| `UNKNOWN_FIELD` | 执行 `DESCRIBE TABLE ... COMPACT` |
| `ROUTE_NOT_FOUND` | 打开父 Route 重新导航 |
| `NAVIGATION_CACHE_STALE` | 重新读取 Database/Route |
| `REVISION_CONFLICT` | 重读对象并重新生成修改 |
| `SCHEMA_VERSION_CONFLICT` | 重新读取 Schema |
| `RESULT_LIMIT_REQUIRED` | 添加 `LIMIT` 和输出预算 |
| `POLICY_DENIED` | 不盲目重试，向用户说明 |

### 20.4 禁止行为

- 直接读取物理数据库和索引文件；
- 猜测内部 ID、Page 或 offset；
- 未发现 Schema 就猜表名和字段；
- 将动态数据库索引永久写入 system prompt；
- 在 SQL 中拼接未转义长文本；
- 长时间持有事务等待模型推理；
- 遇到 revision 冲突后强制覆盖；
- 一次性请求整库或无限制结果；
- 把外部文档机械切块后写入数据库。

## 21. Parser 与 Skill 的职责边界

```text
Skill
  帮助 Agent 生成正确 MSQL、遵循发现流程和管理上下文

Parser / Binder / Planner
  验证语法、Schema、类型和能力

Policy / MVCC / Storage Engine
  强制权限、一致性、并发、历史和物理安全
```

Skill 不是安全边界。即使 Agent 未加载或违反 Skill，数据库也不能接受非法操作。

## 22. 待验证问题

1. `SHOW`/`DESCRIBE` 扩展采用 MySQL 风格还是更接近标准 `INFORMATION_SCHEMA`？
2. Router Page 是否需要磁盘上的可读 sidecar，还是只通过 MSQL 动态生成？
3. 短句柄和 cursor 是否属于 MSQL 标准协议？
4. `SEARCH()` 采用表函数、`MATCH` 语法还是两者兼容？
5. Compact 输出使用专用 MIDX 文本、表格还是严格 JSON？
6. 输出预算按字符、token 估算还是两者同时支持？
7. Route Frame 由 Agent 维护还是 CLI 自动返回可复用状态块？
8. 不同 Agent 的 Navigation Cache 是否共享？
9. 缓存中的敏感路径是否需要加密或完全禁用落盘？
10. 多数据库联合查询如何显式授权并控制上下文预算？

## 23. 当前结论

Memora 的查询不是“让 AI 直接翻索引文件”，而是：

```text
通过 MSQL 发现数据库
        │
        ▼
通过 MSQL 读取短语义路由
        │
        ▼
通过 MSQL 描述相关表和字段
        │
        ▼
通过标准 SQL 获取或修改数据
```

动态索引不永久进入 system context。模型上下文只保留当前 Route Frame；完整导航缓存由 CLI/Agent 会话保存；物理索引缓存由引擎维护。这样同时获得查询局部性和上下文洁净度。

## 24. Wiki 导出

Memora 的语义记录和 Route 可以从一致 MVCC 快照编译为 Markdown Wiki：

```text
Semantic Record -> Markdown Page
Router Page      -> Index.md / MOC
Relation         -> [[Wikilink]]
Database         -> 一级目录
Route            -> Wiki 子目录
```

第一阶段是从数据库到 Wiki 的单向、确定性导出。导出器不调用 LLM，也不把 Obsidian 目录作为数据库的第二份真相源。详细设计见 [Wiki 与 Obsidian Markdown 导出设计](./WIKI_EXPORT_DESIGN_2026-07-29.md)。
