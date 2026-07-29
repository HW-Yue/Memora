# Wiki 与 Obsidian Markdown 导出设计

> 状态：架构讨论稿 0.1  
> 日期：2026-07-29  
> 项目暂定名：Memora  
> 相关文档：
> - [MSQL、语义路由与上下文缓存协议](./MSQL_SEMANTIC_ROUTING_AND_CONTEXT_2026-07-29.md)
> - [AI 自主治理、精确修改与 MVCC 设计](./AI_NATIVE_AUTONOMY_AND_MVCC_2026-07-29.md)

## 1. 产品目标

Memora 内部是由 AI 自主建模和维护的个人数据库，但它不应成为只有程序能读取的黑盒。数据库应能导出为完整 Markdown Wiki：

- 可以直接放入 Obsidian；
- 每个语义模块成为一个短小、完整的 Markdown 页面；
- 页面之间使用 `[[Wikilink]]` 跳转；
- Router Page 编译为 Wiki 的目录页/MOC（Map of Content）；
- AI 和人都能脱离 Memora CLI 阅读导出结果；
- 导出内容保持良好层级、标题和关系，而不是数据库行的机械转储。

核心定义：

> Memora 数据库是可修改、可查询、带 MVCC 的权威认知状态；Markdown Wiki 是从某个数据库快照确定性编译出的可读投影。

## 2. 为什么 Wiki 适合 Memora

Memora 的语义记录目标约 800 个中文字，要求单一主题、独立完整、可以精确修改。这天然接近 Wiki 页面，而不是 RAG chunk。

```text
Memora Semantic Record
├── title
├── body
├── route
├── table/type
├── relations
├── aliases
└── revision
          │
          ▼ Wiki Compiler
Markdown Page
├── YAML frontmatter
├── 标题
├── 正文
├── 相关页面 [[...]]
└── 必要的来源/状态
```

Wiki 为两个使用者提供不同价值：

- 对 AI：短页面、明确标题、层级目录、显式链接和有限上下文；
- 对人：可以浏览、搜索、建立图谱、长期备份和手工审阅。

## 3. 权威数据与导出投影

第一阶段采用严格单向关系：

```text
Memora Database
   source of truth
        │
        ▼
Wiki Exporter
        │
        ▼
Obsidian Vault / Markdown Folder
   derived snapshot
```

Markdown 导出目录不是数据库的第二份真相源。第一阶段不允许：

- 直接编辑 Markdown 后自动覆盖数据库；
- 通过文件时间判断谁是新版本；
- 同时在数据库和 Obsidian 修改后静默合并；
- 从缺少 ID 和 revision 的普通 Markdown 猜测更新对象。

双向同步会引入 identity、rename、delete、relation、Schema、MVCC 和冲突合并问题，应在单向导出稳定后单独设计。

## 4. 导出目录布局

候选目录：

```text
Memora Wiki/
├── Home.md
├── _memora/
│   ├── export.json
│   ├── databases.md
│   └── unresolved-links.md
├── Projects/
│   └── Memora/
│       ├── Index.md
│       ├── Product/
│       │   └── Index.md
│       ├── Storage/
│       │   ├── Index.md
│       │   ├── MVCC.md
│       │   ├── Undo Log.md
│       │   └── WAL.md
│       ├── Indexing/
│       │   ├── Index.md
│       │   ├── Semantic Routing.md
│       │   └── Inverted Index.md
│       └── SQL/
│           └── Index.md
├── Knowledge/
│   └── Database/
│       └── Index.md
└── Personal/
    └── Index.md
```

建议映射：

- Database → 一级目录；
- Route → 子目录；
- Router Page → 目录中的 `Index.md`；
- Semantic Record → 独立 Markdown 页面；
- Relation → `[[Wikilink]]`；
- Alias → Obsidian frontmatter aliases；
- Database Root Router → Database 的 `Index.md`；
- 全库 Root Router → `Home.md`。

具体是否按 Table 建目录不应硬编码。Table 是数据模型，Route 是阅读导航；Wiki 目录应优先反映 Route，而不是数据库内部表名。

## 5. Semantic Record 页面

候选输出：

```markdown
---
memora_id: topic_01K0...
database: project_memora
table: design_topics
route: /storage/transactions
revision: 7
status: current
aliases:
  - 多版本并发控制
  - Multi-Version Concurrency Control
exported_at: 2026-07-29T15:30:00+08:00
---

# MVCC

MVCC 通过为同一逻辑对象保留多个不可变版本，使读取事务能够在稳定快照中工作……

## 相关内容

- 上位主题：[[Storage/Index|存储引擎]]
- 依赖：[[Undo Log]]
- 相关：[[WAL]]
- 应用于：[[Concurrent Agent Writes|多 Agent 并发写入]]
```

正文来自数据库当前快照中的语义记录，不拼入整段历史、原始资料或完整查询日志。

## 6. Router Page 编译为 MOC

Memora 的 Router Page 只负责告诉 Agent 下一步去哪。导出到 Wiki 后，它自然成为目录页或 MOC：

