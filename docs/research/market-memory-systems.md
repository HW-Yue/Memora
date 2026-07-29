# Agent 记忆与个人知识产品

状态：2026-07-29 市场快照。能力来自官方资料，比较判断属于 Memora 团队分析。

## 记忆抽取与管理

| 产品 | 官方定位和数据模型 | 检索与修改 | 对 Memora 的意义 |
| --- | --- | --- | --- |
| [Mem0](https://github.com/mem0ai/mem0) | 从消息提取长期记忆，按 user/agent/run 隔离；可选 Graph Memory | Embedding 为主，图召回叠加；提供 memory API/CLI/Skill | 证明 Agent 会主动维护记忆，但仍是通用 memory layer，不是自主关系 Schema 数据库 |
| [OpenMemory](https://mem0.ai/blog/introducing-openmemory-mcp) | Mem0 的本地共享记忆服务和 UI，面向所有 MCP 客户端 | add/search/list/delete memory；跨 Cursor、Claude 等客户端共享 | 直接验证“一个本地全局记忆入口”的需求；当前安装仍包含 Docker、向量库和模型配置，不是可分发的独立数据库包 |
| [Supermemory](https://supermemory.ai/mcp/) | 云端统一 memory/context layer，通过 MCP 给多个 AI 工具共享 | 一条命令连接，自动保存、搜索和召回；另有文档连接器和混合检索 | 最接近“所有 AI 共用一份记忆”的消费定位；数据主要依赖服务端，不以可离线安装的单库为产品对象 |
| [Letta](https://docs.letta.com/guides/core-concepts/memory/memory-blocks) | 小型 Memory Block 永久放入上下文，Archival Memory 按需检索 | Block 可由 Agent 更新；[Archival Memory](https://docs.letta.com/guides/core-concepts/memory/archival-memory) 是语义向量库，Agent 主要 insert/search | “极小常驻状态 + 大型按需状态”的分层值得借鉴；始终可见 Block 不能无限增长 |
| [Zep / Graphiti](https://help.getzep.com/graphiti/getting-started/welcome) | Episode 生成带时间和 provenance 的实体、关系、Fact；支持自定义实体类型 | Embedding + BM25 + 图遍历；可[精确更新 Node/Edge](https://help.getzep.com/adding-fact-triplets)，支持事实失效时间 | 时间事实和来源关联很强；但 Schema 主要由开发者提供，Episode/Embedding/LLM 管线仍是核心 |
| [Cognee](https://docs.cognee.ai/getting-started/introduction) | 把文档构造成原始数据、概念和关系图，组合关系/图/向量存储 | remember/recall/improve/[forget](https://docs.cognee.ai/core-concepts/main-operations/forget)；保留原文件以重新处理 | 提醒我们“吸收后丢原文”会失去重建能力；其多后端管线不是单一可携带数据库 |
| [LangMem](https://github.com/langchain-ai/langmem) | Agent 可在热路径写记忆，后台 Manager 抽取、合并和更新 | 通常依赖 LangGraph Store 和 Embedding；自身更像管理工具包 | Mutation Agent 和后台整理思路可借鉴，但存储正确性仍由外部数据库负责 |
| [MemMachine](https://github.com/MemMachine/MemMachine) | Working、Episodic、Profile 三层；完整 Episode 与 SQL Profile 并存 | Graph/SQL/语义召回，支持 MCP/API | 其[保留完整 Episode 的研究](https://arxiv.org/abs/2604.04853)直接挑战有损抽取；Memora 必须证明不存原文仍能达到足够正确率 |

## 本地、便携与 Agent 接入

| 产品 | 真相源与接口 | 关键特点 | 对 Memora 的意义 |
| --- | --- | --- | --- |
| [Basic Memory](https://github.com/basicmachines-co/basic-memory) | Markdown 是真相源；SQLite/Postgres 建索引；MCP、CLI、Plugin、Skills | Wikilink 图、双向人机编辑、混合向量/全文搜索、渐进式工具发现 | 是最直接的使用体验竞品；也保留了 Markdown 同步、长文档和 Schema 松散问题 |
| [Memvid](https://github.com/memvid/memvid) | 单一 `.mv2` 文件和 append-only Smart Frame；CLI/SDK | BM25 可独立启用，向量可用本地 ONNX；时间旅行、分支和便携 | 证明单文件与本地 recall 有吸引力；Frame 更偏不可变记忆时间线，精细 Schema/SQL 修改不是核心 |
| [Pieces](https://docs.pieces.app/) | 本地后台服务持续记录跨应用工作上下文 | 桌面端搜索和问答，并连接 AI 助手、浏览器和开发工具 | 产品目标接近跨时间、跨应用的个人工作记忆；更像持续捕获的本地应用，不是可安装和交换的语义数据库 |
| [HEBBS](https://github.com/hebbs-ai/hebbs) | 用户文件是真相源，`.hebbs/` 是可携带认知层；单二进制 + Skill | 多种 recall、consolidation、decay | 证明 Skill 和便携索引可工作；仍无法摆脱源文件本身的组织质量 |
| [YantrikDB](https://github.com/yantrikos/yantrikdb-server) | Cognitive memory engine；库、MCP 或服务 | 衰减、合并、矛盾检测、多信号评分 | 记忆治理必须是一等能力；自动遗忘和合并如果缺少 revision/Undo 会很危险 |
| [AgenticMemory](https://github.com/agentralabs/agentic-memory) | 单一 `.amem` 二进制文件；typed cognitive event graph；MCP/CLI | BM25 + vector + graph，Correction/Decision/Episode 等类型 | 与“Go 单文件 + Agent”方向接近；固定认知类型不等于 AI 自主通用 Schema |
| [agentic-box/memora](https://github.com/agentic-box/memora) | SQLite/D1/S3，同名 MCP server，支持 Claude/Codex Skill | TF-IDF、本地或 API Embedding、图关系、文档 fragment、动作历史 | 存在直接命名和搜索入口冲突；其功能也与早期记忆层重合，必须重新评估公开品牌 |

## 共同模式

市场主流通常采用以下组合之一：

1. 原始对话/文档 → LLM 抽取 → 向量或图；
2. Markdown 真相源 → SQLite/Embedding 派生索引；
3. 固定 Memory 类型 → add/search/update/delete API；
4. 单文件不可变事件流 → recall/time travel；
5. 小块常驻上下文 + 大型向量档案。

Memora 的不同点不能只是“也有 MCP/Skill”或“也有图”。它必须证明 AI 能长期维护可解释的 Database/Table/Column 和可精确修改的语义 Row。

## 尚未独立验证

- 各产品官方 benchmark 的数据集、模型和成本是否可横向比较；
- 长期运行后 consolidation/decay 的误删率；
- MCP 工具数量对 Agent 上下文和选择正确率的实际影响；
- 换模型、换机器后索引和记忆是否真正无损接管。
