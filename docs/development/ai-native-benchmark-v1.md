# AI-native Benchmark v1

状态：F42 的场景与评分器仍可作历史测试资产；F51 证据已撤销。当前 Adapter
集合包含被禁止的 Vector/cosine 路径，不能产生产品发布证据，需按产品宪章重做。

## 数据集

`benchmarks/ai-native-v1.json` 使用 `memora.ai-benchmark/v1`，固定五类旅程：

- 多项目 50 轮交替，覆盖稳定写入、瞬时忽略、跨库隔离和 checkpoint；
- 连续修订、陈旧写冲突和 COMPENSATE undo；
- 书籍 inventory、coverage、语义模块、独立复核和 Source Receipt；
- 新 Agent 不读旧聊天的冷启动接管；
- Codex 写/Claude Code 读写及反向读取的宿主切换。

每个 turn 只保存测试意图和可观察预期，不保存真实用户资料或模型输出。Suite
严格解码、ID 去重并绑定 digest，增删场景会改变报告身份。

## 八类质量维度

Adapter 必须返回原始成功数/总数，Runner 统一计算：

1. 记忆选择：write precision/recall；
2. 资料吸收：coverage、claim accuracy、anchor traceability；
3. Schema：duplicate rate；
4. 检索：Recall@5、MRR、nDCG；
5. 上下文：平均字符、工具调用和无关 Row 率；
6. 修改：unintended-row 与 revision-conflict capture；
7. 接管：cold-start success 和跨宿主等价；
8. 引擎：recovery、index consistency 和 deterministic export。

成功数不能超过对应总数，排名 milli-score 不能超过查询数；违反时用稳定
validation code 拒绝，不能把未知分母包装成满分。

## Adapter 与报告

历史格式的 Adapter 名称为 no-memory、markdown-search、sqlite-fts、vector 和
memora；其中 vector 以及依赖混合相似候选的 memora Adapter 均已失效。F42 提供
的 Scripted Adapter 只能用于评分器回归，不能作为当前产品旅程。历史
`memora.ai-benchmark-report/v1` 包含 suite/adapter、逐场景原始计数、
派生指标、宿主等价性与去除自身 hash 后计算的确定性 SHA-256。

撤销原因见 [AI-native 发布门 v1](./ai-native-release-gate-v1.md)。新版本必须改为
Table 级语义树逐层 SQL 旅程，按 `US-COLD`、`US-READ`、`US-SPLIT` 等故事验收，
且不实现任何 Vector baseline。

## 关联

- [AI-native 质量模型与验收](../product/quality-model.md)
- [Scripted Host Harness v1](./scripted-host-harness-v1.md)
- [Codex Adapter v1](./codex-adapter-v1.md)
- [Claude Code Adapter v1](./claude-code-adapter-v1.md)
