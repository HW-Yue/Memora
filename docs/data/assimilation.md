# 资料吸收

状态：产品方向已确认；F189–F196 已实现 Agent-owned 长任务、临时源、Document IR、EPUB、
coverage、正式 MSQL 提交面与可恢复 claim ledger。F35/F36 旧宿主协议仅保留兼容。

## 原则

Memora 不保存完整 PDF、Markdown、图片、音频或大文档。外部资料只是 AI 的临时输入，最终数据库保存 AI 吸收后的结构化理解。

```text
外部资料
  → Agent-owned SourceStore 与 Document IR
  → 按完整语义节点有界阅读并记录 coverage
  → 匹配 Schema 与现有记录
  → 单窗口一次模型调用生成有锚点 claim 与 revise/merge/split/insert 候选
  → Agent-owned hash-chain ledger 持久化 review_required 草稿
  → 交叉检查引用、数字、冲突和遗漏
  → 独立复核
  → 短事务提交记录、关系和 Source Receipt
  → 清除 Memora 临时输入
```

临时读取窗口不是持久化 chunk，也不进入倒排索引。

目录、章节、表格、附件和页码先形成临时 inventory。每个窗口记录已读范围；关键引用未解析、覆盖不完整或复核失败时，任务必须报告“未完成”，不能静默写成长期事实。
当前长任务序列见[资料吸收 Agent Feature 序列](../planning/assimilation-agent-feature-sequence.md)；
正式提交语法见 [F195](../planning/f195-msql-assimilation-surface.md)。旧宿主的结构单元与提交协议
仍可分别从[资料清单与覆盖 v1](../agent/assimilation-coverage-v1.md)和
[资料独立复核与提交 v1](../agent/assimilation-review-v1.md)追溯，但不是内置 Agent 调用面。

## Source Receipt

可选保存很小的来源收据：

- 标题、作者、版本；
- URI 或路径提示；
- 内容哈希；
- 章节、页码、标题路径等短 source anchor；
- 吸收时间和 Agent；
- 影响的记录清单；
- 覆盖和低置信度报告。

不保存原文。Memora 也不能隐式删除用户磁盘上的源文件。

独立复核可以由隔离的验证 Agent 或同一 Agent 的第二遍检查完成。复核者只接收资料清单、候选变化和必要来源窗口，不继承第一遍的推理草稿。

## 明确取舍

丢弃原文后，Memora 不能保证：

- 逐字引用；
- 法律或证据归档；
- 重现原始版式；
- 在 AI 理解错误后自动回看原文纠正。

F36 用隔离上下文的第二遍复核和可追溯门禁降低误吸收风险，但它仍不能证明
模型理解必然正确，也不能替代需要保存原件的证据归档。

## 未决问题

- 哪些任务应要求不同模型复核，而不只是隔离上下文？
- AI 误解资料时，没有原文应如何纠错？
- 是否允许只保存外部可重新访问的 source URI？
- 低置信度内容是否应该拒绝进入长期数据库？
- 多次吸收同一资料的新版本时如何生成 diff？
- 独立复核必须使用不同模型，还是上下文隔离即可？
- 不同格式适配器怎样证明它生成的 normalized extent 没有漏项？

## 关联

- [语义记录](./semantic-records.md)
- [历史未解决痛点](../archive/planning/unresolved-pain-points.md)