```markdown
# Indexing

Memora 的查询、索引、语义导航和上下文装载设计。

## 分支

- [[Lexical/Index|词法检索]] — BM25、N-gram、Posting 和 Segment
- [[Semantic Routing]] — AI 可读的短索引与下一跳选择
- [[Relations/Index|关系索引]] — 结构关系与语义关系
- [[Ranking]] — 多路候选合并和排序

## 当前状态

- [[MSQL and Semantic Routing|MSQL 与语义路由的协作]]
```

Router 仍应遵守紧凑预算。Wiki 页面可以加入少量人类友好标题，但不能扩展成重复所有子页面内容的大文档。

## 7. Wikilink 生成

### 7.1 关系是结构化数据

数据库中保存：

```text
source_id
relation_type
target_id
description
revision
status
```

导出器根据稳定 ID 解析目标文件路径，再生成：

```markdown
[[Storage/MVCC|MVCC]]
```

不能通过标题字符串猜测链接目标。

### 7.2 链接显示

建议根据 relation type 分组：

```markdown
## 相关内容

### 组成
- [[Page Management]]
- [[Version Store]]

### 依赖
- [[WAL]]
- [[Undo Log]]

### 对比
- [[Lock-Based Concurrency Control|基于锁的并发控制]]
```

### 7.3 反向链接

Obsidian 可以动态显示 backlinks，因此默认不需要把所有反向关系重复写进正文。

但如果关系本身具有方向性语义，例如 `used_by`、`contradicted_by`，导出器可以选择显式输出。该策略应可配置，避免页面底部链接爆炸。

## 8. 标题冲突与稳定身份

不同数据库或 Route 可能存在同名页面：

```text
Project A / Architecture
Project B / Architecture
```

内部永远通过 `memora_id` 建立关系，导出时优先使用相对完整路径：

```markdown
[[Projects/Project A/Architecture|Architecture]]
```

文件名生成规则应确定且可复现：

1. 使用规范化 title；
2. 过滤操作系统非法字符；
3. 保留 Unicode，不强制全部转为英文 slug；
4. 同一目录发生冲突时加入简短稳定后缀；
5. `memora_id` 始终写入 frontmatter；
6. 重命名后由导出 manifest 识别旧路径。

## 9. Rename、Move 与 Redirect

对象 title 或 Route 改变时，Markdown 文件路径可能变化。

第一版候选策略：

- 新快照在新路径生成页面；
- 导出 manifest 记录 `memora_id → path`；
- 删除旧导出路径前检查它确实属于上一版导出；
- 可选生成短期 redirect 页面；
- 所有 Wikilink 依据当前 manifest 重新编译。

Redirect 示例：

```markdown
---
memora_redirect: true
target: Projects/Memora/Storage/MVCC
---

此页面已移动到 [[Projects/Memora/Storage/MVCC]]。
```

是否保留 redirect 由导出策略决定。

## 10. 导出命令与 MSQL

导出属于标准化操作，不应通过未定义的文件复制脚本完成。

候选 MSQL：

```sql
EXPORT WIKI
FROM DATABASE project_memora
TO :output_path
WITH (
    FORMAT = 'OBSIDIAN',
    SNAPSHOT = CURRENT,
    INCLUDE_HISTORY = FALSE,
    INCLUDE_SCHEMA = TRUE,
    LINK_STYLE = 'WIKILINK'
);
```

导出全部允许的数据库：

```sql
EXPORT WIKI
FROM ALL DATABASES
TO :output_path
WITH (
    FORMAT = 'OBSIDIAN',
    SNAPSHOT = CURRENT
);
```

CLI 仍只是 MSQL 执行入口。输出目录通过参数绑定传递并接受 Policy 检查。

## 11. 快照一致性

一次 Wiki 导出必须来自一个一致的 MVCC 快照：

```text
BEGIN EXPORT at commit_seq 1843
├── 读取 Schema 12
├── 读取 Route revision 31
├── 读取所有可见语义记录
├── 读取可见关系
├── 编译页面与链接
└── 发布 export snapshot 1843
```

不能出现：

- 页面来自 revision 7，但链接来自 revision 6；
- 新 Schema 和旧数据混合；
- 导出过程中某些页面突然消失；
- 同一个对象在两个路径各输出一次且无说明。

## 12. 确定性导出

相同数据库快照与相同导出配置，应生成逻辑上一致的 Wiki：

- 路径排序稳定；
- frontmatter 字段顺序稳定；
- 链接排序稳定；
- 换行和编码稳定；
- 不在导出过程中调用 LLM 重新撰写内容；
- 不因操作系统目录遍历顺序产生差异。

AI 的写作和建模发生在数据库写入阶段。Wiki Exporter 只是确定性编译器。

## 13. 增量导出

导出 manifest：

```json
{
  "format": "memora-wiki-export",
  "version": 1,
  "snapshot": 1843,
  "database_schema_versions": {
    "project_memora": 12
  },
  "objects": {
    "topic_01K0...": {
      "revision": 7,
      "path": "Projects/Memora/Storage/MVCC.md",
      "content_hash": "sha256:..."
    }
  }
}
```

