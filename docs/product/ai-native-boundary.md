# AI-native 产品边界

状态：方向已形成；受 [AI-native 产品宪章](./ai-native-product-charter.md)约束，仍需端到端原型验证。

## 定义

Memora 是由 AI 自主创建、建模、查询和持续修改的个人数据库。用户通过自然交流和外部资料产生信息，不负责手工建库、建表、切块、配置向量模型或整理索引。

## 职责边界

AI 负责语义层：

- 判断当前在讨论哪个项目或领域；
- 选择或创建数据库；
- 自主设计表、字段和说明；
- 判断什么值得长期保存；
- 创建、修订、合并、拆分和关联记录；
- 维护语义 Router 和数据库描述。

引擎负责物理层：

- SQL Parser、类型和约束；
- Page、B+ Tree 和倒排索引；
- MVCC、Undo Log、Redo Log 和锁；
- Page split、merge、compaction 和恢复；
- 权限、审计和版本冲突。

AI 不能操作 Page、offset、物理索引或 Redo Log。

## 核心体验

陌生 Agent 接入后应能：

1. 用标准语言发现数据库；
2. 理解数据库和 Schema 的用途；
3. 通过短语义索引选择查询范围；
4. 用 SQL 精确获取数据；
5. 用带 revision 前置条件的 SQL 修改数据；
6. 不依赖旧会话上下文接管工作。

## 非目标

- 不是文档仓库或 PDF 阅读器；
- 不是传统 RAG 的 chunk + embedding 管线；
- 不采用 Embedding、向量数据库、余弦/距离相似度或其伪装形式；
- 不是让 AI 直接读写数据库物理文件；
- 不是只能 `add/search/delete` 的记忆投递箱。

## 关联

- [AI-native 产品宪章](./ai-native-product-charter.md)
- [AI-native 产品契约](./ai-native-contract.md)
- [质量模型与验收](./quality-model.md)
- [资料吸收](../data/assimilation.md)
- [AI 自主权与约束](../agent/autonomy.md)
- [MSQL](../query/msql.md)
