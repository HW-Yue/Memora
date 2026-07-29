# AI 数据库与检索基础设施

状态：2026-07-29 市场快照。

## 产品比较

| 产品 | 数据与查询模型 | AI/Agent 能力 | 与 Memora 的边界 |
| --- | --- | --- | --- |
| [OceanBase seekdb](https://github.com/oceanbase/seekdb) | MySQL 兼容、ACID、SQL；结构化 + 全文 + Vector；embedded/server | COW `FORK DATABASE`/`MERGE TABLE`，面向 Agent 状态 | 最接近“传统数据库内核 + Agent 能力”；仍由应用定义 Schema 和记忆内容，官方主路径包含 Embedding/Vector |
| [MatrixOrigin Memoria](https://github.com/matrixorigin/Memoria) | MatrixOne 上的 canonical memory 和混合检索 | Snapshot、Branch、Merge、Rollback、MCP 与治理规则 | 版本治理很接近，但接口仍以 `memory_store/search/correct` 为主，不是 AI 自主通用关系数据库 |
| [HelixDB](https://github.com/HelixDB/helix-db) | Graph + Vector 为主，兼顾 KV/文档/关系；动态 JSON 查询 | MCP、内建 Embedding、Keyword 和图遍历 | Agent 能直接 walk graph，但 Embedding 与图后端是中心；没有短 Router + SQL 自主 Schema 生命周期 |
| [LanceDB](https://github.com/lancedb/lancedb) | Embedded 列式表，Vector、全文和 SQL；多模态 | 自动版本、time travel、本地 SDK | 便携和版本能力强，但面向开发者/ML 数据，不负责判断什么值得记忆或维护语义 Schema |
| [Chroma](https://docs.trychroma.com/docs/embeddings/embedding-functions) | Collection 中保存 document、metadata、ID 和 embedding | 简单 add/update/upsert/query，Embedding 可本地或 API | 易用但仍是 embedding database；精确关系、事务版本和 AI 自主建模不是核心 |
| [Qdrant](https://qdrant.tech/documentation/overview/) | Collection/Point/Vector/Payload；HTTP/gRPC | Dense+sparse hybrid、payload filter、全文 filter | 强检索基础设施；官方明确以 vector search 为中心，不提供通用非向量排名或 ontology |

## “AI-native database”在市场上的常见含义

目前这个标签主要表示：

- 原生 Vector 类型和 ANN 索引；
- 全文、Vector 和结构化过滤混合；
- 内建 Embedding 或模型推理；
- MCP/API 方便 Agent 调用；
- fork/time travel 适合 Agent 实验。

这些能力让数据库适合承载 AI 应用，但通常没有让 AI 成为数据库的持续设计者和维护者。

## Memora 需要证明的不同定义

```text
AI-native ≠ 数据库里有 Vector
AI-native = AI 自主管理语义状态，而引擎保证物理和事务正确性
```

具体表现为：AI 自主决定 Database/Table/Column、记忆价值、merge/split、关系和 Router；Agent 通过标准 MSQL 精确修改；陌生 Agent 只靠有界自描述就能接管。

## 可直接借鉴

- seekdb：MySQL 兼容语言和安全 sandbox；
- MatrixOrigin Memoria：版本 diff/branch/rollback；
- LanceDB：嵌入式、本地版本与 snapshot；
- HelixDB：Agent 直接走图以及内建 MCP；
- Qdrant：结构化过滤、严格查询预算和多路召回融合。

## 不能直接解决的部分

上述数据库不会替 Agent 判断当前对话属于哪个项目、哪些状态值得保存、该创建还是合并 Table，也不会自动把数据组织成能让下一个陌生 Agent 低 token 理解的短 Router。这正是 Memora 的语义控制面。
