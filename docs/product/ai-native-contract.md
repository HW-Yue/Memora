# AI-native 产品契约

状态：方向性规格；用于约束产品，而不是宣传文案。

## 核心定义

AI-native 不等于“数据库支持 Vector”或“提供 MCP”。它表示 AI 是语义数据的持续设计者和维护者，而人不需要手工切块、建表、选字段、维护索引或整理目录。

```text
用户自然交流/提供资料
→ AI 判断范围、价值和语义结构
→ AI 通过 MSQL 提交逻辑变化
→ 引擎保证类型、事务、版本和恢复
→ 新 Agent 通过短自描述重新接管
```

## 八项产品契约

### 1. 自主范围识别

AI 必须判断当前内容属于哪个 Database/项目，必要时新建，不能把所有内容投入一个无边界 memory collection。

### 2. 自主 Schema 生命周期

AI 可以创建和演化 Table/Column/关系；创建前必须发现现有定义和同义项，变化必须有说明、影响范围、版本和回滚路径。

### 3. 自解释接管

一个没有旧聊天记录的新 Agent 应通过 `SHOW DATABASES → DESCRIBE → ROUTE` 在有界输出内理解数据用途。数据库身份不能依赖原作者脑中的上下文。

### 4. 标准化操作

正式读写只通过版本化 MSQL。Skill 解释语法，Parser/Policy/MVCC 执行约束。Agent 不能直接改物理文件，也不能把自然语言猜测当成提交。

### 5. 语义 Row 而非 Chunk

持久化内容是短小、完整、可独立修改的认知模块。长度是预算，不是机械切分规则；关系必须结构化，不能只藏在正文。

### 6. 修改优先于追加

遇到已有主题时，AI 应在 revise、merge、split、supersede、move 中选择，而不是永远追加新记忆。所有修改带 expected revision 和审计原因。

### 7. 上下文成本是一等约束

Router、Data Dictionary、候选和错误输出都有硬预算。查询默认由 Memora 内置 Agent Runtime 完成；外部 Agent 只提交意图或直接提交 MSQL，并接收 Context Pack。Runtime 自主管理可重建的 Query Workspace，不能把旧索引无限累积进模型上下文。

### 8. 可携带和可退出

本地 Instance 可以被复制、校验和恢复；Markdown/Obsidian 是确定性导出。换电脑或换 Agent 后不需要原聊天才能理解数据。

## AI 与引擎边界

AI 决定：

- 领域范围和 Database；
- Table/Column/关系语义；
- 什么值得记忆；
- 哪些 Row 需要修订、合并或拆分；
- Router 和短描述怎样表达。

AI 可以运行在 Memora 内置 Agent Runtime 中，也可以来自外部宿主；两者必须通过同一 MSQL 执行核心提交逻辑操作。

引擎决定：

- SQL 语法、类型、约束和 Policy；
- Page、Extent、Segment、索引和 Buffer Pool；
- 事务、MVCC、Undo Log、Redo Log 和恢复；
- revision 冲突、引用完整性和输出预算执行；
- LRU 失效和 Data Dictionary 版本。

引擎内部不调用 LLM 决定物理行为，AI 也不接触物理地址。

## 第一版无向量策略

第一版不依赖 Embedding API。语义能力由以下组合提供：

```text
AI 写入可读标题、摘要、别名和 Router
+ BM25 / 中文 N-gram / 字段权重
+ 结构化 SQL filter
+ 关系正反向遍历
+ Query Agent 对自然语言意图做关键词扩展
```

Embedding 未来只能作为可删除、可重建的派生索引，不能成为数据库可读性的前提。

## 反例测试

出现任一情况就不能宣称已实现 AI-native：

- 用户必须手工决定 chunk、Table 或 Embedding 模型；
- 新 Agent 要先读完整 Markdown 仓库；
- Agent 只能 add/search/delete，不能精确 revise/merge/split；
- 数据库没有可读的 purpose、Schema 说明和 Router；
- 旧索引长期塞在 system prompt；
- 修改没有 revision、来源或回滚路径；
- 换模型后因无法重建向量而无法使用；
- 自动整理造成错误后无法解释和恢复。

## 关联

- [AI-native 产品边界](./ai-native-boundary.md)
- [Mutation Agent](../agent/database-mutation-agent.md)
- [质量模型与验收](./quality-model.md)
