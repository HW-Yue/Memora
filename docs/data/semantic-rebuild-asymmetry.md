# 语义重建的不对称性（讨论稿，待决策）

状态：讨论稿，2026-08-11。已核对代码与规格的事实部分成立；处置方案尚未选定，
不构成实现授权。

## 事实

「后期可以重建语义索引」这句话在当前架构下只对一部分层成立：

| 层 | 能否重建 | 依据 | 对模型能力的依赖 |
| --- | --- | --- | --- |
| 倒排 postings / lexical | 能，纯机械、不需要模型 | F173c `REBUILD LEXICAL INDEX` 全量 COW replacement 与 parity receipt | 几乎为零 |
| Route 树 / 导航结构 | 大致能，可从已有 Row 重新组织 | Route Plan、reshape、schema change plan | 中等 |
| 内容分解：原文切成哪些 Row、抽出哪些 claim、什么被判定为不值得写 | **不能** | 见下 | **最高** |

第三行不能重建的原因是原始资料不被保留：

- F191 的 `SourceStore` 是显式临时存储，`ReleaseJob` 移除引用后，不再被任何 Job 引用的 Object
  被回收；规格明确「SourceStore 不是 Memora Database，不进入 MSQL、snapshot、备份、Wiki 或倒排索引」；
- F199 的 Source Receipt 明确不保存「MSQL、参数、字段正文、原始窗口、prompt 或 reviewer 推理」，
  保留的是 source locator、content digest、object_ids、revision；
- 产品宪章的永久边界是不把完整大文档作为持久化内容。

这三条都不是缺陷，是有意的设计。但合起来的推论是：**可重建的层恰好最不依赖模型能力，
最依赖模型能力的层恰好不可重建。**

## 为什么这件事重要

「何时值得写入」的 worthiness 决策是所有决策里最有损的一个，而它当前的质量评测被明确后置。
一个能力不足的模型判断某段「不值得写」之后，原文释放，该判断不可见也不可追溯：
Row 里不会留下空洞，收据里不会留下记录。

这与「过度抽取」的代价严重不对称：多写的 Row 可以后续删除，少写的内容在原文回收后无法恢复。

## 候选处置（未选定）

### 候选 A：让 Memora 引用但不拥有外部原始资料归档

收据里已有 source locator 与 content digest，缺的只是「用户重新提供原文 → 重新吸收 →
与现有 Row 做 diff」这条路径。不破坏「不把大文档作为持久化内容」的边界，因为归档由用户拥有，
Memora 只保留可校验的引用。代价是需要冻结重吸收与 diff 的语义。

### 候选 B：接受损失，并据此改吸收 Agent 的默认偏向

不引入归档，但把恢复代价的不对称显式写进 worthiness 策略：默认偏向多写，
因为过度抽取可恢复而抽取不足不可恢复。代价是数据库噪声上升，且需要可用的清理路径。

两个候选不互斥。选定前不实现任何一方。

## 关联

- [资料吸收](./assimilation.md)
- [F191 内容寻址临时 SourceStore](../planning/f191-content-addressed-source-store.md)
- [F199 短事务对账与 Source Receipt](../planning/f199-assimilation-reconciliation.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
