# AI-native 质量模型与验收

状态：首轮候选指标。目标值必须由真实 benchmark 校准。

## 原则

效果好不能只看“搜到了没有”。Memora 的完整质量链是：

```text
值得写 → 写得对 → 组织得对 → 找得到
→ 返回得短 → 改得准 → 能回滚 → 新 Agent 能接管
```

任何一段失败，长期记忆都会逐渐污染后续推理。

## 八类指标

| 维度 | 核心问题 | 主要指标 |
| --- | --- | --- |
| 记忆选择 | 是否只保存未来有用的状态变化 | write precision/recall、重复写入率、瞬时信息留存率 |
| 资料吸收 | 是否完整且没有曲解来源 | coverage、claim accuracy、低置信度拒绝率、source anchor 可追溯率 |
| Schema 健康 | AI 是否长期保持清晰结构 | 同义 Database/Table/Column 率、孤儿字段率、迁移失败率、描述完整率 |
| 检索 | 不同表达能否找回正确 Row | Recall@k、MRR、nDCG、关系多跳成功率、过期事实命中率 |
| 上下文 | 找回结果是否足够短且有证据 | Context Pack 字符/token、工具调用数、无关记录率、truncation 可见性 |
| 修改 | 是否准确改变目标而不误伤 | unintended-row rate、revision conflict 捕获率、merge/split 可逆率 |
| 接管 | 陌生 Agent 能否继续工作 | cold-start task success、首次正确 Database/Route、重新发现调用数 |
| 引擎 | 物理状态是否可靠 | crash recovery、atomicity、index consistency、deterministic export |

## 首轮候选门槛

这些是原型目标，不是最终承诺：

- 写入选择 precision ≥ 95%，宁可漏掉低价值内容，不污染长期库；
- 自建查询集 Recall@5 ≥ 90%，并单独统计同义表达和跨项目干扰；
- 默认 Context Pack ≤ 2400 字符、记录数 ≤ 5，超限必须显式 `truncated`；
- 所有带错误 expected revision 的写入都被拒绝；
- 单事务故障注入后不出现“数据已提交但索引不可见”的状态；
- 50 次连续自主建模后不出现未报告的同义 Table/Column；
- 同一 snapshot 重复 Wiki 导出的内容哈希一致；
- 新 Agent 不读取旧聊天也能完成核心接管任务。

## 基准场景

### 多项目连续对话

在 50～200 轮中交替讨论多个项目、个人偏好、技术和书籍，测试范围识别、值得记忆判断和跨库污染。

### 反复修订

让事实、决策、方案和 Schema 多次变化，测试 supersede、contradiction、merge/split、历史查询和回滚。

### 大资料吸收

使用带目录、交叉引用、表格和相互矛盾段落的资料，测试覆盖、来源锚点和独立复核。

### 冷启动接管

让另一个模型只通过 CLI/Skill 接手，测试能否找到正确 Database、理解 Schema 并继续修改。

### 无向量召回

查询故意使用与原文不同的同义表达，比较 Router+Agent 词项+机械 N-gram+关系与 Vector baseline 的差距，并校准两路索引权重。

## 对照组

- 无长期记忆；
- 全聊天上下文；
- Markdown 全文搜索；
- SQLite FTS/BM25；
- Basic Memory 或同类 Markdown+MCP；
- Mem0/Vector memory；
- Memora 去掉 Router、关系或 LRU 的消融版本。

Vector 产品只作为评测基线，不意味着第一版引入其运行依赖。

## 质量反馈闭环

查询结果必须允许 Agent 标记：

```text
useful / irrelevant / stale / wrong / incomplete
```

反馈不直接改事实。它生成可审计的修订候选，Mutation Agent 重新读取证据和当前 revision 后再提交。
协议和逻辑 Undo 边界见[反馈、修订与逻辑 Undo v1](../agent/feedback-revision-v1.md)。

## 发布门槛

只有当端到端 Agent 体验优于“Markdown + 搜索”基线时，才进入完整自研存储内核阶段。只有当无向量方案在核心场景达到门槛时，才把“无 Embedding 依赖”升级为正式产品承诺。

## 关联

- [AI-native 产品契约](./ai-native-contract.md)
- [开发与验证路线](../planning/roadmap.md)
- [未解决痛点](../planning/unresolved-pain-points.md)
