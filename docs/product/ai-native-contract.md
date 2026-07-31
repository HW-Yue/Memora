# AI-native 产品契约

状态：方向性规格；受 [AI-native 产品宪章](./ai-native-product-charter.md)约束。

## 核心定义

AI-native 不等于“数据库支持 Vector”或“提供 MCP”。它表示 AI 是语义数据的持续设计者和维护者，而人不需要手工切块、建表、选字段、维护索引或整理目录。

```text
用户自然交流/提供资料
→ AI 判断范围、价值和语义结构
→ AI 通过 MSQL 提交逻辑变化
→ 引擎保证类型、事务、版本和恢复
→ 新 Agent 通过短自描述重新接管
```

## 用户体验判定

Memora 只有在用户把“数据库维护主动权”交给 AI 后仍能长期可靠工作，才算 AI-native。用户只需要自然交流、提供资料或纠正结果，不需要决定何时建库、建表、选字段、写索引、做 merge/split 或触发维护。

每次获得用户授权范围内的新输入后，AI 维护闭环应当是：

```text
理解范围和意图
→ 查询已有 Database、Schema 与 Row
→ 判断忽略、写入、修订、拆分、合并或迁移
→ 通过 MSQL 提交短事务
→ 更新语义描述、Router 与索引
→ 验证结果并返回紧凑收据
```

正常维护不要求用户审批内部结构。只有语义内容互相矛盾、高风险或大范围修改、权限扩大和不可恢复操作才打断用户；语义冲突由 Skill 展示相关内容，用户决定后再重写，数据库引擎不替用户判断。

“自动”只适用于产品已经获得的输入和权限。Memora 不能声称理解从未接收的对话、文件或应用活动；要实现日常持续维护，必须拥有稳定的会话入口、宿主事件交接或用户授权的数据连接，不能只依赖 Skill 碰巧被模型调用。

## 九项产品契约

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

Router、Data Dictionary、候选和错误输出都有硬预算。v0 查询由 Codex/Claude Code 按 Canonical Skill 完成，并通过 MSQL 接收有界结果；宿主不能把旧索引无限累积进模型上下文。未来可选的内置 Runtime 也必须遵守同一预算。

### 8. 可携带和可退出

本地 Instance 可以被复制、校验和恢复；Markdown/Obsidian 是确定性导出。换电脑或换 Agent 后不需要原聊天才能理解数据。

### 9. 配置可演化

影响语义质量和使用效果的参数不能作为不可见代码常量写死。字段长度、Router 分支与 Route Frame 预算等都应作为数据库内可读、可版本化的配置。

文档中的数值是启动默认值，不是永久真理。但配置是否在建库后冻结、只能迁移、允许用户修改或允许 AI 优化，必须逐项定义，推迟到最后阶段讨论。任何被允许的变更都必须说明原因、带 expected revision、经过 Policy、可以审计和回滚。Page 格式、事务原子性、权限上限、校验和等物理正确性与安全不变量不属于 AI 自适应配置。

## AI 与引擎边界

AI 决定：

- 领域范围和 Database；
- Table/Column/关系语义；
- 什么值得记忆；
- 哪些 Row 需要修订、合并或拆分；
- Router 和短描述怎样表达。
- 语义质量相关配置怎样根据证据迭代。

v0 的 AI 来自 Codex/Claude Code 等外部宿主；未来若增加内置 Runtime，也必须通过同一 MSQL 执行核心提交逻辑操作。

引擎决定：

- SQL 语法、类型、约束和 Policy；
- Page、Extent、Segment、索引和 Buffer Pool；
- 事务、MVCC、Undo Log、Redo Log 和恢复；
- revision 冲突、引用完整性和输出预算执行；
- LRU 失效和 Data Dictionary 版本。

引擎内部不调用 LLM 决定物理行为，AI 也不接触物理地址。

## 永久语义权威边界

Memora 的语义发现以 AI 维护、AI 可读的显式 Router 为权威，不以 Row、文档 chunk、
正文事实的 Embedding 或相似度排名代替语义结构。可回退的字面位置和 Route-only
Vector 只提供候选位置，不能直接回答、扩大授权范围或排除零命中 Table。语义能力来自：

```text
AI 维护可读的 Table 级多层语义树
+ AI 用 MSQL 逐层选择有限分支
+ 叶子只返回 RowID
+ 结构化 SQL filter 与关系遍历
+ 最终 SELECT 回表
```

候选预测器必须通过 MSQL 暴露 provenance、snapshot 和预算；命中后仍读 Router 并以
RowID SQL 回表，缺失或误预测时确定性回退普通 Router。既有将机械词项、字符向量与
事实候选混合打分的实现只可作为历史原型，不能继续充当主查询路径。

## 反例测试

出现任一情况就不能宣称已实现 AI-native：

- 用户必须手工决定 chunk、Table 或 Embedding 模型；
- 新 Agent 要先读完整 Markdown 仓库；
- Agent 只能 add/search/delete，不能精确 revise/merge/split；
- 数据库没有可读的 purpose、Schema 说明和 Router；
- 旧索引长期塞在 system prompt；
- 修改没有 revision、来源或回滚路径；
- 实现、benchmark 或兜底路径把 Row/chunk 向量或相似度结果当作事实权威；
- 自动整理造成错误后无法解释和恢复。

## 关联

- [AI-native 产品宪章](./ai-native-product-charter.md)
- [AI-native 产品边界](./ai-native-boundary.md)
- [Mutation Agent](../agent/database-mutation-agent.md)
- [质量模型与验收](./quality-model.md)
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