下一次导出只需要重写：

- 新对象；
- revision 变化的对象；
- 路径变化的对象；
- 链接目标变化导致内容变化的对象；
- Router revision 变化的目录页。

未变化页面保持不动，减少 Obsidian 文件监听、Git diff 和同步流量。

## 14. 历史导出

默认只导出当前有效状态。MVCC 全部历史如果直接展开，会让 Wiki 充满重复页面。

可选模式：

```sql
EXPORT WIKI
FROM DATABASE project_memora
AS OF COMMIT 1800
TO :output_path;
```

或者显式生成历史附录：

```text
_history/
└── object-id/
    ├── revision-1.md
    └── revision-2.md
```

历史导出默认关闭。

## 15. Schema 与表说明导出

为了让脱离 Memora 的 AI 理解 Wiki，可以可选导出紧凑 Schema 页面：

```text
_memora/schema/
├── project_memora.md
├── design_topics.md
└── design_decisions.md
```

内容包括：

- Database purpose；
- Table purpose；
- 字段语义；
- Route 映射；
- 必要约束；
- Schema version。

不导出物理 Page、WAL、锁和内部索引信息。

## 16. 人类编辑的边界

导出 Wiki 首先用于阅读、搜索、分享、备份和审阅。人类可以在副本中自由做个人标注，但这些修改不会自动进入数据库。

未来若支持回写，应采用显式操作：

```sql
IMPORT WIKI CHANGES
FROM :path
AGAINST EXPORT SNAPSHOT 1843
WITH (
    MODE = 'PLAN_ONLY'
);
```

系统先生成 mutation plan、对象级 diff 和 revision 冲突，不直接覆盖数据库。该能力不进入第一阶段。

## 17. 与上下文检索的关系

运行中的 Memora Agent 仍应使用 MSQL、Router 和 SQL 查询，不应先把整个 Obsidian Vault 读回上下文。

Wiki 导出主要用于：

- 人类阅读；
- Obsidian 图谱和链接导航；
- 脱离 Memora 的静态 AI 阅读；
- Git/文件级备份；
- 公开或分享部分知识；
- 数据库可解释性和可迁移性验证。

## 18. 安全与隐私

导出会把数据库内容转换成普通文件，因此必须明确：

- 默认只导出显式选择的 Database；
- 跨 Database Wikilink 受 Policy 控制；
- 敏感字段可被 omit、mask 或拒绝导出；
- 导出报告列出跳过的记录和原因；
- 输出目录不能隐式指向不安全的共享位置；
- 不把 secret、token 等系统拒绝数据写入 frontmatter；
- 导出操作需要记录 actor、snapshot 和配置。

## 19. 导出验证

导出完成后自动检查：

- 所有内部 Wikilink 是否有目标；
- 是否存在重复 `memora_id`；
- 是否存在两个对象写入同一路径；
- Router 是否覆盖有效子 Route；
- frontmatter 是否可解析；
- Markdown 文件是否为 UTF-8；
- manifest 与实际文件是否一致；
- 是否意外导出被 Policy 禁止的字段；
- 是否满足目标快照一致性。

候选报告：

```json
{
  "ok": true,
  "snapshot": 1843,
  "pages_written": 182,
  "pages_unchanged": 714,
  "links_checked": 2190,
  "broken_links": 0,
  "records_omitted_by_policy": 3,
  "manifest": "_memora/export.json"
}
```

## 20. 当前结论

1. Memora 内部数据库是权威状态，Markdown Wiki 是派生快照；
2. 每个完整语义记录自然导出为一个 Markdown 页面；
3. Router Page 导出为 `Index.md`/MOC；
4. 结构和语义 Relation 导出为 Obsidian `[[Wikilink]]`；
5. Wiki 目录优先反映 Route，而不是机械反映数据库 Table；
6. 导出使用一致 MVCC 快照；
7. 相同快照应确定性生成相同 Wiki；
8. 第一阶段只支持单向导出，不做隐式双向同步；
9. 默认只导出当前状态，历史必须显式选择；
10. Wiki Exporter 不调用 LLM，不重新解释数据；
11. 增量导出通过 `memora_id`、revision、path 和 content hash 实现；
12. 运行时 Agent 仍使用 MSQL 查询，不把导出 Vault 当回数据库索引。

## 21. 待讨论问题

1. Database、Route 和目录路径的默认命名规则是什么？
2. 文件名使用中文标题、英文 slug，还是按数据库语言决定？
3. 页面底部应该输出多少种 Relation，如何避免链接过多？
4. Table/Schema 页面默认是否导出？
5. 是否为 Obsidian 生成 tags、properties 和 graph group 配置？
6. 是否需要兼容 GitHub Wiki、MkDocs 和普通 Markdown 浏览器？
7. Redirect 页面默认保留多久？
8. 导出目录中的用户手工新增文件如何避免被下一次增量导出误删？
9. 是否允许选择部分 Route 导出，并处理跨范围 Wikilink？
10. 未来双向同步采用明确 diff/plan，还是完全不进入产品范围？
